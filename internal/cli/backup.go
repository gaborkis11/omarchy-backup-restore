package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gaborkis11/omarchy-backup-restore/internal/backup"
	"github.com/gaborkis11/omarchy-backup-restore/internal/engine"
	"github.com/gaborkis11/omarchy-backup-restore/internal/inventory"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

func cmdBackup(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	var d destFlags
	d.register(fs)
	dry := fs.Bool("dry-run", false, i18n.T("csak kiírja, mi kerülne a mentésbe"))
	consistent := fs.Bool("consistent", false, i18n.T("mentés btrfs pillanatképből (sudo kell)"))
	skipCheck := fs.Bool("skip-check", false, i18n.T("a mentés utáni ellenőrzés kihagyása"))
	only := fs.String("only", "", i18n.T("csak ezek a tételek (vesszővel elválasztott azonosítók)"))
	without := fs.String("without", "", i18n.T("ezek a tételek kimaradnak (vesszővel)"))
	home := fs.String("home", "", i18n.T("más home mappa mentése (teszteléshez)"))
	if err := fs.Parse(args); err != nil {
		return err
	}
	app, err := newApp(*home, *dry)
	if err != nil {
		return err
	}
	defer app.Close()
	if !app.Env.IsOmarchy() && os.Getenv("OBR_ALLOW_NON_OMARCHY") == "" {
		return fmt_errNotOmarchy()
	}
	dest, err := app.destination(&d)
	if err != nil {
		return err
	}
	prev := backup.LoadLast(app.Env, dest.Name)
	fmt.Fprintln(os.Stderr, i18n.T("A gép felmérése…"))
	inv, err := app.Scan(ctx, prev)
	if err != nil {
		return err
	}
	applyOnlyWithout(inv, *only, *without)
	for _, m := range inv.Missing() {
		for _, id := range m {
			inv.Select(id, true)
		}
	}
	paths, excludes := backup.Plan(app.Env, inv)
	if *dry {
		fmt.Printf(i18n.T("Mentendő utak (%d):\n"), len(paths))
		for _, p := range paths {
			fmt.Println("  ~/" + p)
		}
		fmt.Printf(i18n.T("\nKizárások (%d):\n"), len(excludes))
		for _, e := range excludes {
			fmt.Println("  " + e)
		}
		fmt.Printf(i18n.T("\nBecsült méret: %s\n"), inventory.HumanSize(inv.SelectedSize()))
		return nil
	}

	interactive := !d.unattended
	if running := inv.RunningApps(isRunning(app.Env.Runner)); len(running) > 0 && interactive && !*consistent {
		fmt.Fprintf(os.Stderr, i18n.T("Figyelem: ezek a programok futnak: %s.\nFutás közben az adatbázisaik (pl. Chrome) félig írt állapotban kerülhetnek a mentésbe.\n"), strings.Join(running, ", "))
		if !confirm(i18n.T("Folytatod így is?"), true) {
			return fmt.Errorf("%s", i18n.T("megszakítva"))
		}
	}
	if inv.NeedsRoot() || *consistent || app.Config.ConsistentSnapshot {
		stop, err := ensureSudo(ctx, interactive)
		if err != nil {
			fmt.Fprintf(os.Stderr, i18n.T("Rendszergazdai jog nélkül a rendszerszintű tételek kimaradnak (%v).\n"), err)
			for _, it := range inv.Items {
				if it.NeedsRoot {
					it.Selected = false
				}
			}
			*consistent = false
		} else {
			defer stop()
		}
	}
	loc, err := app.open(ctx, &d, true)
	if err != nil {
		return err
	}
	start := time.Now()
	lastLine := ""
	res, err := backup.Run(ctx, app.Env, inv, loc, backup.Options{
		Consistent: *consistent || app.Config.ConsistentSnapshot, SkipCheck: *skipCheck,
		DestName: dest.Name, Retention: retentionFlags(app.Config.Retention),
	}, func(e backup.Event) {
		if interactive {
			printEvent(e, &lastLine)
		} else if e.Message != "" {
			app.Env.Log.Print(e.Message)
		}
	})
	if interactive {
		fmt.Fprintln(os.Stderr)
	}
	if err != nil {
		if d.unattended {
			notify(i18n.T("Omarchy mentés: HIBA"), err.Error())
		}
		return err
	}
	msg := fmt.Sprintf(i18n.T("Mentés kész (%s): %s feldolgozva, %s új adat, %d új / %d módosult fájl, %s."),
		dest.Name, inventory.HumanSize(res.Manifest.Stats.BytesProcessed), inventory.HumanSize(res.Manifest.Stats.BytesAdded),
		res.Manifest.Stats.FilesNew, res.Manifest.Stats.FilesChanged, time.Since(start).Round(time.Second))
	fmt.Println(msg)
	for _, w := range res.Warnings {
		fmt.Println("  ! " + w)
	}
	if d.unattended {
		body := msg
		if len(res.Warnings) > 0 {
			body += fmt.Sprintf(i18n.T(" %d figyelmeztetés (napló: %s)"), len(res.Warnings), app.LogPath)
		}
		notify(i18n.T("Omarchy mentés kész"), body)
	}
	return nil
}

