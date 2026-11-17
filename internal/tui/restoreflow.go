package tui

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/gaborkis11/omarchy-backup-restore/internal/backup"
	"github.com/gaborkis11/omarchy-backup-restore/internal/config"
	"github.com/gaborkis11/omarchy-backup-restore/internal/engine"
	"github.com/gaborkis11/omarchy-backup-restore/internal/inventory"
	"github.com/gaborkis11/omarchy-backup-restore/internal/restore"
	"github.com/gaborkis11/omarchy-backup-restore/internal/sys"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

// newRestoreFromPath starts a restore from a local omarchy-backup folder (kit/restore.sh).
func newRestoreFromPath(p string) Screen {
	p = filepath.Clean(p)
	if filepath.Base(p) == engine.BaseDir {
		p = filepath.Dir(p)
	}
	d := config.Destination{Name: filepath.Base(p), Type: config.Local, Path: p}
	return newPasswordScreen(d, purposeRestore, func(loc *engine.Location, exists bool) tea.Cmd {
		return push(newBackupList(loc, true))
	})
}

// ───────────────────────── backup list ─────────────────────────

type backupsMsg struct {
	list  []restore.Backup
	sizes map[string]int64
	err   error
}

type backupList struct {
	loc        *engine.Location
	forRestore bool
	list       []restore.Backup
	sizes      map[string]int64
	cur        int
	loaded     bool
	status     string
	busy       bool
}

func newBackupList(loc *engine.Location, forRestore bool) *backupList {
	return &backupList{loc: loc, forRestore: forRestore}
}

func (s *backupList) Title() string {
	if s.forRestore {
		return i18n.T("Visszaállítás – melyik mentésből?")
	}
	return i18n.T("Mentések – ") + s.loc.Dest.Name
}

func (s *backupList) Help() string {
	if s.forRestore {
		return i18n.T("↑/↓  •  Enter ezt állítom vissza  •  Esc vissza")
	}
	return i18n.T("↑/↓  •  Enter visszaállítás  •  c ellenőrzés  •  C teljes ellenőrzés  •  u kit frissítése  •  t régi törlése  •  x törlés  •  Esc")
}

func (s *backupList) Init(a *App) tea.Cmd {
	if s.loaded {
		return nil
	}
	loc := s.loc
	return func() tea.Msg {
		l, err := restore.ListBackups(a.ctx, loc)
		sizes := map[string]int64{}
		if datas, err := loc.Repo.Snapshots(a.ctx, "obr-data"); err == nil {
			for _, d := range datas {
				if d.Summary != nil {
					sizes[d.Tag("run-")] = d.Summary.TotalBytesProcessed
				}
			}
		}
		return backupsMsg{l, sizes, err}
	}
}

type actionDone struct {
	msg    string
	err    error
	reload bool
}

