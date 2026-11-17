// Package backup turns a selected inventory into a data snapshot plus a meta
// snapshot (manifest, seed copies, dconf dump, root-collected system files).
package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gaborkis11/omarchy-backup-restore/internal/engine"
	"github.com/gaborkis11/omarchy-backup-restore/internal/inventory"
	"github.com/gaborkis11/omarchy-backup-restore/internal/manifest"
	"github.com/gaborkis11/omarchy-backup-restore/internal/runner"
	"github.com/gaborkis11/omarchy-backup-restore/internal/sys"
	"github.com/gaborkis11/omarchy-backup-restore/internal/version"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

// Event reports progress to the caller (TUI or CLI).
type Event struct {
	Phase    string // prepare | data | meta | check | kit | prune | done
	Message  string
	Progress *engine.Progress
	Warning  string
}

// Options for a run.
type Options struct {
	Consistent bool // back up from a read-only btrfs snapshot of /home (needs sudo)
	SkipCheck  bool
	HelperPath string // absolute path of the obr binary (for `sudo obr helper`)
	DestName   string
	Retention  []string // restic forget policy flags, empty = keep everything
}

// Result of a run.
type Result struct {
	RunID        string
	DataSnapshot string
	MetaSnapshot string
	Manifest     *manifest.Manifest
	Warnings     []string
	Duration     time.Duration
}

// RunID makes a sortable run id.
func RunID(t time.Time) string { return t.Format("20060102-150405") }

// Plan computes the restic path list and excludes for the selected items.
func Plan(env *sys.Env, inv *inventory.Inventory) (paths, excludes []string) {
	selected := map[string]bool{}
	var selPaths []string
	for _, it := range inv.Items {
		if !it.Selected {
			continue
		}
		for _, p := range it.Paths {
			if !sys.Exists(env.Abs(p)) || selected[p] {
				continue
			}
			selected[p] = true
			selPaths = append(selPaths, p)
		}
	}
	sort.Strings(selPaths)
	// drop paths nested in another selected path
	for _, p := range selPaths {
		nested := false
		for _, q := range paths {
			if strings.HasPrefix(p, q+"/") {
				nested = true
				break
			}
		}
		if !nested {
			paths = append(paths, p)
		}
	}
	excludes = append(excludes, inv.GlobalExcludes...)
	for _, it := range inv.Items {
		if it.Selected {
			excludes = append(excludes, it.Excludes...)
			continue
		}
		// unselected items nested inside a selected path must be excluded
		for _, p := range it.Paths {
			if selected[p] {
				continue
			}
			for _, q := range paths {
				if strings.HasPrefix(p, q+"/") && !containsSelected(p, selPaths) {
					excludes = append(excludes, env.Abs(p))
					break
				}
			}
		}
	}
	excludes = dedupe(excludes)
	return paths, excludes
}

