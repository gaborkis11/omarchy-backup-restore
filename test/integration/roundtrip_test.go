// Package integration runs a full backup → restore round trip against fixture
// homes with the real restic binary and a recording fake for everything that
// would touch the real system (pacman, systemctl, omarchy-*, sudo…).
package integration

import (
	"bytes"
	"context"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gaborkis11/omarchy-backup-restore/internal/backup"
	"github.com/gaborkis11/omarchy-backup-restore/internal/catalog"
	"github.com/gaborkis11/omarchy-backup-restore/internal/config"
	"github.com/gaborkis11/omarchy-backup-restore/internal/engine"
	"github.com/gaborkis11/omarchy-backup-restore/internal/inventory"
	"github.com/gaborkis11/omarchy-backup-restore/internal/restore"
	"github.com/gaborkis11/omarchy-backup-restore/internal/runner"
	"github.com/gaborkis11/omarchy-backup-restore/internal/sys"
)

type fx struct {
	t      *testing.T
	root   string
	restic string
	fake   *runner.Fake
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("hiányzik: %s", p)
	}
	return string(b)
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func findRestic() string {
	home, _ := os.UserHomeDir()
	for _, c := range []string{os.Getenv("OBR_TEST_RESTIC"), filepath.Join(home, ".local/share/obr/bin/restic")} {
		if c != "" && exists(c) {
			return c
		}
	}
	p, _ := exec.LookPath("restic")
	return p
}

func newFx(t *testing.T) *fx {
	restic := findRestic()
	if restic == "" {
		t.Skip("restic nem található (make tools)")
	}
	f := &fx{t: t, root: t.TempDir(), restic: restic, fake: &runner.Fake{}}
	real := &runner.Exec{}
	f.fake.Fallback = func(c runner.Cmd) (runner.Result, error) {
		switch c.Name {
		case restic, "git", "sync":
			return real.Run(context.Background(), c)
		}
		return runner.Result{}, nil
	}
	notFound := &runner.ExitError{Code: 1}
	f.fake.On("pacman -Qqe", "zed\nbase\nnvidia-container-toolkit\n", nil)
	f.fake.On("pacman -Qqm", "", nil)
	f.fake.On("pacman -Qi", "Name            : zed\nVersion         : 1.0\nProvides        : None\n\nName            : base\nVersion         : 3\nProvides        : None\n\nName            : nvidia-container-toolkit\nVersion         : 1\nProvides        : None\n\n", nil)
	f.fake.On("pacman -T", "", notFound) // nothing installed on the target
	f.fake.On("pacman -Qo", "", notFound)
	f.fake.On("omarchy-pkg-defaults", "base\n", nil)
	f.fake.On("omarchy-version", "4.5.0-3", nil)
	f.fake.On("omarchy-font-current", "JetBrainsMono Nerd Font", nil)
	f.fake.On("id -nG", "olduser wheel docker", nil)
	f.fake.On("localectl status", "System Locale: LANG=en_US.UTF-8\n    VC Keymap: hu\n   X11 Layout: hu\n", nil)
	f.fake.On("timedatectl show", "Europe/Budapest", nil)
	f.fake.On("dconf dump", "[org/gnome/desktop/interface]\ncolor-scheme='prefer-dark'\n", nil)
	f.fake.On("pgrep", "", notFound)
	f.fake.On("bluetoothctl", "", notFound)
	f.fake.On("nmcli", "", notFound)
	f.fake.On("lpstat", "", notFound)
	f.fake.On("findmnt", "", notFound)
	f.fake.On("df", "", notFound)
	return f
}

func (f *fx) p(rel string) string { return filepath.Join(f.root, rel) }

func (f *fx) env(home string) *sys.Env {
	return &sys.Env{
		Home: home, User: filepath.Base(home), UID: 1000, GID: 1000,
		OmarchyPath: f.p("omarchy"), SkelDir: f.p("skel"), EtcDir: f.p("etc"),
		RuntimeDir: f.p("run"), StateDir: filepath.Join(home, ".local/state/obr"),
		ConfigDir: filepath.Join(home, ".config/obr"), BinDir: filepath.Join(home, ".local/share/obr/bin"),
		Runner: f.fake, Log: log.New(io.Discard, "", 0),
	}
}