func (s *backupList) Update(a *App, msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case backupsMsg:
		s.loaded = true
		if m.err != nil {
			return replace(errorScreen(a, i18n.T("Mentések"), m.err))
		}
		s.list, s.sizes = m.list, m.sizes
		if len(s.list) == 0 {
			s.status = i18n.T("Ezen a helyen még nincs mentés.")
		}
		return nil
	case actionDone:
		s.busy = false
		if m.err != nil {
			s.status = a.th.errS.Render(i18n.T("Hiba: ")) + m.err.Error()
		} else {
			s.status = a.th.okS.Render("✓ ") + m.msg
		}
		if m.reload {
			s.loaded = false
			return s.Init(a)
		}
		return nil
	case tea.KeyPressMsg:
		if s.busy {
			return nil
		}
		switch m.String() {
		case "esc", "q":
			return pop()
		case "up", "k":
			if s.cur > 0 {
				s.cur--
			}
		case "down", "j":
			if s.cur < len(s.list)-1 {
				s.cur++
			}
		case "enter":
			if len(s.list) > 0 {
				return push(newRestoreLoad(s.loc, s.list[s.cur]))
			}
		case "c", "C":
			if s.forRestore {
				return nil
			}
			subset := ""
			label := i18n.T("Ellenőrzés (szerkezet)…")
			if m.String() == "C" {
				subset, label = "100%", i18n.T("Teljes ellenőrzés (minden adat beolvasása, sokáig tart)…")
			}
			s.busy, s.status = true, label
			loc := s.loc
			return func() tea.Msg {
				err := loc.Repo.Check(a.ctx, subset)
				return actionDone{msg: i18n.T("A mentés ép, nincs hiba."), err: err}
			}
		case "u":
			if s.forRestore {
				return nil
			}
			s.busy, s.status = true, i18n.T("A visszaállító készlet frissítése…")
			loc := s.loc
			return func() tea.Msg {
				return actionDone{msg: i18n.T("A kit frissítve: ") + loc.Base + "/kit", err: backup.WriteKit(a.ctx, a.d.Env, loc)}
			}
		case "t":
			if s.forRestore {
				return nil
			}
			pol := retention(a.d.Config.Retention)
			if len(pol) == 0 {
				return push(newMessage(i18n.T("Nincs megőrzési szabály"), []string{a.th.text.Render(i18n.T("Állítsd be a Beállítások menüben, meddig őrizzük a régi mentéseket."))}))
			}
			s.busy, s.status = true, i18n.T("Régi mentések törlése…")
			loc := s.loc
			return func() tea.Msg {
				n, err := backup.Prune(a.ctx, loc.Repo, pol)
				return actionDone{msg: fmt.Sprintf(i18n.T("%d régi mentés törölve."), n), err: err, reload: true}
			}
		case "x":
			if s.forRestore || len(s.list) == 0 {
				return nil
			}
			b := s.list[s.cur]
			loc := s.loc
			return push(newMessage(i18n.T("Mentés törlése"), []string{
				a.th.warnS.Render(fmt.Sprintf(i18n.T("  Biztosan törlöd ezt a mentést? %s (%s)"), b.Time.Local().Format("2006-01-02 15:04"), b.Host)),
				a.th.mutedS.Render(i18n.T("  Ez nem vonható vissza.")),
			}, option{label: i18n.T("Mégse"), run: pop}, option{label: i18n.T("Igen, törlöm"), run: func() tea.Cmd {
				s.busy, s.status = true, i18n.T("Törlés…")
				return tea.Sequence(pop(), func() tea.Msg {
					ids := []string{b.Meta.ID}
					if datas, err := loc.Repo.Snapshots(a.ctx, "run-"+b.RunID); err == nil {
						ids = nil
						for _, d := range datas {
							ids = append(ids, d.ID)
						}
					}
					return actionDone{msg: i18n.T("Mentés törölve."), err: loc.Repo.Forget(a.ctx, ids, true), reload: true}
				})
			}}))
		}
	}
	return nil
}

func (s *backupList) View(a *App, w, h int) string {
	th := a.th
	if !s.loaded {
		return "\n  " + th.accentS.Render(i18n.T("Mentések betöltése…"))
	}
	var b strings.Builder
	b.WriteString("\n" + th.mutedS.Render(fmt.Sprintf("  %-20s  %-14s  %10s", i18n.T("Időpont"), i18n.T("Gép"), i18n.T("Méret"))) + "\n")
	for i, x := range s.list {
		line := fmt.Sprintf("  %-20s  %-14s  %10s", x.Time.Local().Format("2006-01-02 15:04"), truncate(x.Host, 14), inventory.HumanSize(s.sizes[x.RunID]))
		if i == 0 {
			line += th.dimS.Render(i18n.T("  (legutóbbi)"))
		}
		if i == s.cur {
			b.WriteString(th.accentS.Render("›") + th.h2.Render(line) + "\n")
		} else {
			b.WriteString(" " + th.text.Render(line) + "\n")
		}
	}
	if s.status != "" {
		b.WriteString("\n  " + s.status + "\n")
	}
	return b.String()
}

// ───────────────────────── load selected backup ─────────────────────────

type loadDone struct {
	src *restore.Source
	err error
}

func newRestoreLoad(loc *engine.Location, b restore.Backup) Screen {
	return &loadWrap{loc: loc, b: b}
}

type loadWrap struct {
	loc  *engine.Location
	b    restore.Backup
	busy *busyScreen
}

