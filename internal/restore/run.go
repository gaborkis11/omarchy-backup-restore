package restore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gaborkis11/omarchy-backup-restore/internal/engine"
	"github.com/gaborkis11/omarchy-backup-restore/internal/inventory"
	"github.com/gaborkis11/omarchy-backup-restore/internal/manifest"
	"github.com/gaborkis11/omarchy-backup-restore/internal/merge"
	"github.com/gaborkis11/omarchy-backup-restore/internal/runner"
	"github.com/gaborkis11/omarchy-backup-restore/internal/sys"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

// Resolution of a config conflict.
type Resolution int

const (
	KeepMine  Resolution = iota // the backed-up user version
	TakeNew                     // the new Omarchy default
	UseMerged                   // automatic merge with conflict markers
)

// Conflict is raised when a user-modified Omarchy config file meets a changed default.
type Conflict struct {
	Path      string // home-relative
	Reason    string
	Mine      []byte
	New       []byte
	Merged    []byte
	HasMerged bool
	Diff      string // mine vs new
}

// Resolver decides conflicts (the TUI asks the user, the CLI applies a policy).
type Resolver func(Conflict) Resolution

// Options for a restore.
type Options struct {
	Selected     map[string]bool // candidate ids to restore
	UpdateSystem bool            // full system upgrade before installing packages
	Snapshot     bool            // omarchy-snapshot create first
	Migrations   []string        // migration file names to run
	Recipes      map[string]bool // git item id -> run its install recipe
	Resolver     Resolver
	HelperPath   string
	// Interactive runs a command attached to the terminal (TUI: tea.ExecProcess).
	// When nil, interactive steps (recipes) are listed as manual steps.
	Interactive func(c *exec.Cmd) error
	DryRun      bool
}

// Event is a progress report.
type Event struct {
	Step     string
	Status   string // start | ok | warn | skip | fail | info
	Message  string
	Progress *engine.Progress
}

// Report summarises the run.
type Report struct {
	Done           []string
	Warnings       []string
	Manual         []string
	WorkDir        string
	UndoDir        string
	NeedsRelogin   bool
	FailedPackages []string
}

// Journal records step state for resume/undo.
type Journal struct {
	RunID    string            `json:"run_id"`
	Started  time.Time         `json:"started"`
	Steps    map[string]string `json:"steps"`
	Replaced []string          `json:"replaced"` // home-relative paths moved to undo/
	Copied   []string          `json:"copied"`   // home-relative files copied to undo/
	Created  []string          `json:"created"`  // home-relative top-level paths that did not exist before
}

type runCtx struct {
	ctx   context.Context
	env   *sys.Env
	src   *Source
	o     Options
	emit  func(Event)
	rep   *Report
	j     *Journal
	undo  string
	stage string
	sel   map[string]manifest.Item
	hw    sys.Hardware
}

// Steps in execution order (ids are stable for the journal).
var Steps = []string{"preflight", "packages", "keyring", "system", "git", "files", "seeds", "replay", "recipes", "post", "migrations", "fixups"}

// StepTitle returns a Hungarian title for a step.
func StepTitle(s string) string {
	switch s {
	case "preflight":
		return i18n.T("Előkészítés (pillanatkép, rendszerfrissítés)")
	case "packages":
		return i18n.T("Programok telepítése (legfrissebb verzió)")
	case "keyring":
		return i18n.T("Kulcstartó visszaállítása")
	case "system":
		return i18n.T("Rendszerbeállítások (szolgáltatások, csoportok, billentyűzet, hálózat)")
	case "git":
		return i18n.T("Git-projektek letöltése")
	case "files":
		return i18n.T("Alkalmazásadatok, kulcsok és fájlok visszaállítása")
	case "seeds":
		return i18n.T("Omarchy-beállítások visszaállítása (összefésüléssel)")
	case "replay":
		return i18n.T("Téma, kapcsolók, webappok újra beállítása")
	case "recipes":
		return i18n.T("Projekt-telepítők futtatása")
	case "post":
		return i18n.T("Utólagos lépések (gh, mise…)")
	case "migrations":
		return i18n.T("Omarchy frissítő lépések (migrációk)")
	case "fixups":
		return i18n.T("Útvonalak javítása, frissítés")
	}
	return s
}

