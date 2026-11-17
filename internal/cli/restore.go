package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gaborkis11/omarchy-backup-restore/internal/engine"
	"github.com/gaborkis11/omarchy-backup-restore/internal/inventory"
	"github.com/gaborkis11/omarchy-backup-restore/internal/restore"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

func cmdRestore(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	var d destFlags
	d.register(fs)
	source := fs.String("source", "", i18n.T("a mentés helye (omarchy-backup mappa vagy a lemez)"))
	snap := fs.String("snapshot", "latest", i18n.T("melyik mentés (azonosító vagy latest)"))
	sel := fs.String("select", "defaults", i18n.T("defaults | all | vesszővel elválasztott azonosítók (prefix*)"))
	without := fs.String("without", "", i18n.T("kihagyandó tételek (vesszővel, prefix*)"))
	conflict := fs.String("conflict", "mine", i18n.T("ütközésnél: mine (a tiéd) | new (új alap) | merged"))
	yes := fs.Bool("yes", false, i18n.T("nincs megerősítés"))
	noUpdate := fs.Bool("no-update", false, i18n.T("ne frissítse a rendszert a csomagok előtt"))
	noSnap := fs.Bool("no-snapshot", false, i18n.T("ne készítsen rendszer-pillanatképet"))
	migr := fs.String("migrations", "config", i18n.T("config (a beállításokat érintők) | all | none"))
	recipes := fs.Bool("recipes", false, i18n.T("a git-projektek telepítőinek (pl. install.sh) futtatása"))
	list := fs.Bool("list", false, i18n.T("csak kiírja, mi állítható vissza"))
	dry := fs.Bool("dry-run", false, i18n.T("próbafutás: nem változtat semmit"))
	home := fs.String("home", "", i18n.T("visszaállítás más home mappába (teszteléshez)"))
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *source != "" {
		d.path = *source
	}
	app, err := newApp(*home, *dry)
	if err != nil {
		return err
	}
	defer app.Close()
	if !app.Env.IsOmarchy() && os.Getenv("OBR_ALLOW_NON_OMARCHY") == "" {
		return fmt_errNotOmarchy()
	}
	loc, err := app.open(ctx, &d, false)
	if err != nil {
		return err
	}
	backups, err := restore.ListBackups(ctx, loc)
	if err != nil {
		return err
	}
	if len(backups) == 0 {
		return fmt.Errorf("%s", i18n.T("ezen a helyen nincs mentés"))
	}
	b := backups[0]
	if *snap != "latest" {
		found := false
		for _, x := range backups {
			if strings.HasPrefix(x.Meta.ID, *snap) || x.RunID == *snap {
				b, found = x, true
				break
			}
		}
		if !found {
			return fmt.Errorf(i18n.T("nincs ilyen mentés: %s"), *snap)
		}
	}
	fmt.Fprintf(os.Stderr, i18n.T("Mentés: %s (%s)\n"), b.Time.Local().Format("2006-01-02 15:04"), b.Host)
	src, err := restore.Load(ctx, app.Env, loc, b)
	if err != nil {
		return err
	}
	cands := restore.Candidates(ctx, app.Env, src)
	applySelect(cands, *sel, *without)
	if *list {
		printCandidates(cands)
		return nil
	}
	selected := map[string]bool{}
	for _, c := range cands {
		if c.Selected && c.Available {
			selected[c.ID] = true
		}
	}
	if len(selected) == 0 {
		return fmt.Errorf("%s", i18n.T("nincs kiválasztott tétel"))
	}
	printCandidates(cands)
	if !*yes && !confirm(fmt.Sprintf(i18n.T("Visszaállítsuk a kiválasztott %d tételt erre a gépre?"), len(selected)), false) {
		return fmt.Errorf("%s", i18n.T("megszakítva"))
	}
	stop, err := ensureSudo(ctx, !*yes || os.Getenv("SUDO_ASKPASS") != "")
	if err != nil {
		fmt.Fprintf(os.Stderr, i18n.T("Figyelem: rendszergazdai jog nélkül a programtelepítés és a rendszerbeállítások nem fognak sikerülni (%v).\n"), err)
	} else {
		defer stop()
	}
	var migrations []string
	for _, m := range restore.PendingMigrations(app.Env, src) {
		if *migr == "all" || *migr == "config" && m.TouchesConfig {
			migrations = append(migrations, m.Name)
		}
	}
	rec := map[string]bool{}
	if *recipes {
		for _, g := range src.Manifest.GitProjects {
			rec[g.ItemID] = true
		}
	}
	res := restore.KeepMine
	switch *conflict {
	case "new":
		res = restore.TakeNew
	case "merged":
		res = restore.UseMerged
	}
	opts := restore.Options{
		Selected: selected, UpdateSystem: !*noUpdate, Snapshot: !*noSnap, Migrations: migrations,
		Recipes: rec, DryRun: *dry,
		Resolver: func(c restore.Conflict) restore.Resolution {
			fmt.Fprintf(os.Stderr, i18n.T("\nÜtközés: ~/%s – %s\n"), c.Path, c.Reason)
			return res
		},
		Interactive: func(c *exec.Cmd) error {
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			return c.Run()
		},
	}
	rep, err := restore.Run(ctx, app.Env, src, opts, func(e restore.Event) {
		switch {
		case e.Progress != nil:
			p := e.Progress
			fmt.Fprintf(os.Stderr, i18n.T("\r\033[K  %5.1f%%  %d/%d fájl  %s"), p.Percent*100, p.FilesDone, p.TotalFiles, inventory.HumanSize(p.BytesDone))
		case e.Status == "start":
			fmt.Fprintf(os.Stderr, "\r\033[K▸ %s\n", e.Message)
		case e.Status == "warn":
			fmt.Fprintf(os.Stderr, "\r\033[K  ! %s\n", e.Message)
		case e.Status == "info":
			fmt.Fprintf(os.Stderr, "\r\033[K  ✓ %s\n", e.Message)
		case e.Status == "fail":
			fmt.Fprintf(os.Stderr, "\r\033[K  ✗ %s\n", e.Message)
		}
	})
	if err != nil {
		return err
	}
	printReport(rep, app.LogPath)
	return nil
}