func (s *loadWrap) Title() string { return i18n.T("Visszaállítás – betöltés") }
func (s *loadWrap) Help() string  { return "" }
func (s *loadWrap) Init(a *App) tea.Cmd {
	if s.busy != nil {
		return nil
	}
	loc, b := s.loc, s.b
	s.busy = newBusy("", i18n.T("A mentés leírójának betöltése…"), func() tea.Msg {
		src, err := restore.Load(a.ctx, a.d.Env, loc, b)
		return loadDone{src, err}
	}, func(m tea.Msg) tea.Cmd {
		r := m.(loadDone)
		if r.err != nil {
			return replace(errorScreen(a, i18n.T("Betöltés"), r.err))
		}
		return replace(newRestorePreflight(r.src))
	})
	return s.busy.Init(a)
}
func (s *loadWrap) Update(a *App, msg tea.Msg) tea.Cmd { return s.busy.Update(a, msg) }
func (s *loadWrap) View(a *App, w, h int) string       { return s.busy.View(a, w, h) }

// ───────────────────────── preflight ─────────────────────────

type restorePreflight struct {
	src        *restore.Source
	f          form
	migrations []restore.Migration
	cands      []*restore.Candidate
	hw         sys.Hardware
	info       []string
	ready      bool
}

type preflightData struct {
	cands []*restore.Candidate
	migs  []restore.Migration
	hw    sys.Hardware
	ver   string
}

func newRestorePreflight(src *restore.Source) *restorePreflight { return &restorePreflight{src: src} }

func (s *restorePreflight) Title() string { return i18n.T("Visszaállítás – előkészítés") }
func (s *restorePreflight) Help() string {
	return i18n.T("Tab/↑/↓ mezőváltás  •  Space kapcsol  •  Enter az utolsón: tovább  •  Esc vissza")
}

func (s *restorePreflight) Init(a *App) tea.Cmd {
	if s.ready {
		return s.f.focus()
	}
	src := s.src
	return func() tea.Msg {
		return preflightData{restore.Candidates(a.ctx, a.d.Env, src), restore.PendingMigrations(a.d.Env, src),
			sys.DetectHardware(a.ctx, a.d.Env), a.d.Env.OmarchyVersion(a.ctx)}
	}
}

func (s *restorePreflight) Update(a *App, msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case preflightData:
		s.cands, s.migrations, s.hw = m.cands, m.migs, m.hw
		src := s.src.Manifest.Source
		th := a.th
		s.info = []string{
			th.text.Render(fmt.Sprintf(i18n.T("  Mentés: %s, gép: %s, felhasználó: %s"), s.src.Manifest.Created.Local().Format("2006-01-02 15:04"), src.Host, src.User)),
			th.text.Render(fmt.Sprintf(i18n.T("  Omarchy a mentéskor: %s  •  ezen a gépen: %s"), src.OmarchyVersion, m.ver)),
		}
		if src.Hardware.Summary() != m.hw.Summary() {
			s.info = append(s.info, th.warnS.Render(i18n.T("  Más hardver: "))+th.mutedS.Render(src.Hardware.Summary()+" → "+m.hw.Summary()+i18n.T(". A hardverfüggő tételek kimaradnak.")))
		}
		if src.Home != a.d.Env.Home {
			s.info = append(s.info, th.mutedS.Render(fmt.Sprintf(i18n.T("  Más home mappa (%s → %s): az útvonalakat a program kijavítja."), src.Home, a.d.Env.Home)))
		}
		fields := []*field{
			toggleField("snapshot", i18n.T("Rendszer-pillanatkép"), true, i18n.T("Előtte pillanatképet készítünk a rendszerről (a boot menüből visszaállítható).")),
			toggleField("update", i18n.T("Rendszerfrissítés"), true, i18n.T("A programok telepítése előtt frissítjük a rendszert (a legfrissebb verziók miatt ajánlott).")),
		}
		nConf := 0
		for _, mg := range s.migrations {
			if mg.TouchesConfig {
				nConf++
			}
		}
		if nConf > 0 {
			fields = append(fields, toggleField("migrations", fmt.Sprintf(i18n.T("Omarchy frissítő lépések (%d)"), nConf), true,
				i18n.T("Az újabb Omarchy olyan javításokat is tartalmaz, amelyek a mentett (régebbi) beállításaidon még nem futottak le. Ezeket lefuttatjuk rajtuk.")))
		}
		for _, g := range s.src.Manifest.GitProjects {
			if g.Recipe != "" {
				fields = append(fields, toggleField("recipe:"+g.ItemID, i18n.T("Telepítő: ")+filepath.Base(g.Path), true,
					fmt.Sprintf(i18n.T("A letöltés után lefuttatjuk: %s (a kimenetét látni fogod)."), g.Recipe)))
			}
		}
		s.f = form{fields: fields}
		s.ready = true
		return s.f.focus()
	case tea.KeyPressMsg:
		if m.String() == "esc" {
			return pop()
		}
	}
	if !s.ready {
		return nil
	}
	cmd, submit := s.f.update(msg)
	if !submit {
		return cmd
	}
	opts := restore.Options{Snapshot: s.f.value("snapshot") != "", UpdateSystem: s.f.value("update") != "", Recipes: map[string]bool{}}
	if s.f.value("migrations") != "" {
		for _, mg := range s.migrations {
			if mg.TouchesConfig {
				opts.Migrations = append(opts.Migrations, mg.Name)
			}
		}
	}
	for _, g := range s.src.Manifest.GitProjects {
		if s.f.value("recipe:"+g.ItemID) != "" {
			opts.Recipes[g.ItemID] = true
		}
	}
	return push(newRestoreSelect(s.src, s.cands, opts))
}