// Run executes the restore.
func Run(ctx context.Context, env *sys.Env, src *Source, o Options, emit func(Event)) (*Report, error) {
	if emit == nil {
		emit = func(Event) {}
	}
	if o.Resolver == nil {
		o.Resolver = func(Conflict) Resolution { return KeepMine }
	}
	r := &runCtx{ctx: ctx, env: env, src: src, o: o, emit: emit,
		rep:   &Report{WorkDir: src.WorkDir},
		undo:  filepath.Join(src.WorkDir, "undo"),
		stage: filepath.Join(src.WorkDir, "stage"),
		sel:   map[string]manifest.Item{},
		hw:    sys.DetectHardware(ctx, env)}
	r.rep.UndoDir = r.undo
	r.j = loadJournal(src.WorkDir)
	if r.j.RunID == "" {
		r.j = &Journal{RunID: src.Manifest.RunID, Started: time.Now(), Steps: map[string]string{}}
	}
	for _, it := range src.Manifest.Items {
		if it.Selected && o.Selected[it.ID] {
			r.sel[it.ID] = it
		}
	}
	for _, d := range []string{r.undo, r.stage} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return r.rep, err
		}
	}
	steps := map[string]func() error{
		"preflight": r.preflight, "packages": r.packages, "keyring": r.keyring, "system": r.system,
		"git": r.git, "files": r.files, "seeds": r.seeds, "replay": r.replay, "recipes": r.recipes,
		"post": r.post, "migrations": r.migrations, "fixups": r.fixups,
	}
	for _, s := range Steps {
		if r.j.Steps[s] == "ok" {
			emit(Event{Step: s, Status: "skip", Message: i18n.T("már kész (korábbi futás)")})
			continue
		}
		emit(Event{Step: s, Status: "start", Message: StepTitle(s)})
		err := steps[s]()
		if err != nil {
			r.j.Steps[s] = "fail"
			r.saveJournal()
			emit(Event{Step: s, Status: "fail", Message: err.Error()})
			if errors.Is(err, context.Canceled) {
				return r.rep, err
			}
			r.warn("%s: %v", StepTitle(s), err)
			continue
		}
		r.j.Steps[s] = "ok"
		r.saveJournal()
		emit(Event{Step: s, Status: "ok"})
	}
	return r.rep, nil
}

func (r *runCtx) warn(f string, a ...any) {
	msg := fmt.Sprintf(f, a...)
	r.rep.Warnings = append(r.rep.Warnings, msg)
	r.env.Log.Print("WARN ", msg)
	r.emit(Event{Status: "warn", Message: msg})
}

func (r *runCtx) info(f string, a ...any) {
	msg := fmt.Sprintf(f, a...)
	r.rep.Done = append(r.rep.Done, msg)
	r.env.Log.Print(msg)
	r.emit(Event{Status: "info", Message: msg})
}

func (r *runCtx) manual(f string, a ...any) {
	r.rep.Manual = append(r.rep.Manual, fmt.Sprintf(f, a...))
}

func (r *runCtx) run(name string, args ...string) (runner.Result, error) {
	if r.o.DryRun {
		r.env.Log.Printf("dry-run: %s %s", name, strings.Join(args, " "))
		return runner.Result{}, nil
	}
	return r.env.Runner.Run(r.ctx, runner.Cmd{Name: name, Args: args})
}

func (r *runCtx) sudo(name string, args ...string) (runner.Result, error) {
	if r.o.DryRun {
		r.env.Log.Printf("dry-run: sudo %s %s", name, strings.Join(args, " "))
		return runner.Result{}, nil
	}
	return r.env.Runner.Run(r.ctx, runner.Cmd{Name: name, Args: args, Sudo: true})
}

func (r *runCtx) has(id string) bool { _, ok := r.sel[id]; return ok }

func journalPath(dir string) string { return filepath.Join(dir, "journal.json") }

func loadJournal(dir string) *Journal {
	j := &Journal{Steps: map[string]string{}}
	if b, err := os.ReadFile(journalPath(dir)); err == nil {
		_ = json.Unmarshal(b, j)
	}
	if j.Steps == nil {
		j.Steps = map[string]string{}
	}
	return j
}

func (r *runCtx) saveJournal() {
	b, _ := json.MarshalIndent(r.j, "", "  ")
	_ = os.WriteFile(journalPath(r.src.WorkDir), b, 0o600)
	sel := []string{}
	for id := range r.sel {
		sel = append(sel, id)
	}
	sort.Strings(sel)
	b, _ = json.MarshalIndent(map[string]any{"selected": sel, "backup": r.src.Backup.Meta.ID, "destination": r.src.Loc.Dest.Name}, "", "  ")
	_ = os.WriteFile(filepath.Join(r.src.WorkDir, "selection.json"), b, 0o600)
}

// ───────────────────────── steps ─────────────────────────

func (r *runCtx) preflight() error {
	if r.o.Snapshot {
		if _, err := r.run("omarchy-snapshot", "create"); err != nil {
			if runner.Code(err) == 127 {
				r.info("%s", i18n.T("Rendszer-pillanatkép kihagyva (nincs snapper)."))
			} else {
				r.warn(i18n.T("Rendszer-pillanatkép nem sikerült: %v"), err)
			}
		} else {
			r.info("%s", i18n.T("Rendszer-pillanatkép elkészült (a boot menüből visszaállítható)."))
		}
	}
	if r.o.UpdateSystem && r.needPackages() {
		r.emit(Event{Status: "info", Message: i18n.T("Rendszer frissítése (ez eltarthat egy ideig)…")})
		var err error
		if _, lerr := exec.LookPath("omarchy-update-system-pkgs"); lerr == nil {
			_, err = r.run("omarchy-update-system-pkgs")
		} else {
			_, err = r.sudo("pacman", "-Syu", "--noconfirm")
		}
		if err != nil {
			r.warn(i18n.T("Rendszerfrissítés nem sikerült: %v"), err)
		} else {
			r.info("%s", i18n.T("A rendszer naprakész."))
		}
	}
	return nil
}

