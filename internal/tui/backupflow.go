package tui

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/gaborkis11/omarchy-backup-restore/internal/backup"
	"github.com/gaborkis11/omarchy-backup-restore/internal/config"
	"github.com/gaborkis11/omarchy-backup-restore/internal/engine"
	"github.com/gaborkis11/omarchy-backup-restore/internal/inventory"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

// ───────────────────────── scan ─────────────────────────

type scanDone struct {
	inv  *inventory.Inventory
	free int64
	err  error
}

func newScanScreen(d config.Destination) Screen {
	var sc *busyScreen
	sc = newBusy(i18n.T("Mentés – felmérés"), i18n.T("A gép felmérése (programok, beállítások, fájlok)…"), nil, nil)
	sc.run = func() tea.Msg { return nil }
	return &scanWrap{d: d, busy: sc}
}

// scanWrap runs the scan with access to the App.
type scanWrap struct {
	d    config.Destination
	busy *busyScreen
}

func (s *scanWrap) Title() string { return s.busy.Title() }
func (s *scanWrap) Help() string  { return s.busy.Help() }
func (s *scanWrap) Init(a *App) tea.Cmd {
	d := s.d
	if a.d.Config.LastDestination != d.Name {
		a.d.Config.LastDestination = d.Name
		_ = a.d.Config.Save()
	}
	s.busy.run = func() tea.Msg {
		prev := backup.LoadLast(a.d.Env, d.Name)
		sc := &inventory.Scanner{Env: a.d.Env, Catalog: a.d.Catalog, Override: a.d.Overrides, Prev: prev}
		inv, err := sc.Scan(a.ctx)
		var free int64
		if !d.Remote() {
			p := d.Path
			if d.UUID != "" {
				if mp, err := engine.MountPointByUUID(a.ctx, a.d.Env.Runner, d.UUID); err == nil && mp != "" {
					p = mp
				}
			}
			free = engine.FreeBytes(a.ctx, a.d.Env.Runner, p)
		}
		return scanDone{inv, free, err}
	}
	s.busy.done = func(m tea.Msg) tea.Cmd {
		r := m.(scanDone)
		if r.err != nil {
			return replace(errorScreen(a, i18n.T("Felmérés"), r.err))
		}
		return replace(newBackupSelect(d, r.inv, r.free))
	}
	return s.busy.Init(a)
}
func (s *scanWrap) Update(a *App, msg tea.Msg) tea.Cmd { return s.busy.Update(a, msg) }
func (s *scanWrap) View(a *App, w, h int) string       { return s.busy.View(a, w, h) }

// ───────────────────────── selection ─────────────────────────

type backupSelect struct {
	d    config.Destination
	inv  *inventory.Inventory
	free int64
	tree *selTree
	note string
}

func newBackupSelect(d config.Destination, inv *inventory.Inventory, free int64) *backupSelect {
	s := &backupSelect{d: d, inv: inv, free: free, tree: newSelTree(true)}
	for _, it := range inv.Items {
		it := it
		s.tree.add(&selItem{id: it.ID, label: it.Label, detail: it.Detail, cat: it.Category, kind: it.Kind,
			size: it.Size, sel: &it.Selected, enabled: true, status: inv.Status[it.ID], hardware: it.Hardware,
			secret: it.Secret, requires: it.Requires})
	}
	s.tree.order()
	s.tree.onToggle = func(si *selItem, on bool) { inv.Select(si.id, on) }
	if len(inv.Removed) > 0 {
		s.note = i18n.T("Az előző mentés óta eltűnt: ") + strings.Join(inv.Removed, ", ")
	}
	return s
}

func (s *backupSelect) Init(a *App) tea.Cmd { return nil }
func (s *backupSelect) Title() string       { return i18n.T("Mentés – mit mentsünk?") }
func (s *backupSelect) Help() string {
	h := i18n.T("Space kijelöl  •  Enter/→ kinyit  •  a mind  •  d részletek  •  n tovább  •  Esc másik célhely")
	if r := s.tree.current(); r.item != nil && r.item.cat == "other" {
		h = i18n.T("m mindig mentsd  •  s soha  •  c gyorsítótár  •  ") + h
	}
	return h
}