func (s *restorePreflight) View(a *App, w, h int) string {
	if !s.ready {
		return "\n  " + a.th.accentS.Render(i18n.T("A gép és a mentés összevetése…"))
	}
	var info []string
	for _, l := range s.info {
		info = append(info, truncate(l, w))
	}
	return "\n" + strings.Join(info, "\n") + "\n\n" + s.f.view(a, w)
}

// ───────────────────────── restore selection ─────────────────────────

type restoreSelect struct {
	src   *restore.Source
	cands []*restore.Candidate
	opts  restore.Options
	tree  *selTree
}

func newRestoreSelect(src *restore.Source, cands []*restore.Candidate, opts restore.Options) *restoreSelect {
	s := &restoreSelect{src: src, cands: cands, opts: opts, tree: newSelTree(true)}
	for _, c := range cands {
		c := c
		s.tree.add(&selItem{id: c.ID, label: c.Label, detail: c.Detail, cat: c.Category, kind: c.Kind, size: c.Size,
			sel: &c.Selected, enabled: c.Available, reason: c.Reason, hardware: c.Hardware, secret: c.Secret, requires: c.Requires})
	}
	s.tree.order()
	return s
}

func (s *restoreSelect) Init(a *App) tea.Cmd { return nil }
func (s *restoreSelect) Title() string       { return i18n.T("Visszaállítás – mit kérsz vissza?") }
func (s *restoreSelect) Help() string {
	return i18n.T("Space kijelöl  •  Enter/→ kinyit  •  a mind  •  d részletek  •  n tovább  •  Esc vissza")
}

func (s *restoreSelect) Update(a *App, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc", "q":
			return pop()
		case "n", "ctrl+n", "f":
			sel := map[string]bool{}
			for _, c := range s.cands {
				if c.Selected && c.Available {
					sel[c.ID] = true
				}
			}
			if len(sel) == 0 {
				return push(newMessage(i18n.T("Nincs kijelölve semmi"), []string{a.th.text.Render(i18n.T("Jelölj ki legalább egy tételt."))}))
			}
			s.opts.Selected = sel
			return push(newRestoreConfirm(s.src, s.opts))
		}
	}
	s.tree.update(msg)
	return nil
}

func (s *restoreSelect) View(a *App, w, h int) string {
	n, size := s.tree.totalSelected()
	head := a.th.text.Render(fmt.Sprintf(i18n.T("Kiválasztva: %d tétel, kb. %s"), n, inventory.HumanSize(size))) +
		a.th.mutedS.Render(i18n.T("  •  a szürke tételek ezen a gépen nem állíthatók vissza"))
	return head + "\n" + s.tree.view(a, w, h-1)
}

// ───────────────────────── confirm ─────────────────────────