func fmt_errNotOmarchy() error {
	return fmt.Errorf("%s", i18n.T("ez a gép nem Omarchy; az obr csak Omarchy-n működik"))
}

func applyOnlyWithout(inv *inventory.Inventory, only, without string) {
	if only != "" {
		want := map[string]bool{}
		for _, id := range strings.Split(only, ",") {
			want[strings.TrimSpace(id)] = true
		}
		for _, it := range inv.Items {
			it.Selected = want[it.ID] || matchPrefix(it.ID, want)
		}
	}
	if without != "" {
		for _, id := range strings.Split(without, ",") {
			id = strings.TrimSpace(id)
			for _, it := range inv.Items {
				if it.ID == id || strings.HasSuffix(id, "*") && strings.HasPrefix(it.ID, strings.TrimSuffix(id, "*")) {
					it.Selected = false
				}
			}
		}
	}
}

func matchPrefix(id string, want map[string]bool) bool {
	for w := range want {
		if strings.HasSuffix(w, "*") && strings.HasPrefix(id, strings.TrimSuffix(w, "*")) {
			return true
		}
	}
	return false
}

func printEvent(e backup.Event, last *string) {
	switch {
	case e.Warning != "":
		fmt.Fprintf(os.Stderr, "\r\033[K  ! %s\n", e.Warning)
	case e.Progress != nil:
		p := e.Progress
		line := fmt.Sprintf(i18n.T("\r\033[K  %5.1f%%  %d/%d fájl  %s/%s"), p.Percent*100, p.FilesDone, p.TotalFiles,
			inventory.HumanSize(p.BytesDone), inventory.HumanSize(p.TotalBytes))
		if p.SecondsLeft > 0 {
			line += fmt.Sprintf(i18n.T("  ~%s van hátra"), (time.Duration(p.SecondsLeft) * time.Second).String())
		}
		fmt.Fprint(os.Stderr, line)
	case e.Message != "" && e.Message != *last:
		*last = e.Message
		fmt.Fprintf(os.Stderr, "\r\033[K%s\n", e.Message)
	}
}