func (s *backupSelect) Update(a *App, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc", "q":
			return pop()
		case "n", "ctrl+n", "f":
			return s.proceed(a)
		case "m", "s", "c":
			r := s.tree.current()
			if r.item != nil && r.item.cat == "other" {
				rel := strings.TrimPrefix(r.item.id, "other:")
				action := map[string]string{"m": "always", "s": "never", "c": "cache"}[k.String()]
				a.d.Overrides.Set(rel, action)
				_ = a.d.Overrides.Save(a.d.Env.ConfigDir)
				*r.item.sel = action == "always"
				r.item.reason = map[string]string{"always": i18n.T("Megjelölve: mindig mentsd."), "never": i18n.T("Megjelölve: soha ne mentsd."), "cache": i18n.T("Megjelölve: gyorsítótár, nem kell menteni.")}[action]
				return nil
			}
		}
	}
	s.tree.update(msg)
	return nil
}

func (s *backupSelect) View(a *App, w, h int) string {
	th := a.th
	n, size := s.tree.totalSelected()
	sum := fmt.Sprintf(i18n.T("Kiválasztva: %d tétel, kb. %s"), n, inventory.HumanSize(size))
	if s.free > 0 {
		sum += fmt.Sprintf(i18n.T("  •  szabad hely a célon: %s"), inventory.HumanSize(s.free))
		if size > s.free {
			sum += th.errS.Render(i18n.T("  NEM FÉR EL!"))
		}
	}
	head := th.text.Render(sum) + th.mutedS.Render(i18n.T("  •  cél: ")+s.d.Name)
	extra := 1
	if s.note != "" {
		head += "\n" + th.warnS.Render(truncate(s.note, w))
		extra++
	}
	return head + "\n" + s.tree.view(a, w, h-extra)
}

func (s *backupSelect) proceed(a *App) tea.Cmd {
	for id, req := range s.inv.Missing() {
		for _, r := range req {
			s.inv.Select(r, true)
		}
		_ = id
	}
	if n, _ := s.tree.totalSelected(); n == 0 {
		return push(newMessage(i18n.T("Nincs kijelölve semmi"), []string{a.th.text.Render(i18n.T("Jelölj ki legalább egy tételt."))}))
	}
	return push(newPreBackup(s.d, s.inv))
}

// ───────────────────────── pre-backup checks ─────────────────────────

type preBackup struct {
	d          config.Destination
	inv        *inventory.Inventory
	running    []string
	consistent bool
	list       optionList
	checked    bool
}

type runningMsg []string

func newPreBackup(d config.Destination, inv *inventory.Inventory) *preBackup {
	return &preBackup{d: d, inv: inv}
}

func (s *preBackup) Title() string { return i18n.T("Mentés – ellenőrzés") }
func (s *preBackup) Help() string  { return i18n.T("↑/↓  •  Enter  •  Esc vissza") }
func (s *preBackup) Init(a *App) tea.Cmd {
	s.consistent = a.d.Config.ConsistentSnapshot
	inv := s.inv
	return func() tea.Msg {
		return runningMsg(inv.RunningApps(func(p string) bool { return exec.Command("pgrep", "-x", p).Run() == nil }))
	}
}

func (s *preBackup) Update(a *App, msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case runningMsg:
		s.running, s.checked = m, true
		if len(m) == 0 {
			return s.afterChecks(a)
		}
		s.list = optionList{opts: []option{
			{label: i18n.T("Bezártam őket, ellenőrizd újra"), run: func() tea.Cmd { return s.Init(a) }},
			{label: i18n.T("Konzisztens mentés pillanatképből"), desc: i18n.T("A /home-ról egy pillanatra „fotót” készítünk (btrfs), és abból mentünk, így futó programok mellett is ép lesz. Rendszergazdai jog kell."),
				run: func() tea.Cmd { s.consistent = true; return s.afterChecks(a) }},
			{label: i18n.T("Folytatás így is"), desc: i18n.T("A futó programok adatbázisai (pl. a Chrome sütijei) félig írt állapotban kerülhetnek a mentésbe."),
				run: func() tea.Cmd { return s.afterChecks(a) }},
			{label: i18n.T("Vissza"), run: pop},
		}}
		return nil
	case sudoResult:
		if !m.ok {
			for _, it := range s.inv.Items {
				if it.NeedsRoot {
					it.Selected = false
				}
			}
			s.consistent = false
		}
		return replace(newPasswordScreen(s.d, purposeBackup, func(loc *engine.Location, exists bool) tea.Cmd {
			return push(newBackupRun(s.d, s.inv, loc, s.consistent))
		}))
	case tea.KeyPressMsg:
		if m.String() == "esc" {
			return pop()
		}
	}
	cmd, _ := s.list.update(msg)
	return cmd
}