func newRestoreConfirm(src *restore.Source, opts restore.Options) Screen {
	return &confirmWrap{src: src, opts: opts}
}

type confirmWrap struct {
	src  *restore.Source
	opts restore.Options
	m    *messageScreen
}

func (s *confirmWrap) Title() string { return i18n.T("Visszaállítás – indítás") }
func (s *confirmWrap) Help() string  { return i18n.T("↑/↓  •  Enter  •  Esc vissza") }
func (s *confirmWrap) Init(a *App) tea.Cmd {
	th := a.th
	body := []string{
		th.text.Render(fmt.Sprintf(i18n.T("  %d tételt állítunk vissza erre a gépre."), len(s.opts.Selected))),
		"",
		th.mutedS.Render(i18n.T("  • A programokat a legfrissebb verzióval telepítjük (internet kell).")),
		th.mutedS.Render(i18n.T("  • Amit felülírunk, arról másolat készül: ") + filepath.Join(s.src.WorkDir, "undo")),
		th.mutedS.Render(i18n.T("    Visszavonás: obr undo")),
		th.mutedS.Render(i18n.T("  • A rendszergazdai jelszót a sudo kéri (nem az obr).")),
		th.mutedS.Render(i18n.T("  • Ha a Chrome fut, előtte zárd be.")),
	}
	s.m = newMessage("", body,
		option{label: i18n.T("Indítás"), run: func() tea.Cmd {
			return a.ensureSudo(func(ok bool) tea.Msg { return sudoResult{ok} })
		}},
		option{label: i18n.T("Vissza"), run: pop})
	return nil
}
func (s *confirmWrap) Update(a *App, msg tea.Msg) tea.Cmd {
	if r, ok := msg.(sudoResult); ok {
		if !r.ok {
			return push(newMessage(i18n.T("Rendszergazdai jog"), []string{a.th.warnS.Render(i18n.T("  A sudo nem engedélyezte. Programtelepítés és rendszerbeállítás nélkül folytatod?"))},
				option{label: i18n.T("Folytatás így"), run: func() tea.Cmd { return tea.Sequence(pop(), replace(newRestoreRun(s.src, s.opts))) }},
				option{label: i18n.T("Vissza"), run: pop}))
		}
		return replace(newRestoreRun(s.src, s.opts))
	}
	return s.m.Update(a, msg)
}
func (s *confirmWrap) View(a *App, w, h int) string { return s.m.View(a, w, h) }

// ───────────────────────── restore progress ─────────────────────────

type rEvent struct{ e restore.Event }
type rDone struct {
	rep *restore.Report
	err error
}
type conflictAsk struct {
	c     restore.Conflict
	reply chan restore.Resolution
}
type execAsk struct {
	c     *exec.Cmd
	reply chan error
}
type execDone struct {
	err   error
	reply chan error
}

type restoreRun struct {
	src      *restore.Source
	opts     restore.Options
	ch       chan tea.Msg
	steps    map[string]string
	current  string
	log      []string
	prog     *engine.Progress
	bar      progress.Model
	sp       spinner.Model
	started  time.Time
	conflict *conflictAsk
	cList    optionList
	showDiff bool
}

func newRestoreRun(src *restore.Source, opts restore.Options) *restoreRun {
	return &restoreRun{src: src, opts: opts, ch: make(chan tea.Msg, 64), steps: map[string]string{}}
}

func (s *restoreRun) Title() string { return i18n.T("Visszaállítás folyamatban") }
func (s *restoreRun) Help() string {
	if s.conflict != nil {
		return i18n.T("↑/↓  •  Enter választ  •  v különbség mutatása")
	}
	return i18n.T("Dolgozom… (Ctrl+C megszakítás)")
}

func (s *restoreRun) Init(a *App) tea.Cmd {
	if !s.started.IsZero() {
		return nil
	}
	s.started = time.Now()
	s.bar = progress.New(progress.WithWidth(50), progress.WithColors(a.th.accent, a.th.yellow), progress.WithoutPercentage())
	s.sp = spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(a.th.accentS))
	a.busy = true
	opts := s.opts
	opts.Resolver = func(c restore.Conflict) restore.Resolution {
		reply := make(chan restore.Resolution)
		s.ch <- conflictAsk{c, reply}
		return <-reply
	}
	opts.Interactive = func(c *exec.Cmd) error {
		reply := make(chan error)
		s.ch <- execAsk{c, reply}
		return <-reply
	}
	go func() {
		rep, err := restore.Run(a.ctx, a.d.Env, s.src, opts, func(e restore.Event) { s.ch <- rEvent{e} })
		s.ch <- rDone{rep, err}
	}()
	return tea.Batch(listen(s.ch), s.sp.Tick)
}