func (r *runCtx) needPackages() bool {
	for _, it := range r.sel {
		if len(it.Packages) > 0 {
			return true
		}
	}
	return false
}

func (r *runCtx) packages() error {
	foreign := map[string]bool{}
	for _, p := range r.src.Manifest.Packages {
		if p.Foreign {
			foreign[p.Name] = true
		}
	}
	want := map[string]bool{}
	for _, it := range r.sel {
		if it.Kind == inventory.KindPackage {
			for _, p := range it.Packages {
				want[p] = true
			}
		}
	}
	// packages needed by selected app data (e.g. google-chrome for its profile)
	for _, it := range r.sel {
		if it.Kind != inventory.KindPackage {
			for _, p := range it.Packages {
				if _, ok := r.sel["pkg:"+p]; ok || inBackupPackages(r.src.Manifest, p) {
					want[p] = true
				}
			}
		}
	}
	var repo, aur []string
	names := sortedKeys(want)
	for _, n := range names {
		if sys.Installed(r.ctx, r.env, n) {
			continue
		}
		switch {
		case !foreign[n] && sys.Available(r.ctx, r.env, n):
			repo = append(repo, n)
		case foreign[n]:
			aur = append(aur, n)
		default:
			r.warn(i18n.T("A(z) %s csomag nem található a tárolókban – kihagyva."), n)
			r.rep.FailedPackages = append(r.rep.FailedPackages, n)
		}
	}
	if len(repo) == 0 && len(aur) == 0 {
		r.info("%s", i18n.T("Minden kiválasztott program telepítve van."))
		return nil
	}
	if len(repo) > 0 {
		r.emit(Event{Status: "info", Message: i18n.T("Telepítés: ") + strings.Join(repo, ", ")})
		if _, err := r.run("omarchy-pkg-add", repo...); err != nil {
			// retry one by one to find the culprit(s)
			for _, n := range repo {
				if _, err := r.run("omarchy-pkg-add", n); err != nil {
					r.warn(i18n.T("%s telepítése nem sikerült: %v"), n, err)
					r.rep.FailedPackages = append(r.rep.FailedPackages, n)
				}
			}
		}
	}
	for _, n := range aur {
		r.emit(Event{Status: "info", Message: i18n.T("Telepítés AUR-ból: ") + n})
		if _, err := r.run("omarchy-pkg-aur-add", n); err != nil {
			r.warn(i18n.T("%s (AUR) telepítése nem sikerült: %v"), n, err)
			r.rep.FailedPackages = append(r.rep.FailedPackages, n)
		}
	}
	ok := len(repo) + len(aur) - len(r.rep.FailedPackages)
	if ok > 0 {
		r.info(i18n.T("%d program telepítve."), ok)
	}
	return nil
}

func inBackupPackages(m *manifest.Manifest, name string) bool {
	for _, p := range m.Packages {
		if p.Name == name && p.Class == "user" {
			return true
		}
	}
	return false
}

func (r *runCtx) keyring() error {
	it, ok := r.sel["secret:keyring"]
	if !ok {
		return nil
	}
	if pids, _ := r.env.Out(r.ctx, "pgrep", "-x", "chrome"); pids != "" {
		r.warn("%s", i18n.T("A Chrome fut – zárd be, különben a kulcstartó visszaállítása után a belépések nem lesznek olvashatók."))
	}
	_, _ = r.run("systemctl", "--user", "stop", "gnome-keyring-daemon.service", "gnome-keyring-daemon.socket")
	err := r.restorePaths([]manifest.Item{withReplace(it)}, nil)
	_, _ = r.run("systemctl", "--user", "start", "gnome-keyring-daemon.socket")
	if err != nil {
		return err
	}
	r.rep.NeedsRelogin = true
	r.info("%s", i18n.T("Kulcstartó visszaállítva (a teljes érvényesüléshez jelentkezz ki és be)."))
	return nil
}

func withReplace(it manifest.Item) manifest.Item { it.Replace = true; return it }

