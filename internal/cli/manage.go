package cli

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gaborkis11/omarchy-backup-restore/internal/config"
	"github.com/gaborkis11/omarchy-backup-restore/internal/engine"
	"github.com/gaborkis11/omarchy-backup-restore/internal/tui"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

func cmdDest(ctx context.Context, args []string) error {
	app, err := newApp("", false)
	if err != nil {
		return err
	}
	defer app.Close()
	if len(args) == 0 || args[0] == "list" {
		if len(app.Config.Destinations) == 0 {
			fmt.Println(i18n.T("Nincs beállított célhely. Példa: obr dest add --name BACKUP --path /run/media/$USER/BACKUP"))
			return nil
		}
		for _, d := range app.Config.Destinations {
			fmt.Println(d.Describe())
		}
		return nil
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("dest add", flag.ContinueOnError)
		var d config.Destination
		fs.StringVar(&d.Name, "name", "", i18n.T("név"))
		fs.StringVar(&d.Type, "type", config.Local, "local | sftp | smb")
		fs.StringVar(&d.Path, "path", "", i18n.T("helyi mappa (local)"))
		fs.StringVar(&d.Host, "host", "", i18n.T("NAS címe (pl. 192.168.1.10)"))
		fs.IntVar(&d.Port, "port", 0, i18n.T("port (alapértelmezett: 22 / 445)"))
		fs.StringVar(&d.User, "user", "", i18n.T("NAS felhasználó"))
		fs.StringVar(&d.Share, "share", "", i18n.T("megosztás (Synology: pl. backup)"))
		fs.StringVar(&d.Folder, "folder", "", i18n.T("almappa a megosztáson belül"))
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if err := completeDestination(ctx, app, &d); err != nil {
			return err
		}
		app.Config.Upsert(d)
		if err := app.Config.Save(); err != nil {
			return err
		}
		fmt.Println(i18n.T("Célhely mentve:"), d.Describe())
		return nil
	case "remove", "rm":
		if len(args) < 2 {
			return fmt.Errorf("%s", i18n.T("add meg a nevét"))
		}
		app.Config.Remove(args[1])
		config.DeleteSecret(app.Env.ConfigDir, "repo", args[1])
		config.DeleteSecret(app.Env.ConfigDir, "nas", args[1])
		return app.Config.Save()
	}
	return fmt.Errorf(i18n.T("ismeretlen: obr dest %s"), args[0])
}

// completeDestination validates and fills in a destination (UUID for drives,
// host key for SFTP).
func completeDestination(ctx context.Context, app *App, d *config.Destination) error {
	switch d.Type {
	case config.Local:
		if d.Path == "" {
			return fmt.Errorf("%s", i18n.T("helyi célhoz kell --path"))
		}
		p, err := filepath.Abs(d.Path)
		if err != nil {
			return err
		}
		d.Path = p
		if out, err := app.Env.Out(ctx, "findmnt", "-n", "-o", "UUID,LABEL,TARGET", "--target", p); err == nil {
			f := strings.Fields(out)
			if len(f) >= 3 && f[len(f)-1] != "/" && f[len(f)-1] != "/home" {
				d.UUID = f[0]
				if len(f) >= 3 {
					d.Label = f[1]
				}
			}
		}
		if d.Name == "" {
			d.Name = filepath.Base(p)
		}
	case config.SFTP, config.SMB:
		if d.Host == "" || d.User == "" || d.Share == "" {
			return fmt.Errorf("%s", i18n.T("NAS-hoz kell --host, --user és --share"))
		}
		if d.Name == "" {
			d.Name = "NAS"
		}
		if d.Type == config.SFTP {
			if err := tui.LearnHostKey(ctx, app.Env, d); err != nil {
				fmt.Fprintf(os.Stderr, i18n.T("Figyelem: a NAS SSH-kulcsát nem sikerült lekérni (%v).\n"), err)
			}
		}
	default:
		return fmt.Errorf(i18n.T("ismeretlen típus: %s"), d.Type)
	}
	return nil
}

const unitService = `[Unit]
Description=Omarchy Backup & Restore – scheduled backup (%s)
After=network-online.target

[Service]
Type=oneshot
ExecStart=%s backup --unattended --dest "%s"
Nice=10
IOSchedulingClass=idle
`

const unitTimer = `[Unit]
Description=Omarchy Backup & Restore – scheduled backup timer

[Timer]
OnCalendar=%s
Persistent=true
RandomizedDelaySec=10min

[Install]
WantedBy=timers.target
`