func cmdList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	var d destFlags
	d.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	app, err := newApp("", false)
	if err != nil {
		return err
	}
	defer app.Close()
	loc, err := app.open(ctx, &d, false)
	if err != nil {
		return err
	}
	metas, err := loc.Repo.Snapshots(ctx, "obr-meta")
	if err != nil {
		return err
	}
	if len(metas) == 0 {
		fmt.Println(i18n.T("Nincs még mentés ezen a helyen."))
		return nil
	}
	datas, _ := loc.Repo.Snapshots(ctx, "obr-data")
	size := map[string]int64{}
	for _, s := range datas {
		if s.Summary != nil {
			size[s.Tag("run-")] = s.Summary.TotalBytesProcessed
		}
	}
	fmt.Printf("%-10s  %-19s  %-12s  %s\n", i18n.T("AZONOSÍTÓ"), i18n.T("IDŐPONT"), i18n.T("GÉP"), i18n.T("MÉRET"))
	for _, s := range metas {
		fmt.Printf("%-10s  %-19s  %-12s  %s\n", s.ShortID, s.Time.Local().Format("2006-01-02 15:04:05"), s.Hostname, inventory.HumanSize(size[s.Tag("run-")]))
	}
	if total, err := loc.Repo.Stats(ctx); err == nil && total > 0 {
		fmt.Printf(i18n.T("\nA tároló mérete a lemezen: %s\n"), inventory.HumanSize(total))
	}
	return nil
}

func cmdCheck(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	var d destFlags
	d.register(fs)
	read := fs.String("read-data", "", i18n.T("az adatok egy részének beolvasása is, pl. 5%"))
	if err := fs.Parse(args); err != nil {
		return err
	}
	app, err := newApp("", false)
	if err != nil {
		return err
	}
	defer app.Close()
	loc, err := app.open(ctx, &d, false)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, i18n.T("Ellenőrzés…"))
	if err := loc.Repo.Check(ctx, *read); err != nil {
		return fmt.Errorf(i18n.T("az ellenőrzés hibát talált: %w"), err)
	}
	fmt.Println(i18n.T("A mentés ép, nincs hiba."))
	return nil
}

func cmdPrune(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	var d destFlags
	d.register(fs)
	last := fs.Int("keep-last", 0, i18n.T("utolsó N mentés"))
	daily := fs.Int("keep-daily", 0, "napi")
	weekly := fs.Int("keep-weekly", 0, "heti")
	monthly := fs.Int("keep-monthly", 0, "havi")
	if err := fs.Parse(args); err != nil {
		return err
	}
	app, err := newApp("", false)
	if err != nil {
		return err
	}
	defer app.Close()
	pol := app.Config.Retention
	if *last+*daily+*weekly+*monthly > 0 {
		pol.KeepLast, pol.KeepDaily, pol.KeepWeekly, pol.KeepMonthly = *last, *daily, *weekly, *monthly
	}
	flags := retentionFlags(pol)
	if len(flags) == 0 {
		return fmt.Errorf("%s", i18n.T("nincs megőrzési szabály (pl. --keep-daily 7 --keep-weekly 4 --keep-monthly 12)"))
	}
	loc, err := app.open(ctx, &d, false)
	if err != nil {
		return err
	}
	n, err := backup.Prune(ctx, loc.Repo, flags)
	if err != nil {
		return err
	}
	fmt.Printf(i18n.T("%d régi mentés törölve.\n"), n)
	return nil
}

func cmdKit(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("kit", flag.ContinueOnError)
	var d destFlags
	d.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	app, err := newApp("", false)
	if err != nil {
		return err
	}
	defer app.Close()
	dest, err := app.destination(&d)
	if err != nil {
		return err
	}
	cr := engine.Credentials{RepoPassword: "x"}
	if dest.Remote() {
		cr.NASPassword, _ = secretFrom(d.nasPassFile, "OBR_NAS_PASSWORD")
		if cr.NASPassword == "" {
			if cr.NASPassword, err = askPassword(i18n.T("A NAS jelszava: "), false); err != nil {
				return err
			}
		}
	}
	loc, err := engine.Resolve(ctx, app.Env.Runner, dest, cr, engine.FindTool(app.Env.BinDir, "restic"), engine.FindTool(app.Env.BinDir, "rclone"))
	if err != nil {
		return err
	}
	if err := backup.WriteKit(ctx, app.Env, loc); err != nil {
		return err
	}
	fmt.Println(i18n.T("A visszaállító készlet frissítve:"), loc.Base+"/kit")
	return nil
}