func applySelect(cands []*restore.Candidate, sel, without string) {
	switch sel {
	case "defaults", "":
	case "all":
		for _, c := range cands {
			c.Selected = c.Available
		}
	default:
		want := map[string]bool{}
		for _, s := range strings.Split(sel, ",") {
			want[strings.TrimSpace(s)] = true
		}
		for _, c := range cands {
			c.Selected = c.Available && (want[c.ID] || matchPrefix(c.ID, want))
		}
	}
	if without != "" {
		drop := map[string]bool{}
		for _, s := range strings.Split(without, ",") {
			drop[strings.TrimSpace(s)] = true
		}
		for _, c := range cands {
			if drop[c.ID] || matchPrefix(c.ID, drop) {
				c.Selected = false
			}
		}
	}
}

func printCandidates(cands []*restore.Candidate) {
	byCat := map[string][]*restore.Candidate{}
	for _, c := range cands {
		byCat[c.Category] = append(byCat[c.Category], c)
	}
	for _, cat := range inventory.Categories {
		list := byCat[cat]
		if len(list) == 0 {
			continue
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Label < list[j].Label })
		fmt.Printf("\n== %s\n", categoryTitle(cat))
		for _, c := range list {
			mark := "[ ]"
			if c.Selected {
				mark = "[x]"
			}
			if !c.Available {
				mark = " - "
			}
			extra := ""
			if c.Reason != "" {
				extra = "  (" + strings.TrimSpace(c.Reason) + ")"
			}
			fmt.Printf("  %s %-60s %s%s\n", mark, trunc(c.Label, 60), c.ID, extra)
		}
	}
	fmt.Println()
}

func printReport(rep *restore.Report, logPath string) {
	fmt.Println(i18n.T("\n══ Visszaállítás kész ══"))
	for _, d := range rep.Done {
		fmt.Println("  ✓ " + d)
	}
	if len(rep.Warnings) > 0 {
		fmt.Println(i18n.T("\nFigyelmeztetések:"))
		for _, w := range rep.Warnings {
			fmt.Println("  ! " + w)
		}
	}
	if len(rep.Manual) > 0 {
		fmt.Println(i18n.T("\nTeendőid:"))
		for _, m := range rep.Manual {
			fmt.Println("  → " + m)
		}
	}
	fmt.Printf(i18n.T("\nA felülírt fájlok másolata: %s\n(visszavonás: obr undo %s)\nNapló: %s\n"), rep.UndoDir, rep.WorkDir, logPath)
}

func cmdUndo(ctx context.Context, args []string) error {
	app, err := newApp("", false)
	if err != nil {
		return err
	}
	defer app.Close()
	dir := ""
	if len(args) > 0 {
		dir = args[0]
	} else {
		matches, _ := filepath.Glob(filepath.Join(app.Env.StateDir, "restore-*"))
		sort.Strings(matches)
		if len(matches) == 0 {
			return fmt.Errorf("%s", i18n.T("nincs visszavonható visszaállítás"))
		}
		dir = matches[len(matches)-1]
	}
	if !confirm(fmt.Sprintf(i18n.T("Visszavonod ezt a visszaállítást: %s?"), dir), false) {
		return fmt.Errorf("%s", i18n.T("megszakítva"))
	}
	n, err := restore.Undo(app.Env, dir)
	if err != nil {
		return err
	}
	fmt.Printf(i18n.T("%d elem visszatéve. (A telepített programok és a rendszerbeállítások maradnak.)\n"), n)
	return nil
}

var _ = engine.BaseDir