func (r *runCtx) system() error {
	sy := r.src.Manifest.System
	if r.has("sys:units") {
		for _, u := range sy.Units {
			if _, err := r.env.Run(r.ctx, "systemctl", "cat", u); err != nil {
				r.warn(i18n.T("A(z) %s szolgáltatás nem létezik ezen a gépen (hiányzik a programja?) – kihagyva."), u)
				continue
			}
			if out, _ := r.env.Out(r.ctx, "systemctl", "is-enabled", u); out == "enabled" {
				continue
			}
			if _, err := r.sudo("systemctl", "enable", "--now", u); err != nil {
				r.warn(i18n.T("%s bekapcsolása nem sikerült: %v"), u, err)
			} else {
				r.info(i18n.T("Szolgáltatás bekapcsolva: %s"), u)
			}
		}
	}
	if r.has("sys:user-units") {
		for _, u := range sy.UserUnits {
			if _, err := r.env.Run(r.ctx, "systemctl", "--user", "cat", u); err != nil {
				r.warn(i18n.T("A(z) %s felhasználói szolgáltatás nem létezik – kihagyva."), u)
				continue
			}
			if _, err := r.run("systemctl", "--user", "enable", "--now", u); err != nil {
				r.warn(i18n.T("%s bekapcsolása nem sikerült: %v"), u, err)
			} else {
				r.info(i18n.T("Háttérszolgáltatás bekapcsolva: %s"), u)
			}
		}
	}
	if r.has("sys:groups") {
		cur, _ := r.env.Out(r.ctx, "id", "-nG")
		have := map[string]bool{}
		for _, g := range strings.Fields(cur) {
			have[g] = true
		}
		for _, g := range sy.Groups {
			if have[g] || g == r.src.Manifest.Source.User {
				continue
			}
			if _, err := r.env.Run(r.ctx, "getent", "group", g); err != nil {
				r.warn(i18n.T("A(z) %s csoport nem létezik ezen a gépen – kihagyva."), g)
				continue
			}
			if _, err := r.sudo("usermod", "-aG", g, r.env.User); err != nil {
				r.warn(i18n.T("Csoport (%s) hozzáadása nem sikerült: %v"), g, err)
			} else {
				r.info(i18n.T("Csoporttagság: %s"), g)
				r.rep.NeedsRelogin = true
			}
		}
	}
	if r.has("sys:locale") {
		cur, _ := r.env.Out(r.ctx, "localectl", "status")
		if sy.Keymap != "" && !strings.Contains(cur, "VC Keymap: "+sy.Keymap) {
			if _, err := r.sudo("localectl", "set-keymap", "--no-convert", sy.Keymap); err != nil {
				r.warn(i18n.T("Konzol-billentyűzet: %v"), err)
			}
		}
		if sy.X11Layout != "" && !strings.Contains(cur, "X11 Layout: "+sy.X11Layout) {
			args := []string{"set-x11-keymap", "--no-convert", sy.X11Layout, sy.X11Model, sy.X11Variant, sy.X11Options}
			if _, err := r.sudo("localectl", args...); err != nil {
				r.warn(i18n.T("Billentyűzetkiosztás: %v"), err)
			}
		}
		if tz, _ := r.env.Out(r.ctx, "timedatectl", "show", "-p", "Timezone", "--value"); sy.Timezone != "" && tz != sy.Timezone {
			if _, err := r.sudo("timedatectl", "set-timezone", sy.Timezone); err != nil {
				r.warn(i18n.T("Időzóna: %v"), err)
			}
		}
		r.info("%s", i18n.T("Billentyűzet és időzóna beállítva."))
	}
	if r.has("sys:hostname") && sy.Hostname != "" && sy.Hostname != sys.Hostname() {
		if _, err := r.sudo("hostnamectl", "set-hostname", sy.Hostname); err != nil {
			r.warn(i18n.T("Gépnév: %v"), err)
		} else {
			r.info(i18n.T("Gépnév: %s"), sy.Hostname)
		}
	}
	return r.applySystemTar()
}

func (r *runCtx) applySystemTar() error {
	sy := r.src.Manifest.System
	tarPath := filepath.Join(r.src.MetaDir, "system.tar")
	if !sy.Collected || !sys.Exists(tarPath) {
		return nil
	}
	args := []string{"helper", "apply"}
	any := false
	if r.has("sys:network") {
		args = append(args, "--nm")
		any = true
	}
	if r.has("sys:bluetooth") && sy.BTAdapter != "" && len(r.hw.BTAdapters) > 0 {
		args = append(args, "--bt", r.hw.BTAdapters[0], "--bt-from", sy.BTAdapter)
		any = true
	}
	if r.has("sys:printers") {
		args = append(args, "--printers")
		any = true
	}
	for id := range r.sel {
		if strings.HasPrefix(id, "etc:") {
			args = append(args, "--etc", strings.TrimPrefix(id, "etc:"))
			any = true
		}
	}
	if !any {
		return nil
	}
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	if r.o.DryRun {
		return nil
	}
	var out bytes.Buffer
	_, err = r.env.Runner.Run(r.ctx, runner.Cmd{Name: helperPath(r.o.HelperPath), Args: args, Stdin: f, Sudo: true, Stdout: &out})
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if l != "" {
			r.env.Log.Print("helper: ", l)
		}
	}
	if err != nil {
		return fmt.Errorf(i18n.T("rendszerfájlok visszaállítása: %w"), err)
	}
	r.info("%s", i18n.T("Hálózati kapcsolatok / Bluetooth / nyomtatók / rendszerfájlok visszaállítva."))
	return nil
}

func helperPath(p string) string {
	if p != "" {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			return r
		}
		return exe
	}
	return "obr"
}