type sudoResult struct{ ok bool }

func (s *preBackup) afterChecks(a *App) tea.Cmd {
	if s.inv.NeedsRoot() || s.consistent {
		return a.ensureSudo(func(ok bool) tea.Msg { return sudoResult{ok} })
	}
	return func() tea.Msg { return sudoResult{true} }
}

func (s *preBackup) View(a *App, w, h int) string {
	th := a.th
	if !s.checked {
		return "\n  " + th.accentS.Render(i18n.T("Futó programok ellenőrzése…"))
	}
	if len(s.running) == 0 {
		return "\n  " + th.accentS.Render(i18n.T("Rendszergazdai jog kérése…"))
	}
	out := "\n" + th.warnS.Render(i18n.T("  Ezek a programok most futnak: ")+strings.Join(s.running, ", ")) + "\n"
	out += th.mutedS.Render(i18n.T("  A legjobb, ha mentés előtt bezárod őket.")) + "\n\n"
	return out + s.list.view(a, w)
}

// ───────────────────────── backup progress ─────────────────────────

type bEvent struct{ e backup.Event }
type bDone struct {
	res *backup.Result
	err error
}

type backupRun struct {
	d          config.Destination
	inv        *inventory.Inventory
	loc        *engine.Location
	consistent bool
	ch         chan tea.Msg
	log        []string
	warnings   []string
	prog       *engine.Progress
	bar        progress.Model
	sp         spinner.Model
	phase      string
	started    time.Time
	finished   bool
}

func newBackupRun(d config.Destination, inv *inventory.Inventory, loc *engine.Location, consistent bool) *backupRun {
	return &backupRun{d: d, inv: inv, loc: loc, consistent: consistent, ch: make(chan tea.Msg, 64)}
}

func listen(ch chan tea.Msg) tea.Cmd { return func() tea.Msg { return <-ch } }

func (s *backupRun) Title() string { return i18n.T("Mentés folyamatban") }
func (s *backupRun) Help() string {
	if s.finished {
		return ""
	}
	return i18n.T("A mentés fut – ne húzd ki a lemezt.  •  Ctrl+C megszakítás")
}

func (s *backupRun) Init(a *App) tea.Cmd {
	if !s.started.IsZero() {
		return nil
	}
	s.started = time.Now()
	s.bar = progress.New(progress.WithWidth(50), progress.WithColors(a.th.accent, a.th.yellow), progress.WithoutPercentage())
	s.sp = spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(a.th.accentS))
	a.busy = true
	go func() {
		res, err := backup.Run(a.ctx, a.d.Env, s.inv, s.loc, backup.Options{
			Consistent: s.consistent, DestName: s.d.Name, Retention: retention(a.d.Config.Retention),
		}, func(e backup.Event) { s.ch <- bEvent{e} })
		s.ch <- bDone{res, err}
	}()
	return tea.Batch(listen(s.ch), s.sp.Tick)
}

func retention(r config.Retention) []string {
	var out []string
	add := func(flag string, n int) {
		if n > 0 {
			out = append(out, flag, fmt.Sprint(n))
		}
	}
	add("--keep-last", r.KeepLast)
	add("--keep-daily", r.KeepDaily)
	add("--keep-weekly", r.KeepWeekly)
	add("--keep-monthly", r.KeepMonthly)
	return out
}