func (s *restoreRun) Update(a *App, msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case rEvent:
		e := m.e
		switch {
		case e.Progress != nil:
			s.prog = e.Progress
		case e.Status == "start":
			s.current = e.Step
			s.steps[e.Step] = "run"
			s.prog = nil
		case e.Status == "ok" || e.Status == "skip" || e.Status == "fail":
			s.steps[e.Step] = e.Status
		case e.Status == "info":
			s.log = append(s.log, a.th.okS.Render("✓ ")+e.Message)
		case e.Status == "warn":
			s.log = append(s.log, a.th.warnS.Render("! ")+e.Message)
		}
		return listen(s.ch)
	case conflictAsk:
		s.conflict = &m
		s.showDiff = false
		opts := []option{
			{label: i18n.T("A saját beállításom marad"), desc: i18n.T("Az új Omarchy-alapértéket .obr-uj-alap-* néven mellé tesszük."), run: s.answer(restore.KeepMine)},
			{label: i18n.T("Az új Omarchy-alapértéket kérem"), desc: i18n.T("A te változatod .obr-sajat-* néven megmarad mellette."), run: s.answer(restore.TakeNew)},
		}
		if m.c.HasMerged {
			opts = append(opts, option{label: i18n.T("Összefésült változat ütközésjelekkel"), desc: i18n.T("Mindkettő benne lesz; a <<<<<<< jelek között kézzel kell rendbe tenni."), run: s.answer(restore.UseMerged)})
		}
		s.cList = optionList{opts: opts}
		return nil
	case execAsk:
		reply := m.reply
		m.c.Stdin, m.c.Stdout, m.c.Stderr = nil, nil, nil
		wrapped := exec.Command("bash", "-c", `echo; echo "▶ $0"; echo; cd "$1" && bash -lc "$2"; rc=$?; echo; printf "$3" "$rc"; read -n1 -s -r; exit $rc`,
			i18n.T("Telepítő futtatása"), m.c.Dir, strings.Join(m.c.Args[2:], " "),
			i18n.T("Kész (kilépési kód: %s). Nyomj egy gombot a folytatáshoz…"))
		return tea.ExecProcess(wrapped, func(err error) tea.Msg { return execDone{err, reply} })
	case execDone:
		m.reply <- m.err
		return listen(s.ch)
	case rDone:
		a.busy = false
		if m.err != nil && m.rep == nil {
			return replace(errorScreen(a, i18n.T("A visszaállítás megszakadt"), m.err, i18n.T("Napló: ")+a.d.LogPath))
		}
		return replace(newRestoreSummary(a, m.rep, m.err))
	case spinner.TickMsg:
		var cmd tea.Cmd
		s.sp, cmd = s.sp.Update(m)
		return cmd
	case tea.KeyPressMsg:
		if s.conflict != nil {
			if m.String() == "v" {
				s.showDiff = !s.showDiff
				return nil
			}
			cmd, _ := s.cList.update(msg)
			return cmd
		}
	}
	return nil
}

func (s *restoreRun) answer(r restore.Resolution) func() tea.Cmd {
	return func() tea.Cmd {
		c := s.conflict
		s.conflict = nil
		c.reply <- r
		return listen(s.ch)
	}
}