func cmdSchedule(ctx context.Context, args []string) error {
	app, err := newApp("", false)
	if err != nil {
		return err
	}
	defer app.Close()
	if len(args) == 0 {
		args = []string{"status"}
	}
	unitDir := filepath.Join(app.Env.Home, ".config/systemd/user")
	switch args[0] {
	case "status":
		s := app.Config.Schedule
		if !s.Enabled {
			fmt.Println(i18n.T("Ütemezett mentés: kikapcsolva."))
			return nil
		}
		fmt.Printf(i18n.T("Ütemezett mentés: bekapcsolva → %s (%s)\n"), s.Destination, s.OnCalendar)
		out, _ := app.Env.Out(ctx, "systemctl", "--user", "list-timers", "obr-backup.timer", "--no-pager")
		fmt.Println(out)
		return nil
	case "enable":
		fs := flag.NewFlagSet("schedule enable", flag.ContinueOnError)
		var d destFlags
		d.register(fs)
		when := fs.String("when", "daily", i18n.T("daily | weekly | systemd OnCalendar kifejezés (pl. \"*-*-* 20:00\")"))
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		dest, err := app.destination(&d)
		if err != nil {
			return err
		}
		fmt.Println(tui.ScheduleWarning())
		if !confirm(i18n.T("Elfogadod, hogy a jelszó a gépen tárolódjon?"), false) {
			return fmt.Errorf("%s", i18n.T("megszakítva"))
		}
		loc, err := app.open(ctx, &d, true)
		if err != nil {
			return err
		}
		if ok, err := loc.Repo.Exists(ctx); err != nil {
			return err
		} else if !ok {
			if err := loc.Repo.Init(ctx); err != nil {
				return err
			}
		}
		pw := envValue(loc.Repo.Env, "RESTIC_PASSWORD")
		if err := config.WriteSecret(app.Env.ConfigDir, "repo", dest.Name, pw); err != nil {
			return err
		}
		if dest.Remote() {
			nas, _ := secretFrom(d.nasPassFile, "OBR_NAS_PASSWORD")
			if nas == "" {
				if nas, err = askPassword(i18n.T("A NAS jelszava (tároláshoz): "), false); err != nil {
					return err
				}
			}
			if err := config.WriteSecret(app.Env.ConfigDir, "nas", dest.Name, nas); err != nil {
				return err
			}
		}
		return EnableSchedule(ctx, app, dest.Name, *when, unitDir)
	case "disable":
		_, _ = app.Env.Run(ctx, "systemctl", "--user", "disable", "--now", "obr-backup.timer")
		_ = os.Remove(filepath.Join(unitDir, "obr-backup.timer"))
		_ = os.Remove(filepath.Join(unitDir, "obr-backup.service"))
		_, _ = app.Env.Run(ctx, "systemctl", "--user", "daemon-reload")
		if app.Config.Schedule.Destination != "" && confirm(i18n.T("Töröljük a tárolt jelszavakat is?"), true) {
			config.DeleteSecret(app.Env.ConfigDir, "repo", app.Config.Schedule.Destination)
			config.DeleteSecret(app.Env.ConfigDir, "nas", app.Config.Schedule.Destination)
		}
		app.Config.Schedule = config.Schedule{}
		if err := app.Config.Save(); err != nil {
			return err
		}
		fmt.Println(i18n.T("Ütemezett mentés kikapcsolva."))
		return nil
	}
	return fmt.Errorf(i18n.T("ismeretlen: obr schedule %s"), args[0])
}

// EnableSchedule writes and enables the systemd user units.
func EnableSchedule(ctx context.Context, app *App, dest, when, unitDir string) error {
	cal := when
	switch when {
	case "daily", "naponta":
		cal = "*-*-* 20:00:00"
	case "weekly", "hetente":
		cal = "Sun *-*-* 20:00:00"
	}
	bin := filepath.Join(app.Env.BinDir, "obr")
	if !fileExists(bin) {
		if exe, err := os.Executable(); err == nil {
			bin = exe
		}
	}
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(unitDir, "obr-backup.service"), []byte(fmt.Sprintf(unitService, dest, bin, dest)), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(unitDir, "obr-backup.timer"), []byte(fmt.Sprintf(unitTimer, cal)), 0o644); err != nil {
		return err
	}
	if _, err := app.Env.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if _, err := app.Env.Run(ctx, "systemctl", "--user", "enable", "--now", "obr-backup.timer"); err != nil {
		return err
	}
	app.Config.Schedule = config.Schedule{Enabled: true, Destination: dest, OnCalendar: cal}
	if err := app.Config.Save(); err != nil {
		return err
	}
	fmt.Printf(i18n.T("Ütemezett mentés bekapcsolva: %s → %s\n"), cal, dest)
	return nil
}