func containsSelected(p string, sel []string) bool {
	for _, s := range sel {
		if strings.HasPrefix(s, p+"/") {
			return true
		}
	}
	return false
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// Run performs the backup.
func Run(ctx context.Context, env *sys.Env, inv *inventory.Inventory, loc *engine.Location, o Options, emit func(Event)) (*Result, error) {
	if emit == nil {
		emit = func(Event) {}
	}
	start := time.Now()
	res := &Result{RunID: RunID(start)}
	repo := loc.Repo
	warn := func(f string, a ...any) {
		msg := fmt.Sprintf(f, a...)
		res.Warnings = append(res.Warnings, msg)
		emit(Event{Warning: msg})
		env.Log.Print("WARN ", msg)
	}

	emit(Event{Phase: "prepare", Message: i18n.T("Mentés előkészítése…")})
	exists, err := repo.Exists(ctx)
	if err != nil {
		return nil, err
	}
	if !exists {
		emit(Event{Phase: "prepare", Message: i18n.T("Új mentési tároló létrehozása…")})
		if err := repo.Init(ctx); err != nil {
			return nil, fmt.Errorf(i18n.T("tároló létrehozása: %w"), err)
		}
	}

	metaDir, err := os.MkdirTemp(env.RuntimeDir, "obr-run-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(metaDir)
	if err := os.Chmod(metaDir, 0o700); err != nil {
		return nil, err
	}

	paths, excludes := Plan(env, inv)
	workDir := env.Home
	if o.Consistent {
		snapHome, err := createSnapshot(ctx, env, o.HelperPath)
		if err != nil {
			warn(i18n.T("Konzisztens (btrfs) pillanatkép nem sikerült, élő fájlokból mentünk: %v"), err)
		} else {
			defer deleteSnapshot(context.WithoutCancel(ctx), env, o.HelperPath)
			workDir = snapHome
			for i, ex := range excludes {
				if strings.HasPrefix(ex, env.Home+"/") {
					excludes[i] = snapHome + strings.TrimPrefix(ex, env.Home)
				}
			}
		}
	}

	// parent = newest data snapshot
	parent := ""
	if snaps, err := repo.Snapshots(ctx, "obr-data"); err == nil && len(snaps) > 0 {
		parent = snaps[0].ID
	}

	m := manifest.New(res.RunID)
	m.ObrVersion = version.Version
	m.Source = manifest.Source{Host: sys.Hostname(), User: env.User, UID: env.UID, Home: env.Home,
		OmarchyVersion: env.OmarchyVersion(ctx), Hardware: sys.DetectHardware(ctx, env)}

	var dataSum *engine.BackupSummary
	if len(paths) > 0 {
		emit(Event{Phase: "data", Message: fmt.Sprintf(i18n.T("Fájlok mentése (%d elem)…"), len(paths))})
		dataSum, err = repo.Backup(ctx, engine.BackupOpts{
			Dir: workDir, Paths: paths, Excludes: excludes, Parent: parent, TmpDir: metaDir,
			Tags: []string{"obr-data", "run-" + res.RunID}, ExcludeCaches: true,
		}, func(p engine.Progress) {
			if p.Error != "" {
				env.Log.Print("restic: ", p.Error)
				return
			}
			pp := p
			emit(Event{Phase: "data", Progress: &pp})
		})
		if err != nil {
			return nil, fmt.Errorf(i18n.T("fájlok mentése: %w"), err)
		}
		res.DataSnapshot = dataSum.SnapshotID
		m.DataSnapshot = dataSum.SnapshotID
		m.Stats = manifest.Stats{FilesNew: dataSum.FilesNew, FilesChanged: dataSum.FilesChanged,
			FilesUnchanged: dataSum.FilesUnmodified, BytesProcessed: dataSum.TotalBytesProcessed,
			BytesAdded: dataSum.DataAdded, Seconds: dataSum.TotalDuration, Incomplete: dataSum.Incomplete}
		if dataSum.Incomplete {
			for _, e := range dataSum.Errors {
				warn(i18n.T("nem olvasható, kimaradt: %s"), e)
			}
		}
	}

	// ── meta ──
	emit(Event{Phase: "meta", Message: i18n.T("Leíró adatok mentése…")})
	sel := map[string]bool{}
	for _, it := range inv.Items {
		m.Items = append(m.Items, *it)
		if it.Selected {
			sel[it.ID] = true
		}
	}
	m.Packages = inv.Packages
	m.Migrations = inv.Migrations
	m.Replay = inv.Replay
	m.System = inv.System
	m.System.EtcFiles = nil
	for _, it := range inv.Items {
		if it.Selected && strings.HasPrefix(it.ID, "etc:") {
			m.System.EtcFiles = append(m.System.EtcFiles, strings.TrimPrefix(it.ID, "etc:"))
		}
	}
	for _, sd := range inv.Seeds {
		if sel[sd.ItemID] {
			m.Seeds = append(m.Seeds, sd)
			if c, ok := inv.SeedContents[sd.Path]; ok {
				dst := filepath.Join(metaDir, "seeds", sd.Path)
				if err := os.MkdirAll(filepath.Dir(dst), 0o700); err == nil {
					_ = os.WriteFile(dst, c, 0o600)
				}
			}
		}
	}
	for _, g := range inv.GitProjects {
		if sel[g.ItemID] {
			m.GitProjects = append(m.GitProjects, g)
		}
	}
	if sel["replay:dconf"] {
		if out, err := env.Run(ctx, "dconf", "dump", "/"); err == nil {
			_ = os.WriteFile(filepath.Join(metaDir, "dconf.ini"), out.Stdout, 0o600)
		}
	} else {
		m.Replay.DconfDump = false
	}
	if needsSystemCollect(sel, m) {
		emit(Event{Phase: "meta", Message: i18n.T("Rendszerbeállítások összegyűjtése (rendszergazdai joggal)…")})
		if err := collectSystem(ctx, env, o.HelperPath, sel, m, filepath.Join(metaDir, "system.tar")); err != nil {
			warn(i18n.T("Rendszerbeállítások gyűjtése nem sikerült: %v"), err)
		} else {
			m.System.Collected = true
		}
	}
	m.Warnings = append(m.Warnings, res.Warnings...)
	if err := m.Write(filepath.Join(metaDir, "manifest.json")); err != nil {
		return nil, err
	}
	metaPaths := []string{"manifest.json"}
	for _, n := range []string{"seeds", "dconf.ini", "system.tar"} {
		if sys.Exists(filepath.Join(metaDir, n)) {
			metaPaths = append(metaPaths, n)
		}
	}
	metaSum, err := repo.Backup(ctx, engine.BackupOpts{
		Dir: metaDir, Paths: metaPaths, TmpDir: metaDir,
		Tags: []string{"obr-meta", "run-" + res.RunID, "src-" + tagSafe(m.Source.Host), "user-" + tagSafe(m.Source.User)},
	}, nil)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("leíró adatok mentése: %w"), err)
	}
	res.MetaSnapshot = metaSum.SnapshotID
	res.Manifest = m

	if !o.SkipCheck {
		emit(Event{Phase: "check", Message: i18n.T("Mentés ellenőrzése…")})
		loc.Sync(ctx)
		if err := repo.Check(ctx, ""); err != nil {
			warn(i18n.T("Az ellenőrzés hibát talált: %v"), err)
		}
	}

	// remember the selection locally for the "new since last backup" marks
	_ = SaveLast(env, o.DestName, m)

	emit(Event{Phase: "kit", Message: i18n.T("Visszaállító készlet frissítése a célhelyen…")})
	if err := WriteKit(ctx, env, loc); err != nil {
		warn(i18n.T("A visszaállító készlet (kit) frissítése nem sikerült: %v"), err)
	}

	if len(o.Retention) > 0 {
		emit(Event{Phase: "prune", Message: i18n.T("Régi mentések törlése a megőrzési szabály szerint…")})
		if n, err := Prune(ctx, repo, o.Retention); err != nil {
			warn(i18n.T("Régi mentések törlése nem sikerült: %v"), err)
		} else if n > 0 {
			emit(Event{Phase: "prune", Message: fmt.Sprintf(i18n.T("%d régi mentés törölve."), n)})
		}
	}
	loc.Sync(ctx)
	res.Duration = time.Since(start)
	emit(Event{Phase: "done", Message: i18n.T("Kész.")})
	return res, nil
}