func (r *runCtx) git() error {
	for _, g := range r.src.Manifest.GitProjects {
		if !r.has(g.ItemID) || g.Full || g.Remote == "" {
			continue
		}
		dst := r.env.Abs(g.Path)
		if sys.IsDir(filepath.Join(dst, ".git")) {
			if remote, _ := r.env.Out(r.ctx, "git", "-C", dst, "remote", "get-url", "origin"); remote == g.Remote {
				if _, err := r.run("git", "-C", dst, "pull", "--ff-only"); err != nil {
					r.warn(i18n.T("%s frissítése (git pull) nem sikerült: %v"), g.Path, err)
				}
				continue
			}
		}
		if sys.Exists(dst) && len(listDir(dst)) > 0 {
			if err := r.moveToUndo(g.Path); err != nil {
				return err
			}
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		args := []string{"clone"}
		if g.Branch != "" && g.Branch != "HEAD" {
			args = append(args, "--branch", g.Branch)
		}
		args = append(args, g.Remote, dst)
		r.emit(Event{Status: "info", Message: i18n.T("Letöltés: ") + g.Remote})
		if _, err := r.run("git", args...); err != nil {
			r.warn(i18n.T("%s letöltése nem sikerült (%v). A helyi fájlok ettől még visszakerülnek."), g.Remote, err)
			continue
		}
		r.j.Created = append(r.j.Created, g.Path)
		r.info(i18n.T("Git-projekt letöltve: %s"), g.Path)
	}
	return nil
}

// files restores everything that is plain file data (apps, secrets, webapps,
// launchers, personal files, models, other, full git projects, kept files).
func (r *runCtx) files() error {
	var items []manifest.Item
	for _, it := range r.sel {
		if it.ID == "secret:keyring" || it.Kind == inventory.KindSeed || len(it.Paths) == 0 {
			continue
		}
		items = append(items, it)
	}
	if len(items) == 0 {
		return nil
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return r.restorePaths(items, nil)
}

// restorePaths restores the given items' paths directly into the home.
func (r *runCtx) restorePaths(items []manifest.Item, progressLabel func(engine.Progress)) error {
	var includes []string
	inc := map[string]bool{}
	for _, it := range items {
		for _, p := range it.Paths {
			if !inc[p] {
				inc[p] = true
				includes = append(includes, "/"+p)
			}
		}
	}
	if len(includes) == 0 {
		return nil
	}
	excludes := r.restoreExcludes(inc)
	// items that replace whole folders: move the current ones aside
	for _, it := range items {
		if !it.Replace {
			continue
		}
		for _, p := range it.Paths {
			if sys.Exists(r.env.Abs(p)) {
				if err := r.moveToUndo(p); err != nil {
					return err
				}
			}
		}
	}
	for _, p := range includes {
		if !sys.Exists(r.env.Abs(p)) {
			r.j.Created = append(r.j.Created, strings.TrimPrefix(p, "/"))
		}
	}
	snap := r.src.Manifest.DataSnapshot
	if snap == "" {
		return errors.New(i18n.T("a mentés nem tartalmaz fájlokat"))
	}
	opts := engine.RestoreOpts{Snapshot: snap, SubPath: "/", Target: r.env.Home, Includes: includes,
		Overwrite: "if-changed", TmpDir: r.src.WorkDir}
	// find files that would be overwritten and keep a copy for undo
	dry := opts
	dry.DryRun = true
	if sum, err := r.src.Loc.Repo.RestoreWithExcludes(r.ctx, dry, excludes, nil); err == nil {
		for _, it := range sum.Updated {
			rel := r.relTarget(it)
			if rel != "" {
				if err := r.copyToUndo(rel); err != nil {
					r.warn(i18n.T("undo másolat (%s): %v"), rel, err)
				}
			}
		}
	}
	if r.o.DryRun {
		return nil
	}
	sum, err := r.src.Loc.Repo.RestoreWithExcludes(r.ctx, opts, excludes, func(p engine.Progress) {
		pp := p
		r.emit(Event{Progress: &pp})
	})
	if err != nil {
		return err
	}
	for _, e := range sum.Errors {
		r.warn(i18n.T("visszaállítás: %s"), e)
	}
	r.info(i18n.T("%d fájl visszaállítva (%s)."), sum.FilesRestored, inventory.HumanSize(sum.BytesRestored))
	return nil
}

// restoreExcludes excludes unselected items nested in included paths.
func (r *runCtx) restoreExcludes(inc map[string]bool) []string {
	var out []string
	for _, it := range r.src.Manifest.Items {
		if _, ok := r.sel[it.ID]; ok || !it.Selected {
			continue
		}
		for _, p := range it.Paths {
			if inc[p] {
				continue
			}
			for q := range inc {
				if strings.HasPrefix(p, q+"/") {
					out = append(out, "/"+p)
					break
				}
			}
		}
	}
	return out
}

// relTarget converts a restic item path to a home-relative path.
func (r *runCtx) relTarget(item string) string {
	if strings.HasPrefix(item, r.env.Home+"/") {
		return strings.TrimPrefix(item, r.env.Home+"/")
	}
	return strings.TrimPrefix(item, "/")
}

func (r *runCtx) moveToUndo(rel string) error {
	src := r.env.Abs(rel)
	dst := filepath.Join(r.undo, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if sys.Exists(dst) {
		_ = os.RemoveAll(dst)
	}
	if r.o.DryRun {
		return nil
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf(i18n.T("félretétel (%s): %w"), rel, err)
	}
	r.j.Replaced = append(r.j.Replaced, rel)
	return nil
}

func (r *runCtx) copyToUndo(rel string) error {
	src := r.env.Abs(rel)
	st, err := os.Lstat(src)
	if err != nil || st.IsDir() {
		return nil
	}
	dst := filepath.Join(r.undo, rel)
	if sys.Exists(dst) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := copyPath(src, dst); err != nil {
		return err
	}
	r.j.Copied = append(r.j.Copied, rel)
	return nil
}

func copyPath(src, dst string) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		t, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(t, dst)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// seeds restores Omarchy config files with a three-way merge against the
// defaults of the Omarchy version installed here.
func (r *runCtx) seeds() error {
	var paths []string
	var seeds []manifest.Seed
	for _, sd := range r.src.Manifest.Seeds {
		if r.has(sd.ItemID) {
			paths = append(paths, "/"+sd.Path)
			seeds = append(seeds, sd)
		}
	}
	if len(seeds) == 0 {
		return nil
	}
	if _, err := r.src.Loc.Repo.Restore(r.ctx, engine.RestoreOpts{Snapshot: r.src.Manifest.DataSnapshot, SubPath: "/",
		Target: r.stage, Includes: paths, TmpDir: r.src.WorkDir}, nil); err != nil {
		return fmt.Errorf(i18n.T("beállítófájlok letöltése: %w"), err)
	}
	ts := time.Now().Format("20060102-150405")
	nMerged, nConf := 0, 0
	for _, sd := range seeds {
		mine, err := os.ReadFile(filepath.Join(r.stage, sd.Path))
		if err != nil {
			if lt, lerr := os.Readlink(filepath.Join(r.stage, sd.Path)); lerr == nil {
				// symlinked config (e.g. a plugin link): restore the link as-is
				home := r.env.Abs(sd.Path)
				_ = r.copyToUndo(sd.Path)
				_ = os.Remove(home)
				_ = os.MkdirAll(filepath.Dir(home), 0o755)
				_ = os.Symlink(lt, home)
				continue
			}
			r.warn(i18n.T("%s hiányzik a mentésből"), sd.Path)
			continue
		}
		home := r.env.Abs(sd.Path)
		cur, curErr := os.ReadFile(home)
		if curErr == nil && bytes.Equal(cur, mine) {
			continue
		}
		newDefault, ndErr := os.ReadFile(filepath.Join(r.env.SkelDir, sd.Path))
		base, _ := os.ReadFile(filepath.Join(r.src.MetaDir, "seeds", sd.Path))
		write := mine
		switch {
		case curErr != nil:
			// not present here: just put the user's version in place
		case ndErr != nil:
			// no default on this Omarchy: the user's file wins
		case sd.State == "modified" && bytes.Equal(base, newDefault):
			// the default did not change since the backup: user's version
		default:
			c := Conflict{Path: sd.Path, Mine: mine, New: newDefault}
			if strings.HasSuffix(sd.Path, ".json") && merge.SchemaVersion(mine) != merge.SchemaVersion(newDefault) {
				c.Reason = i18n.T("Az új Omarchy más formátumú (séma) beállításfájlt használ.")
			} else if sd.State == "modified" && len(base) > 0 {
				mr, err := merge.ThreeWay(r.ctx, r.env.Runner, r.src.WorkDir, mine, base, newDefault)
				if err == nil && !mr.Conflicts {
					write = mr.Merged
					nMerged++
					break
				}
				if err == nil {
					c.Merged, c.HasMerged = mr.Merged, true
				}
				c.Reason = i18n.T("Az Omarchy azóta megváltoztatta ezt az alapbeállítást, és a módosításaid ütköznek vele.")
			} else {
				c.Reason = i18n.T("Az új Omarchy már tartalmaz ilyen fájlt, ami eltér a tiédtől.")
			}
			if c.Reason != "" {
				c.Diff = merge.Diff(r.ctx, r.env.Runner, r.src.WorkDir, mine, newDefault, i18n.T("a-tiéd"), i18n.T("új-alap"))
				nConf++
				switch r.o.Resolver(c) {
				case KeepMine:
					write = mine
					_ = writeFileMode(home+".obr-uj-alap-"+ts, newDefault, 0o644)
				case TakeNew:
					write = newDefault
					_ = writeFileMode(home+".obr-sajat-"+ts, mine, 0o644)
				case UseMerged:
					write = c.Merged
					if len(write) == 0 {
						write = mine
					}
				}
			}
		}
		if r.o.DryRun {
			continue
		}
		if curErr == nil {
			_ = r.copyToUndo(sd.Path)
		} else {
			r.j.Created = append(r.j.Created, sd.Path)
		}
		mode := os.FileMode(0o644)
		if st, err := os.Stat(filepath.Join(r.stage, sd.Path)); err == nil {
			mode = st.Mode().Perm()
		}
		if err := os.MkdirAll(filepath.Dir(home), 0o755); err != nil {
			return err
		}
		if err := writeFileMode(home, write, mode); err != nil {
			return err
		}
	}
	msg := fmt.Sprintf(i18n.T("%d Omarchy-beállításfájl visszaállítva"), len(seeds))
	if nMerged > 0 {
		msg += fmt.Sprintf(i18n.T(", %d automatikusan összefésülve az új alapértékekkel"), nMerged)
	}
	if nConf > 0 {
		msg += fmt.Sprintf(i18n.T(", %d ütközés eldöntve"), nConf)
	}
	r.info("%s.", msg)
	return nil
}

func writeFileMode(p string, b []byte, mode os.FileMode) error {
	tmp := p + ".obr-tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (r *runCtx) replay() error {
	rp := r.src.Manifest.Replay
	home := r.env.Home
	if r.has("replay:theme") {
		for _, t := range rp.UserThemes {
			dir := filepath.Join(home, ".config/omarchy/themes", t.Name)
			if t.GitURL != "" && !sys.Exists(dir) {
				if _, err := r.run("git", "clone", t.GitURL, dir); err != nil {
					r.warn(i18n.T("A(z) %s téma letöltése nem sikerült: %v"), t.Name, err)
				}
			}
		}
		if rp.Theme != "" {
			cur, _ := os.ReadFile(filepath.Join(home, ".local/state/omarchy/current/theme.name"))
			if strings.TrimSpace(string(cur)) != rp.Theme {
				if _, err := r.run("omarchy-theme-set", rp.Theme); err != nil {
					r.warn(i18n.T("Téma beállítása (%s): %v"), rp.Theme, err)
				} else {
					r.info(i18n.T("Téma: %s"), rp.Theme)
				}
			}
		}
		if rp.Font != "" {
			if cur, err := r.env.Out(r.ctx, "omarchy-font-current"); err == nil && cur != rp.Font {
				if _, err := r.run("omarchy-font-set", rp.Font); err != nil {
					r.warn(i18n.T("Betűtípus (%s): %v"), rp.Font, err)
				} else {
					r.info(i18n.T("Betűtípus: %s"), rp.Font)
				}
			}
		}
	}
	if r.has("replay:toggles") {
		for _, t := range rp.HyprToggles {
			if sys.Exists(filepath.Join(home, ".local/state/omarchy/toggles/hypr", t+".lua")) {
				continue
			}
			if _, err := r.run("omarchy-hyprland-toggle", t, "on"); err != nil {
				r.warn(i18n.T("Kapcsoló (%s): %v"), t, err)
			}
		}
		for _, t := range rp.FlagToggles {
			if _, err := r.run("omarchy-toggle", t, "on"); err != nil {
				r.warn(i18n.T("Kapcsoló (%s): %v"), t, err)
			}
		}
		r.info(i18n.T("Kapcsolók: %s"), strings.Join(append(append([]string{}, rp.HyprToggles...), rp.FlagToggles...), ", "))
	}
	if r.has("replay:dconf") {
		p := filepath.Join(r.src.MetaDir, "dconf.ini")
		if f, err := os.Open(p); err == nil {
			if !r.o.DryRun {
				_, err = r.env.Runner.Run(r.ctx, runner.Cmd{Name: "dconf", Args: []string{"load", "/"}, Stdin: f})
			}
			f.Close()
			if err != nil {
				r.warn("dconf: %v", err)
			} else {
				r.info("%s", i18n.T("GTK/GNOME beállítások betöltve."))
			}
		}
	}
	n := 0
	for _, w := range rp.Webapps {
		if !r.has(w.ItemID) {
			continue
		}
		args := []string{w.Name, w.URL, w.Icon}
		if w.CustomExec != "" || w.MimeTypes != "" {
			args = append(args, w.CustomExec, w.MimeTypes)
		}
		if _, err := r.run("omarchy-webapp-install", args...); err != nil {
			r.warn(i18n.T("Webapp (%s) újralétrehozása nem sikerült, a mentett indító marad: %v"), w.Name, err)
			continue
		}
		n++
	}
	if n > 0 {
		r.info(i18n.T("%d webapp létrehozva."), n)
	}
	return nil
}

func (r *runCtx) recipes() error {
	for _, g := range r.src.Manifest.GitProjects {
		if !r.has(g.ItemID) || g.Recipe == "" {
			continue
		}
		dir := r.env.Abs(g.Path)
		if !r.o.Recipes[g.ItemID] || r.o.Interactive == nil {
			r.manual(i18n.T("%s telepítője: cd %s && %s"), g.Path, dir, g.Recipe)
			continue
		}
		c := exec.Command("bash", "-lc", g.Recipe)
		c.Dir = dir
		if err := r.o.Interactive(c); err != nil {
			r.warn(i18n.T("%s telepítője hibával állt le: %v"), g.Path, err)
			r.manual(i18n.T("%s telepítőjének újrafuttatása: cd %s && %s"), g.Path, dir, g.Recipe)
		} else {
			r.info(i18n.T("%s telepítője lefutott."), g.Path)
		}
	}
	return nil
}

func (r *runCtx) post() error {
	seen := map[string]bool{}
	for _, id := range sortedKeys(selKeys(r.sel)) {
		for _, c := range r.sel[id].Post {
			if seen[c] {
				continue
			}
			seen[c] = true
			bin := strings.Fields(c)[0]
			if _, err := exec.LookPath(bin); err != nil {
				continue
			}
			if r.o.DryRun {
				continue
			}
			res, err := r.env.Runner.Run(r.ctx, runner.Cmd{Name: "bash", Args: []string{"-lc", c}, Dir: r.env.Home})
			if err != nil {
				r.warn("%s: %v", c, err)
				_ = res
			} else {
				r.info(i18n.T("Lefutott: %s"), c)
			}
		}
	}
	return nil
}

func (r *runCtx) migrations() error {
	if len(r.o.Migrations) == 0 {
		return nil
	}
	tmp := filepath.Join(r.src.WorkDir, "migration-state")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return err
	}
	run := map[string]bool{}
	for _, m := range r.o.Migrations {
		run[m] = true
	}
	ents, _ := os.ReadDir(filepath.Join(r.env.OmarchyPath, "migrations"))
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".sh") && !run[e.Name()] {
			_ = os.WriteFile(filepath.Join(tmp, e.Name()), nil, 0o600)
		}
	}
	if r.o.DryRun {
		return nil
	}
	_, err := r.env.Runner.Run(r.ctx, runner.Cmd{Name: "omarchy-migrate", Env: []string{"OMARCHY_MIGRATION_STATE=" + tmp}})
	if err != nil {
		return fmt.Errorf("omarchy-migrate: %w", err)
	}
	r.info(i18n.T("%d Omarchy frissítő lépés lefutott a visszaállított beállításokon."), len(r.o.Migrations))
	return nil
}