func (f *fx) setup() (homeA, homeB string) {
	t := f.t
	_ = os.MkdirAll(f.p("run"), 0o700)
	// Omarchy install tree
	write(t, f.p("omarchy/bin/.keep"), "")
	write(t, f.p("omarchy/default/hypr/toggles/bar-glassy.lua"), "-- glassy\n")
	write(t, f.p("omarchy/migrations/100.sh"), "echo old\n")
	write(t, f.p("omarchy/migrations/200.sh"), "# Fix the bar\nsed -i s/a/b/ ~/.config/omarchy/shell.json\n")
	// skel
	write(t, f.p("skel/.config/hypr/hyprland.lua"), "require(\"default\")\nrequire(\"hypr.monitors\")\n")
	write(t, f.p("skel/.config/hypr/monitors.lua"), "-- monitors\n")
	write(t, f.p("skel/.config/omarchy/shell.json"), "{\"version\":1,\"bar\":{}}\n")
	write(t, f.p("skel/.bashrc"), "source omarchy\n")
	write(t, f.p("skel/.local/share/applications/WhatsApp.desktop"), "[Desktop Entry]\nName=WhatsApp\nExec=omarchy-launch-webapp https://web.whatsapp.com/\n")
	write(t, f.p("skel/.local/state/omarchy/toggles/hypr/flags.lua"), "-- flags\n")
	write(t, f.p("skel/.local/state/omarchy/migrations/100.sh"), "")
	write(t, f.p("skel/.local/state/omarchy/migrations/200.sh"), "")
	// /etc fixture: installed a day ago, cronie enabled later
	write(t, f.p("etc/machine-id"), "abc\n")
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(f.p("etc/machine-id"), old, old)
	_ = os.MkdirAll(f.p("etc/systemd/system/multi-user.target.wants"), 0o755)
	_ = os.Symlink("/usr/lib/systemd/system/cronie.service", f.p("etc/systemd/system/multi-user.target.wants/cronie.service"))

	homeA = f.p("home/olduser")
	w := func(rel, c string) { write(t, filepath.Join(homeA, rel), c) }
	w(".config/hypr/hyprland.lua", "require(\"default\")\nrequire(\"hypr.monitors\")\nrequire(\"hypr.snap\")\n")
	w(".config/hypr/monitors.lua", "-- monitors\n")
	w(".config/hypr/snap.lua", "-- snap zones\n")
	w(".config/omarchy/shell.json", "{\"version\":1,\"bar\":{\"glassy\":true}}\n")
	w(".bashrc", "source omarchy\nexport FOO=1\n")
	w(".config/google-chrome/Default/Preferences", "{}")
	w(".config/google-chrome/Default/Cookies", "secret-cookies")
	w(".config/google-chrome/Default/Cache/data_0", "cache!")
	w(".config/chrome-flags.conf", "--password-store=gnome-libsecret\n")
	w(".local/share/keyrings/Default_keyring.keyring", "[keyring]\n")
	w(".local/share/applications/WhatsApp.desktop", "[Desktop Entry]\nName=WhatsApp\nExec=omarchy-launch-webapp https://web.whatsapp.com/\n")
	w(".local/share/applications/MyApp.desktop", "[Desktop Entry]\nName=MyApp\nExec=omarchy-launch-webapp https://my.example/\nIcon=myapp\n")
	w(".local/share/icons/hicolor/256x256/apps/myapp.png", "PNG")
	w(".local/share/applications/tool.desktop", "[Desktop Entry]\nName=Tool\nExec="+homeA+"/Applications/tool/run.sh\n")
	w(".local/state/omarchy/current/theme.name", "starship\n")
	w(".local/state/omarchy/toggles/hypr/bar-glassy.lua", "-- glassy\n")
	w(".local/state/omarchy/toggles/hypr/flags.lua", "-- flags\n")
	w(".local/state/omarchy/migrations/100.sh", "")
	w("Documents/notes.txt", "hello")
	w(".config/unknownapp/settings.ini", "x=1")
	w(".cache/big/junk.bin", "junk")
	w("Applications/proj/main.go", "package main\n")
	w("Applications/proj/venv/lib/huge.so", "venv")
	_ = os.MkdirAll(filepath.Join(homeA, ".config/systemd/user"), 0o755)
	if out, err := exec.Command("git", "-C", filepath.Join(homeA, "Applications/proj"), "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}

	// fresh target home = copy of skel
	homeB = f.p("home/newuser")
	if out, err := exec.Command("cp", "-a", f.p("skel")+"/.", homeB).CombinedOutput(); err != nil {
		_ = os.MkdirAll(homeB, 0o755)
		if out2, err2 := exec.Command("cp", "-a", f.p("skel")+"/.", homeB+"/").CombinedOutput(); err2 != nil {
			t.Fatalf("cp skel: %v %s %s", err, out, out2)
		}
	}
	return homeA, homeB
}

func (f *fx) location(env *sys.Env) *engine.Location {
	loc, err := engine.Resolve(context.Background(), env.Runner, config.Destination{Name: "test", Type: config.Local, Path: f.p("drive")},
		engine.Credentials{RepoPassword: "correct horse battery"}, f.restic, "")
	if err != nil {
		f.t.Fatal(err)
	}
	return loc
}

func TestRoundTrip(t *testing.T) {
	f := newFx(t)
	homeA, homeB := f.setup()
	ctx := context.Background()
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}

	// ── scan + backup on the "old" machine ──
	envA := f.env(homeA)
	inv, err := (&inventory.Scanner{Env: envA, Catalog: cat}).Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mustItem := func(id string) *inventory.Item {
		it := inv.ByID(id)
		if it == nil {
			var ids []string
			for _, x := range inv.Items {
				ids = append(ids, x.ID)
			}
			t.Fatalf("hiányzó tétel: %s (van: %s)", id, strings.Join(ids, ", "))
		}
		return it
	}
	mustItem("seed:.config/hypr")
	mustItem("seed:.config/omarchy")
	mustItem("seed:.bashrc")
	mustItem("app:google-chrome")
	mustItem("secret:google-chrome")
	mustItem("secret:keyring")
	mustItem("webapp:MyApp")
	mustItem("launcher:tool.desktop")
	mustItem("replay:theme")
	mustItem("replay:toggles")
	mustItem("replay:dconf")
	mustItem("pkg:zed")
	nv := mustItem("pkg:nvidia-container-toolkit")
	if nv.Hardware != "nvidia" {
		t.Errorf("nvidia csomag hardvercímke: %q", nv.Hardware)
	}
	mustItem("files:Documents")
	mustItem("other:.config/unknownapp")
	mustItem("git:Applications/proj")
	mustItem("sys:units")
	if inv.ByID("webapp:WhatsApp") != nil {
		t.Error("a gyári WhatsApp webappot nem kellene menteni")
	}
	if it := inv.ByID("seed:.config/hypr"); len(it.Paths) != 2 {
		t.Errorf("hypr seed utak: %v (hyprland.lua + snap.lua kell)", it.Paths)
	}
	for _, it := range inv.Items {
		for _, p := range it.Paths {
			if strings.HasPrefix(p, ".cache") {
				t.Errorf("gyorsítótár a mentésben: %s (%s)", p, it.ID)
			}
		}
	}

	loc := f.location(envA)
	res, err := backup.Run(ctx, envA, inv, loc, backup.Options{DestName: "test", SkipCheck: false}, nil)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if res.DataSnapshot == "" || res.MetaSnapshot == "" {
		t.Fatalf("hiányzó snapshot: %+v", res)
	}
	if !exists(filepath.Join(loc.Base, "kit/restore.sh")) || !exists(filepath.Join(loc.Base, "LEIRAS.txt")) {
		t.Error("a kit nem jött létre")
	}

	// second backup: incremental + new item detection
	write(t, filepath.Join(homeA, ".config/newapp/conf"), "new")
	prev := backup.LoadLast(envA, "test")
	if prev == nil {
		t.Fatal("az utolsó manifest nincs elmentve")
	}
	inv2, err := (&inventory.Scanner{Env: envA, Catalog: cat, Prev: prev}).Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if inv2.Status["other:.config/newapp"] != "new" {
		t.Errorf("az új mappa nincs „új\"-ként jelölve: %v", inv2.Status)
	}
	res2, err := backup.Run(ctx, envA, inv2, loc, backup.Options{DestName: "test", SkipCheck: true}, nil)
	if err != nil {
		t.Fatalf("második backup: %v", err)
	}
	if res2.Manifest.Stats.FilesUnchanged == 0 {
		t.Errorf("a második mentés nem növekményes: %+v", res2.Manifest.Stats)
	}

	// ── restore on the "new" machine (different user, newer default for shell.json) ──
	write(t, f.p("skel/.config/omarchy/shell.json"), "{\"version\":1,\"bar\":{},\"dock\":true}\n")
	write(t, filepath.Join(homeB, ".config/omarchy/shell.json"), "{\"version\":1,\"bar\":{},\"dock\":true}\n")
	envB := f.env(homeB)
	locB := f.location(envB)
	backups, err := restore.ListBackups(ctx, locB)
	if err != nil || len(backups) != 2 {
		t.Fatalf("mentések: %v %d", err, len(backups))
	}
	src, err := restore.Load(ctx, envB, locB, backups[0])
	if err != nil {
		t.Fatal(err)
	}
	cands := restore.Candidates(ctx, envB, src)
	sel := map[string]bool{}
	for _, c := range cands {
		if c.ID == "pkg:nvidia-container-toolkit" {
			continue // hardware gate depends on the test machine
		}
		if c.Available {
			sel[c.ID] = true
		}
	}
	pending := restore.PendingMigrations(envB, src)
	if len(pending) != 1 || pending[0].Name != "200.sh" || !pending[0].TouchesConfig {
		t.Errorf("függő migrációk: %+v", pending)
	}
	conflicts := 0
	rep, err := restore.Run(ctx, envB, src, restore.Options{
		Selected: sel, Snapshot: true, Migrations: []string{"200.sh"},
		Resolver: func(c restore.Conflict) restore.Resolution { conflicts++; return restore.KeepMine },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range rep.Warnings {
		t.Logf("figyelmeztetés: %s", w)
	}

	// files
	if got := read(t, filepath.Join(homeB, ".config/hypr/snap.lua")); got != "-- snap zones\n" {
		t.Errorf("snap.lua: %q", got)
	}
	if got := read(t, filepath.Join(homeB, ".config/hypr/hyprland.lua")); !strings.Contains(got, "hypr.snap") {
		t.Errorf("hyprland.lua nem tartalmazza a snap sort: %q", got)
	}
	// shell.json: user changed bar, Omarchy added dock → clean 3-way merge
	if got := read(t, filepath.Join(homeB, ".config/omarchy/shell.json")); !strings.Contains(got, "glassy") {
		t.Errorf("shell.json: %q", got)
	}
	if !strings.Contains(read(t, filepath.Join(homeB, ".bashrc")), "FOO=1") {
		t.Error(".bashrc nem állt vissza")
	}
	if read(t, filepath.Join(homeB, ".config/google-chrome/Default/Cookies")) != "secret-cookies" {
		t.Error("Chrome sütik hiányoznak")
	}
	if exists(filepath.Join(homeB, ".config/google-chrome/Default/Cache/data_0")) {
		t.Error("a Chrome gyorsítótára is visszajött")
	}
	if !exists(filepath.Join(homeB, ".local/share/keyrings/Default_keyring.keyring")) {
		t.Error("kulcstartó hiányzik")
	}
	if read(t, filepath.Join(homeB, "Documents/notes.txt")) != "hello" {
		t.Error("személyes fájl hiányzik")
	}
	if !exists(filepath.Join(homeB, ".config/unknownapp/settings.ini")) || !exists(filepath.Join(homeB, ".config/newapp/conf")) {
		t.Error("„Egyéb\" tételek hiányoznak")
	}
	if !exists(filepath.Join(homeB, "Applications/proj/main.go")) || exists(filepath.Join(homeB, "Applications/proj/venv")) {
		t.Error("git-projekt: main.go kell, venv nem")
	}
	if exists(filepath.Join(homeB, ".cache/big")) {
		t.Error("gyorsítótár visszajött")
	}
	// path fixup in the custom launcher
	if got := read(t, filepath.Join(homeB, ".local/share/applications/tool.desktop")); !strings.Contains(got, homeB+"/Applications/tool/run.sh") {
		t.Errorf("útvonal-javítás: %q", got)
	}

	// replayed commands
	cmds := strings.Join(f.fake.Commands(), "\n")
	for _, want := range []string{
		"omarchy-pkg-add zed",
		"omarchy-snapshot create",
		"omarchy-theme-set starship",
		"omarchy-hyprland-toggle bar-glassy on",
		"omarchy-webapp-install MyApp https://my.example/ myapp",
		"dconf load /",
		"sudo systemctl enable --now cronie.service",
		"omarchy-migrate",
	} {
		if !strings.Contains(cmds, want) {
			t.Errorf("hiányzó parancs: %q", want)
		}
	}
	if strings.Contains(cmds, "omarchy-webapp-install WhatsApp") {
		t.Error("a gyári webappot nem kellene újra létrehozni")
	}
	if strings.Contains(cmds, "omarchy-hyprland-toggle flags") {
		t.Error("a gyári flags kapcsolót nem kellene visszajátszani")
	}

	// undo puts the original Omarchy default shell.json back
	n, err := restore.Undo(envB, src.WorkDir)
	if err != nil || n == 0 {
		t.Fatalf("undo: %v %d", err, n)
	}
	if got := read(t, filepath.Join(homeB, ".config/omarchy/shell.json")); strings.Contains(got, "glassy") {
		t.Errorf("undo után a shell.json: %q", got)
	}
}

func TestWrongPassword(t *testing.T) {
	f := newFx(t)
	homeA, _ := f.setup()
	envA := f.env(homeA)
	loc := f.location(envA)
	if err := loc.Repo.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	bad, err := engine.Resolve(context.Background(), envA.Runner, config.Destination{Name: "test", Type: config.Local, Path: f.p("drive")},
		engine.Credentials{RepoPassword: "wrong"}, f.restic, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = bad.Repo.Snapshots(context.Background())
	if err != engine.ErrBadPass {
		t.Fatalf("hibás jelszóra várt hiba, kaptuk: %v", err)
	}
	var buf bytes.Buffer
	_ = buf
}