// tagSafe makes a value usable inside a restic tag (no commas or spaces).
func tagSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ',' || r == ' ' {
			return '_'
		}
		return r
	}, s)
}

func needsSystemCollect(sel map[string]bool, m *manifest.Manifest) bool {
	return (sel["sys:network"] && len(m.System.Connections) > 0) || sel["sys:bluetooth"] ||
		(sel["sys:printers"] && len(m.System.Printers) > 0) || len(m.System.EtcFiles) > 0
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

func collectSystem(ctx context.Context, env *sys.Env, helper string, sel map[string]bool, m *manifest.Manifest, out string) error {
	args := []string{"helper", "collect"}
	if sel["sys:network"] {
		args = append(args, "--nm")
	}
	if sel["sys:bluetooth"] && m.System.BTAdapter != "" {
		args = append(args, "--bt", m.System.BTAdapter)
	}
	if sel["sys:printers"] {
		args = append(args, "--printers")
	}
	for _, f := range m.System.EtcFiles {
		args = append(args, "--etc", f)
	}
	var buf bytes.Buffer
	_, err := env.Runner.Run(ctx, runner.Cmd{Name: helperPath(helper), Args: args, Sudo: true, Stdout: &buf})
	if err != nil {
		return err
	}
	return os.WriteFile(out, buf.Bytes(), 0o600)
}

const snapshotDir = "/home/.obr-snapshot"

func createSnapshot(ctx context.Context, env *sys.Env, helper string) (string, error) {
	if !strings.HasPrefix(env.Home, "/home/") {
		return "", errors.New(i18n.T("a home nem a /home alatt van"))
	}
	if _, err := env.Runner.Run(ctx, runner.Cmd{Name: helperPath(helper), Args: []string{"helper", "snapshot-create"}, Sudo: true}); err != nil {
		return "", err
	}
	return filepath.Join(snapshotDir, strings.TrimPrefix(env.Home, "/home/")), nil
}

func deleteSnapshot(ctx context.Context, env *sys.Env, helper string) {
	_, _ = env.Runner.Run(ctx, runner.Cmd{Name: helperPath(helper), Args: []string{"helper", "snapshot-delete"}, Sudo: true})
}

// Prune applies a retention policy to meta snapshots and removes the matching
// data snapshots of the same runs. Returns the number of runs removed.
func Prune(ctx context.Context, repo *engine.Repo, policy []string) (int, error) {
	ids, err := repo.ForgetPolicy(ctx, "obr-meta", policy)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	metas, err := repo.Snapshots(ctx, "obr-meta")
	if err != nil {
		return 0, err
	}
	datas, err := repo.Snapshots(ctx, "obr-data")
	if err != nil {
		return 0, err
	}
	drop := map[string]bool{}
	for _, id := range ids {
		drop[id] = true
	}
	runs := map[string]bool{}
	var forget []string
	for _, s := range metas {
		if drop[s.ID] {
			runs[s.Tag("run-")] = true
			forget = append(forget, s.ID)
		}
	}
	for _, s := range datas {
		if runs[s.Tag("run-")] {
			forget = append(forget, s.ID)
		}
	}
	if err := repo.Forget(ctx, forget, true); err != nil {
		return 0, err
	}
	return len(runs), nil
}

// lastPath is where the most recent manifest per destination is cached.
func lastPath(env *sys.Env, dest string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '/' || r == ' ' {
			return '_'
		}
		return r
	}, dest)
	if safe == "" {
		safe = "default"
	}
	return filepath.Join(env.StateDir, "last-"+safe+".json")
}

// SaveLast caches a manifest locally (no secrets inside: only paths and labels).
func SaveLast(env *sys.Env, dest string, m *manifest.Manifest) error {
	if err := os.MkdirAll(env.StateDir, 0o700); err != nil {
		return err
	}
	if err := m.Write(lastPath(env, dest)); err != nil {
		return err
	}
	return m.Write(lastPath(env, "_any"))
}

// LoadLast returns the cached manifest for a destination ("" = most recent of any).
func LoadLast(env *sys.Env, dest string) *manifest.Manifest {
	if dest == "" {
		dest = "_any"
	}
	b, err := os.ReadFile(lastPath(env, dest))
	if err != nil {
		return nil
	}
	m, err := manifest.Parse(b)
	if err != nil {
		return nil
	}
	return m
}