func envValue(env []string, k string) string {
	for _, e := range env {
		if strings.HasPrefix(e, k+"=") {
			return strings.TrimPrefix(e, k+"=")
		}
	}
	return ""
}

//go:embed icon.svg
var iconSVG []byte

const desktopEntry = `[Desktop Entry]
Version=1.0
Name=%s
Comment=%s
Exec=xdg-terminal-exec --app-id=TUI.float -e %s
Terminal=false
Type=Application
Icon=obr
Categories=System;Utility;
StartupNotify=true
`

var launchers = []struct{ file, name, comment, args string }{
	{"obr.desktop", "Omarchy Backup & Restore", "Mentés, visszaállítás, ütemezés, beállítások", ""},
	{"obr-backup.desktop", "Omarchy mentés", "Mentés készítése most (a legutóbb használt helyre)", " tui backup"},
	{"obr-restore.desktop", "Omarchy visszaállítás", "Egy mentés visszaállítása erre a gépre", " tui restore"},
}

// cmdInstall copies obr (and restic/rclone found next to it) into
// ~/.local/share/obr/bin, links ~/.local/bin/obr and adds a launcher.
func cmdInstall(ctx context.Context, args []string) error {
	app, err := newApp("", false)
	if err != nil {
		return err
	}
	defer app.Close()
	bin := app.Env.BinDir
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	dst := filepath.Join(bin, "obr")
	if exe != dst {
		b, err := os.ReadFile(exe)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst+".tmp", b, 0o755); err != nil {
			return err
		}
		if err := os.Rename(dst+".tmp", dst); err != nil {
			return err
		}
	}
	if engine.FindTool(bin, "restic") == "" {
		fmt.Fprintf(os.Stderr, i18n.T("Figyelem: a restic nincs meg (%s), enélkül nem lehet menteni. Fejlesztői gépen: make tools\n"), bin)
	}
	lb := filepath.Join(app.Env.Home, ".local/bin")
	_ = os.MkdirAll(lb, 0o755)
	_ = os.Remove(filepath.Join(lb, "obr"))
	if err := os.Symlink(dst, filepath.Join(lb, "obr")); err != nil {
		return err
	}
	iconDir := filepath.Join(app.Env.Home, ".local/share/icons/hicolor/scalable/apps")
	_ = os.MkdirAll(iconDir, 0o755)
	_ = os.WriteFile(filepath.Join(iconDir, "obr.svg"), iconSVG, 0o644)
	apps := filepath.Join(app.Env.Home, ".local/share/applications")
	_ = os.MkdirAll(apps, 0o755)
	for _, l := range launchers {
		body := fmt.Sprintf(desktopEntry, i18n.T(l.name), i18n.T(l.comment), dst+l.args)
		if err := os.WriteFile(filepath.Join(apps, l.file), []byte(body), 0o644); err != nil {
			return err
		}
	}
	_ = exec.Command("update-desktop-database", apps).Run()
	// Mac-like: the app icon in the Omarchy dock (idempotent)
	if p, err := exec.LookPath("omarchy-dock"); err == nil {
		_ = exec.Command(p, "pin", "obr").Run()
	}
	_ = exec.Command("gtk-update-icon-cache", "-f", "-t", filepath.Join(app.Env.Home, ".local/share/icons/hicolor")).Run()
	fmt.Printf(i18n.T("Telepítve: %s\nIndítás: dock-ikon, Super + Space → „Omarchy Backup & Restore\", vagy terminálban: obr\n"), dst)
	return nil
}

func runTUI(ctx context.Context, mode string) error {
	app, err := newApp("", false)
	if err != nil {
		return err
	}
	defer app.Close()
	source := ""
	for i, a := range os.Args {
		if a == "--source" && i+1 < len(os.Args) {
			source = os.Args[i+1]
		}
	}
	return tui.Run(ctx, tui.Deps{
		Env: app.Env, Catalog: app.Catalog, Overrides: app.Override, Config: app.Config, LogPath: app.LogPath,
		Mode: mode, Source: source,
		EnableSchedule: func(dest, when string) error {
			return EnableSchedule(ctx, app, dest, when, filepath.Join(app.Env.Home, ".config/systemd/user"))
		},
	})
}