func (s *restoreRun) View(a *App, w, h int) string {
	th := a.th
	var b strings.Builder
	if s.conflict != nil {
		c := s.conflict.c
		b.WriteString("\n" + th.warnS.Render(i18n.T("  Ütközés: ~/")+c.Path) + "\n")
		for _, l := range wrap(c.Reason, w-4) {
			b.WriteString(th.text.Render("  "+l) + "\n")
		}
		b.WriteString("\n")
		if s.showDiff {
			lines := strings.Split(c.Diff, "\n")
			maxL := h - 12
			if maxL < 5 {
				maxL = 5
			}
			for i, l := range lines {
				if i >= maxL {
					b.WriteString(th.dimS.Render("  …") + "\n")
					break
				}
				st := th.mutedS
				switch {
				case strings.HasPrefix(l, "+"):
					st = th.okS
				case strings.HasPrefix(l, "-"):
					st = th.errS
				}
				b.WriteString("  " + st.Render(truncate(l, w-4)) + "\n")
			}
			b.WriteString("\n")
		}
		b.WriteString(s.cList.view(a, w))
		return b.String()
	}
	b.WriteString("\n")
	for _, st := range restore.Steps {
		state := s.steps[st]
		mark := th.dimS.Render("  · ")
		title := th.mutedS.Render(restore.StepTitle(st))
		switch state {
		case "run":
			mark = "  " + s.sp.View() + " "
			title = th.h2.Render(restore.StepTitle(st))
		case "ok":
			mark = th.okS.Render("  ✓ ")
			title = th.text.Render(restore.StepTitle(st))
		case "skip":
			mark = th.dimS.Render("  – ")
		case "fail":
			mark = th.errS.Render("  ✗ ")
			title = th.text.Render(restore.StepTitle(st))
		}
		b.WriteString(mark + title + "\n")
		if state == "run" && s.prog != nil && s.prog.TotalBytes > 0 {
			p := s.prog
			b.WriteString("      " + s.bar.ViewAs(p.Percent) + th.mutedS.Render(fmt.Sprintf(i18n.T("  %3.0f%%  %d/%d fájl"), p.Percent*100, p.FilesDone, p.TotalFiles)) + "\n")
		}
	}
	room := h - len(restore.Steps) - 4
	if room > 0 && len(s.log) > 0 {
		b.WriteString("\n")
		start := 0
		if len(s.log) > room {
			start = len(s.log) - room
		}
		for _, l := range s.log[start:] {
			b.WriteString("  " + truncate(l, w-4) + "\n")
		}
	}
	return b.String()
}

// ───────────────────────── summary ─────────────────────────

func newRestoreSummary(a *App, rep *restore.Report, err error) *messageScreen {
	th := a.th
	var body []string
	if err != nil {
		body = append(body, th.errS.Render(i18n.T("  A visszaállítás megszakadt: "))+th.text.Render(err.Error()), "")
	} else {
		body = append(body, th.okS.Render(i18n.T("  ✓ A visszaállítás elkészült.")), "")
	}
	for _, d := range rep.Done {
		body = append(body, th.mutedS.Render("    ✓ "+d))
	}
	if len(rep.Warnings) > 0 {
		body = append(body, "", th.warnS.Render(fmt.Sprintf(i18n.T("  Figyelmeztetések (%d):"), len(rep.Warnings))))
		for _, w := range rep.Warnings {
			body = append(body, th.mutedS.Render("    ! "+w))
		}
	}
	if len(rep.Manual) > 0 {
		body = append(body, "", th.h2.Render(i18n.T("  Teendőid:")))
		for _, m := range rep.Manual {
			body = append(body, th.text.Render("    → "+m))
		}
	}
	body = append(body, "", th.dimS.Render(i18n.T("  Felülírt fájlok másolata: ")+rep.UndoDir+i18n.T("   (visszavonás: obr undo)")),
		th.dimS.Render(i18n.T("  Napló: ")+a.d.LogPath))
	opts := []option{{label: i18n.T("Vissza a menübe"), run: goHome}, {label: i18n.T("Kilépés"), run: quit}}
	if rep.NeedsRelogin {
		opts = append([]option{{label: i18n.T("Újraindítás most"), desc: i18n.T("A kulcstartó, a csoporttagság és a rendszerszolgáltatások az újraindítás után érvényesülnek teljesen."),
			run: func() tea.Cmd {
				return func() tea.Msg {
					_ = exec.Command("systemctl", "reboot").Start()
					return quitMsg{}
				}
			}}}, opts...)
	}
	return &messageScreen{title: i18n.T("Visszaállítás kész"), body: body, list: optionList{opts: opts}}
}