func (r *runCtx) fixups() error {
	src := r.src.Manifest.Source
	var rels []string
	for _, it := range r.sel {
		rels = append(rels, it.Paths...)
	}
	for _, sd := range r.src.Manifest.Seeds {
		if r.has(sd.ItemID) {
			rels = append(rels, sd.Path)
		}
	}
	if !r.o.DryRun {
		if changed := merge.Fixup(r.env.Home, rels, src.Home, r.env.Home, src.UID, r.env.UID); len(changed) > 0 {
			r.info(i18n.T("%d fájlban javítottuk a régi home-útvonalat (%s → %s)."), len(changed), src.Home, r.env.Home)
		}
		if merge.FixGitCredentialHelper(filepath.Join(r.env.Home, ".config/git/config")) {
			r.info("%s", i18n.T("Git: a GitHub hitelesítő út javítva."))
		}
	}
	apps := filepath.Join(r.env.Home, ".local/share/applications")
	if sys.IsDir(apps) {
		_, _ = r.run("update-desktop-database", apps)
	}
	if icons := filepath.Join(r.env.Home, ".local/share/icons/hicolor"); sys.IsDir(icons) {
		_, _ = r.run("gtk-update-icon-cache", "-f", "-t", icons)
	}
	if os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" {
		_, _ = r.run("hyprctl", "reload")
	}
	if r.has("sys:network") || r.has("sys:bluetooth") {
		r.info("%s", i18n.T("Hálózat/Bluetooth: ha valami nem jelenik meg, indítsd újra a gépet."))
	}
	if r.has("pkg:tailscale") || sys.Exists("/usr/bin/tailscale") && r.has("sys:units") {
		r.manual("%s", i18n.T("Tailscale: jelentkezz be újra: sudo tailscale up --operator=$USER --accept-routes"))
	}
	if r.rep.NeedsRelogin {
		r.manual("%s", i18n.T("Jelentkezz ki és be (vagy indítsd újra a gépet), hogy a kulcstartó és a csoporttagság érvényesüljön."))
	}
	return nil
}

func listDir(p string) []string {
	ents, err := os.ReadDir(p)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func selKeys(m map[string]manifest.Item) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

// Undo puts back what a restore replaced or overwrote.
func Undo(env *sys.Env, workDir string) (int, error) {
	j := loadJournal(workDir)
	undo := filepath.Join(workDir, "undo")
	after := filepath.Join(workDir, "after-undo")
	n := 0
	for _, rel := range j.Replaced {
		cur := env.Abs(rel)
		if sys.Exists(cur) {
			_ = os.MkdirAll(filepath.Dir(filepath.Join(after, rel)), 0o700)
			if err := os.Rename(cur, filepath.Join(after, rel)); err != nil {
				return n, err
			}
		}
		if err := os.MkdirAll(filepath.Dir(cur), 0o755); err != nil {
			return n, err
		}
		if err := os.Rename(filepath.Join(undo, rel), cur); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return n, err
		}
		n++
	}
	for _, rel := range j.Copied {
		src := filepath.Join(undo, rel)
		if !sys.Exists(src) {
			continue
		}
		dst := env.Abs(rel)
		_ = os.Remove(dst)
		if err := copyPath(src, dst); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