func (s *backupRun) Update(a *App, msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case bEvent:
		e := m.e
		switch {
		case e.Warning != "":
			s.warnings = append(s.warnings, e.Warning)
		case e.Progress != nil:
			s.prog = e.Progress
		case e.Message != "":
			s.phase = e.Phase
			s.log = append(s.log, e.Message)
		}
		return listen(s.ch)
	case bDone:
		a.busy = false
		s.finished = true
		if m.err != nil {
			return replace(errorScreen(a, i18n.T("A mentés nem sikerült"), m.err, i18n.T("Napló: ")+a.d.LogPath))
		}
		return replace(newBackupSummary(s.d, m.res, s.loc))
	case spinner.TickMsg:
		var cmd tea.Cmd
		s.sp, cmd = s.sp.Update(m)
		return cmd
	}
	return nil
}

func (s *backupRun) View(a *App, w, h int) string {
	th := a.th
	var b strings.Builder
	b.WriteString("\n")
	for i, l := range s.log {
		mark := th.okS.Render("  ✓ ")
		if i == len(s.log)-1 {
			mark = "  " + s.sp.View() + " "
		}
		b.WriteString(mark + th.text.Render(l) + "\n")
	}
	if s.phase == "data" && s.prog != nil {
		p := s.prog
		b.WriteString("\n    " + s.bar.ViewAs(p.Percent) + fmt.Sprintf("  %3.0f%%", p.Percent*100) + "\n")
		info := fmt.Sprintf(i18n.T("    %d / %d fájl  •  %s / %s"), p.FilesDone, p.TotalFiles, inventory.HumanSize(p.BytesDone), inventory.HumanSize(p.TotalBytes))
		if p.SecondsLeft > 0 {
			info += fmt.Sprintf(i18n.T("  •  kb. %s van hátra"), (time.Duration(p.SecondsLeft) * time.Second).Round(time.Second))
		}
		b.WriteString(th.mutedS.Render(info) + "\n")
		if len(p.CurrentFiles) > 0 {
			b.WriteString(th.dimS.Render("    "+truncate(p.CurrentFiles[0], w-6)) + "\n")
		}
	}
	if len(s.warnings) > 0 {
		b.WriteString("\n" + th.warnS.Render(fmt.Sprintf(i18n.T("  %d figyelmeztetés"), len(s.warnings))) + "\n")
		for _, wn := range s.warnings[max(0, len(s.warnings)-3):] {
			b.WriteString(th.mutedS.Render("    "+truncate(wn, w-6)) + "\n")
		}
	}
	b.WriteString("\n" + th.dimS.Render(fmt.Sprintf(i18n.T("  Eltelt idő: %s"), time.Since(s.started).Round(time.Second))))
	return b.String()
}

// ───────────────────────── summary ─────────────────────────

func newBackupSummary(d config.Destination, res *backup.Result, loc *engine.Location) *messageScreen {
	m := res.Manifest
	lines := []string{
		i18n.T("  ✓ A mentés elkészült: ") + d.Name,
		"",
		fmt.Sprintf(i18n.T("    Feldolgozva:   %s (%d fájl új, %d módosult, %d változatlan)"), inventory.HumanSize(m.Stats.BytesProcessed), m.Stats.FilesNew, m.Stats.FilesChanged, m.Stats.FilesUnchanged),
		fmt.Sprintf(i18n.T("    Új adat a mentésben: %s"), inventory.HumanSize(m.Stats.BytesAdded)),
		fmt.Sprintf(i18n.T("    Időtartam:     %s"), res.Duration.Round(time.Second)),
		fmt.Sprintf(i18n.T("    Hely:          %s"), loc.Base),
		"",
		i18n.T("    Friss gépen a visszaállítás: bash ") + loc.Base + "/kit/restore.sh",
	}
	if len(res.Warnings) > 0 {
		lines = append(lines, "", fmt.Sprintf(i18n.T("  ! %d figyelmeztetés:"), len(res.Warnings)))
		for _, w := range res.Warnings[:min(len(res.Warnings), 6)] {
			lines = append(lines, "    "+w)
		}
	}
	return &messageScreen{title: i18n.T("Mentés kész"), body: lines, list: optionList{opts: []option{
		{label: i18n.T("Vissza a menübe"), run: goHome},
		{label: i18n.T("Kilépés"), run: quit},
	}}}
}
