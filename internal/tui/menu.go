package tui

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/gaborkis11/omarchy-backup-restore/internal/backup"
	"github.com/gaborkis11/omarchy-backup-restore/internal/config"
	"github.com/gaborkis11/omarchy-backup-restore/internal/engine"
	"github.com/gaborkis11/omarchy-backup-restore/internal/inventory"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

// ───────────────────────── main menu ─────────────────────────

type menuScreen struct{ list optionList }

func newMenu() *menuScreen { return &menuScreen{} }

func (s *menuScreen) Init(a *App) tea.Cmd {
	cur := s.list.cur
	s.list = optionList{cur: cur, opts: []option{
		{label: i18n.T("Mentés készítése"), desc: i18n.T("Felméri a gépet, kiválasztod mit mentsen, és titkosítva menti pendrive-ra, külső lemezre vagy a NAS-ra."),
			run: func() tea.Cmd { return push(newDestPicker(purposeBackup)) }},
		{label: i18n.T("Visszaállítás"), desc: i18n.T("Egy korábbi mentés visszatöltése erre a gépre: programok, beállítások, kulcsok, fájlok."),
			run: func() tea.Cmd { return push(newDestPicker(purposeRestore)) }},
		{label: i18n.T("Mentések kezelése"), desc: i18n.T("Mentések listája, ellenőrzés, régi mentések törlése, visszaállító készlet frissítése."),
			run: func() tea.Cmd { return push(newDestPicker(purposeManage)) }},
		{label: i18n.T("Ütemezett mentés"), desc: a.scheduleStatus(),
			run: func() tea.Cmd { return push(newScheduleScreen()) }},
		{label: i18n.T("Beállítások"), desc: i18n.T("Nyelv, megőrzési szabály, konzisztens (pillanatképes) mentés, célhelyek."),
			run: func() tea.Cmd { return push(newSettingsScreen()) }},
		{label: i18n.T("Kilépés"), run: quit},
	}}
	return nil
}

func (a *App) scheduleStatus() string {
	s := a.d.Config.Schedule
	if !s.Enabled {
		return i18n.T("Jelenleg kikapcsolva. Bekapcsolva a gép magától ment (pl. naponta este).")
	}
	return fmt.Sprintf(i18n.T("Bekapcsolva: %s → %s"), s.OnCalendar, s.Destination)
}

func (s *menuScreen) Title() string { return "" }
func (s *menuScreen) Help() string {
	return i18n.T("↑/↓ vagy 1–6  •  Enter választ  •  q kilépés")
}
func (s *menuScreen) Update(a *App, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok && (k.String() == "q" || k.String() == "esc") {
		return quit()
	}
	cmd, _ := s.list.update(msg)
	return cmd
}
func (s *menuScreen) View(a *App, w, h int) string {
	th := a.th
	var b strings.Builder
	b.WriteString("\n")
	logo := []string{
		"   ┌─┐┌┐ ┬─┐",
		i18n.T("   │ │├┴┐├┬┘   Mentés és visszaállítás Omarchy-hoz"),
		i18n.T("   └─┘└─┘┴└─   beállítások • programok • kulcsok • fájlok"),
	}
	for _, l := range logo {
		b.WriteString(th.accentS.Render(l) + "\n")
	}
	b.WriteString("\n")
	if m := backup.LoadLast(a.d.Env, ""); m != nil {
		ago := time.Since(m.Created)
		b.WriteString(th.mutedS.Render(fmt.Sprintf(i18n.T("   Utolsó mentés: %s (%s)"), m.Created.Format("2006-01-02 15:04"), humanAgo(ago))) + "\n\n")
	} else {
		b.WriteString(th.mutedS.Render(i18n.T("   Erről a gépről még nem készült mentés.")) + "\n\n")
	}
	b.WriteString(s.list.view(a, w))
	return b.String()
}

func humanAgo(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf(i18n.T("%d perce"), int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf(i18n.T("%d órája"), int(d.Hours()))
	default:
		return fmt.Sprintf(i18n.T("%d napja"), int(d.Hours()/24))
	}
}

// ───────────────────────── destination picker ─────────────────────────

type purpose int

const (
	purposeBackup purpose = iota
	purposeRestore
	purposeManage
	purposeSchedule
)

// restoring reports whether the destination must already hold a backup.
func (p purpose) restoring() bool { return p == purposeRestore || p == purposeManage }

func (p purpose) title() string {
	switch p {
	case purposeBackup:
		return i18n.T("Mentés – hova?")
	case purposeRestore:
		return i18n.T("Visszaállítás – honnan?")
	case purposeManage:
		return i18n.T("Mentések kezelése – melyik helyen?")
	}
	return i18n.T("Ütemezett mentés – hova?")
}

type drivesMsg struct {
	drives []engine.Drive
	err    error
}

type destPicker struct {
	p      purpose
	list   optionList
	drives []engine.Drive
	loaded bool
}

func newDestPicker(p purpose) *destPicker { return &destPicker{p: p} }

func (s *destPicker) Init(a *App) tea.Cmd {
	s.build(a)
	return func() tea.Msg {
		d, err := engine.ListDrives(a.ctx, a.d.Env.Runner)
		return drivesMsg{d, err}
	}
}
func (s *destPicker) Title() string { return s.p.title() }
func (s *destPicker) Help() string  { return i18n.T("↑/↓  •  Enter választ  •  Esc vissza") }

func (s *destPicker) build(a *App) {
	var opts []option
	known := map[string]bool{}
	for _, d := range a.d.Config.Destinations {
		d := d
		desc := ""
		switch d.Type {
		case config.Local:
			known[d.UUID] = true
			known[d.Path] = true
			mounted := false
			for _, dr := range s.drives {
				if dr.UUID != "" && dr.UUID == d.UUID || dr.Mountpoint == d.Path {
					mounted = true
				}
			}
			if d.UUID == "" || mounted {
				desc = i18n.T("Helyi: ") + d.Path
			} else {
				desc = i18n.T("A lemez nincs csatlakoztatva.")
			}
		default:
			desc = fmt.Sprintf(i18n.T("Hálózat (%s): %s@%s, megosztás: %s"), strings.ToUpper(d.Type), d.User, d.Host, d.Share)
		}
		opts = append(opts, option{label: d.Name, desc: desc, run: func() tea.Cmd { return s.chosen(a, d) }})
	}
	for _, dr := range s.drives {
		dr := dr
		if known[dr.UUID] || known[dr.Mountpoint] {
			continue
		}
		name := dr.Label
		if name == "" {
			name = filepath.Base(dr.Mountpoint)
		}
		desc := fmt.Sprintf(i18n.T("%s, %s, %s szabad (%s)"), dr.Mountpoint, strings.ToUpper(dr.FSType), dr.FSAvail, dr.Size)
		if dr.HasBackup {
			desc += i18n.T(" – van rajta mentés")
		}
		opts = append(opts, option{label: i18n.T("Lemez: ") + name, desc: desc, run: func() tea.Cmd {
			d := config.Destination{Name: name, Type: config.Local, Path: dr.Mountpoint, UUID: dr.UUID, Label: dr.Label}
			a.d.Config.Upsert(d)
			_ = a.d.Config.Save()
			return s.chosen(a, d)
		}})
	}
	if !s.loaded {
		opts = append(opts, option{label: i18n.T("Meghajtók keresése…"), disabled: true})
	}
	opts = append(opts,
		option{label: i18n.T("Synology / NAS hozzáadása"), desc: i18n.T("Hálózati mentés SFTP-n vagy SMB-n keresztül (felhasználónév + jelszó)."),
			run: func() tea.Cmd { return push(newNASForm(s.p)) }},
		option{label: i18n.T("Mappa tallózása"), desc: i18n.T("Bármilyen mappa a gépen vagy egy csatolt lemezen (pl. /mnt/backup)."),
			run: func() tea.Cmd { return push(newLocalBrowser(s.p, a)) }},
		option{label: i18n.T("Vissza"), run: pop},
	)
	cur := s.list.cur
	s.list = optionList{opts: opts, cur: cur}
	if s.list.cur >= len(opts) || opts[s.list.cur].disabled {
		s.list.cur = 0
	}
}

func (s *destPicker) chosen(a *App, d config.Destination) tea.Cmd {
	return nextAfterDest(a, s.p, d)
}

func nextAfterDest(a *App, p purpose, d config.Destination) tea.Cmd {
	switch p {
	case purposeBackup:
		return push(newScanScreen(d))
	case purposeSchedule:
		return push(newScheduleForm(d))
	default:
		return push(newPasswordScreen(d, p, func(loc *engine.Location, exists bool) tea.Cmd {
			if p == purposeRestore {
				return push(newBackupList(loc, true))
			}
			return push(newBackupList(loc, false))
		}))
	}
}

func (s *destPicker) Update(a *App, msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case drivesMsg:
		s.drives, s.loaded = m.drives, true
		s.build(a)
		return nil
	case tea.KeyPressMsg:
		if m.String() == "esc" || m.String() == "q" {
			return pop()
		}
	}
	cmd, _ := s.list.update(msg)
	return cmd
}

func (s *destPicker) View(a *App, w, h int) string {
	return "\n" + s.list.view(a, w)
}

// ───────────────────────── NAS form ─────────────────────────

type nasForm struct {
	p    purpose
	f    form
	busy bool
}

type nasTestMsg struct {
	d    config.Destination
	pw   string
	dirs []string
	err  error
}

func newNASForm(p purpose) *nasForm {
	return &nasForm{p: p, f: form{fields: []*field{
		newField("name", i18n.T("Név"), "Synology", "", fText),
		newField("host", i18n.T("NAS címe"), "", i18n.T("IP-cím vagy név, pl. 192.168.1.10 vagy NAS.local."), fText),
		choiceField("type", i18n.T("Kapcsolat"), []string{"SFTP", "SMB"}, 0,
			i18n.T("SFTP: DSM → Vezérlőpult → Fájlszolgáltatások → FTP → SFTP bekapcsolása. SMB: Windows-megosztás, mindig be van kapcsolva.")),
		newField("user", i18n.T("Felhasználó"), "", i18n.T("A DSM felhasználóneved."), fText),
		newField("pass", i18n.T("Jelszó"), "", i18n.T("Csak erre a munkamenetre jegyezzük meg; nem kerül fájlba."), fPassword),
	}}}
}
func (s *nasForm) Init(a *App) tea.Cmd { return s.f.focus() }
func (s *nasForm) Title() string       { return i18n.T("Synology / NAS hozzáadása") }
func (s *nasForm) Help() string {
	return i18n.T("Tab/↑/↓ mezőváltás  •  ←/→ választás  •  Enter az utolsó mezőn: kapcsolódás  •  Esc vissza")
}
func (s *nasForm) Update(a *App, msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case nasTestMsg:
		s.busy = false
		if m.err != nil {
			s.f.err = i18n.T("Nem sikerült kapcsolódni: ") + m.err.Error()
			return nil
		}
		return push(newNASBrowser(s.p, m.d, m.pw, m.dirs))
	case tea.KeyPressMsg:
		if m.String() == "esc" {
			return pop()
		}
	}
	if s.busy {
		return nil
	}
	cmd, submit := s.f.update(msg)
	if !submit {
		return cmd
	}
	d := config.Destination{Name: s.f.value("name"), Type: strings.ToLower(s.f.value("type")), Host: s.f.value("host"), User: s.f.value("user")}
	pw := s.f.raw("pass")
	if d.Name == "" || d.Host == "" || d.User == "" || pw == "" {
		s.f.err = i18n.T("Minden mező kell.")
		return nil
	}
	s.busy = true
	s.f.err = ""
	return func() tea.Msg {
		if d.Type == config.SFTP {
			if err := LearnHostKey(a.ctx, a.d.Env, &d); err != nil {
				return nasTestMsg{d: d, pw: pw, err: fmt.Errorf(i18n.T("az SFTP nem érhető el (be van kapcsolva a DSM-ben?): %v"), err)}
			}
		}
		dirs, err := a.browse(d, pw, "")
		return nasTestMsg{d: d, pw: pw, dirs: dirs, err: err}
	}
}
func (s *nasForm) View(a *App, w, h int) string {
	out := "\n" + s.f.view(a, w)
	if s.busy {
		out += "\n  " + a.th.accentS.Render(i18n.T("Kapcsolódás a NAS-hoz…"))
	}
	return out
}

func (a *App) nasCreds(d config.Destination, pw string) engine.Credentials {
	cr := engine.Credentials{NASPassword: pw}
	if kh := filepath.Join(a.d.Env.ConfigDir, "known_hosts"); d.Type == config.SFTP && fileExists(kh) {
		cr.KnownHosts = kh
	}
	return cr
}

func (a *App) browse(d config.Destination, pw, rel string) ([]string, error) {
	return engine.Browse(a.ctx, a.d.Env.Runner, engine.FindTool(a.d.Env.BinDir, "rclone"), d, a.nasCreds(d, pw), rel)
}

// ───────────────────────── NAS folder browser ─────────────────────────

type browseMsg struct {
	path []string
	dirs []string
	err  error
}

type nasBrowser struct {
	p       purpose
	d       config.Destination
	pw      string
	path    []string
	dirs    []string
	backup  bool // the current folder already holds an obr backup
	loading bool
	err     string
	cur     int
	newDir  *form
}

func newNASBrowser(p purpose, d config.Destination, pw string, dirs []string) *nasBrowser {
	s := &nasBrowser{p: p, d: d, pw: pw}
	s.setDirs(dirs)
	return s
}

func (s *nasBrowser) setDirs(dirs []string) {
	s.dirs, s.backup = nil, false
	for _, d := range dirs {
		if d == engine.BaseDir {
			s.backup = true
			continue
		}
		s.dirs = append(s.dirs, d)
	}
	s.cur = 0
}

func (s *nasBrowser) Title() string {
	if s.p.restoring() {
		return i18n.T("Synology / NAS – hol van a mentés?")
	}
	return i18n.T("Synology / NAS – hova mentsen?")
}
func (s *nasBrowser) Help() string {
	if s.newDir != nil {
		return i18n.T("Enter létrehozás  •  Esc mégse")
	}
	return i18n.T("↑/↓  •  Enter megnyit / választ  •  ←/Backspace fel  •  n új mappa  •  Esc vissza")
}
func (s *nasBrowser) Init(a *App) tea.Cmd { return nil }

func (s *nasBrowser) load(a *App, path []string) tea.Cmd {
	s.loading, s.err = true, ""
	d, pw := s.d, s.pw
	return func() tea.Msg {
		dirs, err := a.browse(d, pw, strings.Join(path, "/"))
		return browseMsg{path: path, dirs: dirs, err: err}
	}
}

// rows: [choose here] [up] dirs...
func (s *nasBrowser) rows() []string {
	var r []string
	if len(s.path) > 0 {
		if !s.p.restoring() || s.backup {
			r = append(r, "choose")
		}
		r = append(r, "up")
	}
	for _, d := range s.dirs {
		r = append(r, "dir:"+d)
	}
	return r
}

func (s *nasBrowser) choose(a *App) tea.Cmd {
	d := s.d
	d.Share = s.path[0]
	d.Folder = strings.Join(s.path[1:], "/")
	a.d.Config.Upsert(d)
	_ = a.d.Config.Save()
	a.nasPasswords[d.Name] = s.pw
	return tea.Sequence(func() tea.Msg { return popMsg{2} }, nextAfterDest(a, s.p, d))
}

func (s *nasBrowser) up(a *App) tea.Cmd {
	if len(s.path) == 0 {
		return nil
	}
	return s.load(a, append([]string{}, s.path[:len(s.path)-1]...))
}

func (s *nasBrowser) Update(a *App, msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case browseMsg:
		s.loading = false
		if m.err != nil {
			s.err = m.err.Error()
			return nil
		}
		s.path = m.path
		s.setDirs(m.dirs)
		return nil
	case tea.KeyPressMsg:
		if s.newDir != nil {
			switch m.String() {
			case "esc":
				s.newDir = nil
				return nil
			}
			cmd, submit := s.newDir.update(msg)
			if !submit {
				return cmd
			}
			name := strings.Trim(s.newDir.value("name"), "/ ")
			if name == "" || strings.Contains(name, "/") {
				s.newDir.err = i18n.T("Érvénytelen mappanév.")
				return nil
			}
			s.newDir = nil
			target := append(append([]string{}, s.path...), name)
			d, pw := s.d, s.pw
			s.loading = true
			return func() tea.Msg {
				if err := engine.MakeDir(a.ctx, a.d.Env.Runner, engine.FindTool(a.d.Env.BinDir, "rclone"), d, a.nasCreds(d, pw), strings.Join(target, "/")); err != nil {
					return browseMsg{path: s.path, err: err}
				}
				dirs, err := a.browse(d, pw, strings.Join(target, "/"))
				return browseMsg{path: target, dirs: dirs, err: err}
			}
		}
		if s.loading {
			return nil
		}
		rows := s.rows()
		switch m.String() {
		case "esc", "q":
			return pop()
		case "up", "k":
			if s.cur > 0 {
				s.cur--
			}
		case "down", "j":
			if s.cur < len(rows)-1 {
				s.cur++
			}
		case "left", "h", "backspace":
			return s.up(a)
		case "n":
			if len(s.path) == 0 {
				s.err = i18n.T("Új mappát egy megosztott mappán belül lehet létrehozni.")
				return nil
			}
			s.newDir = &form{fields: []*field{newField("name", i18n.T("Új mappa neve"), "omarchy", "", fText)}}
			return s.newDir.focus()
		case "enter", "right", "l", "space":
			if s.cur >= len(rows) {
				return nil
			}
			switch r := rows[s.cur]; {
			case r == "choose":
				if m.String() == "enter" || m.String() == "space" {
					return s.choose(a)
				}
			case r == "up":
				return s.up(a)
			default:
				return s.load(a, append(append([]string{}, s.path...), strings.TrimPrefix(r, "dir:")))
			}
		}
	}
	return nil
}

func (s *nasBrowser) View(a *App, w, h int) string {
	th := a.th
	var b strings.Builder
	loc := "/" + strings.Join(s.path, "/")
	b.WriteString("\n  " + th.mutedS.Render(fmt.Sprintf("%s@%s (%s)", s.d.User, s.d.Host, strings.ToUpper(s.d.Type))) + "\n")
	b.WriteString("  " + th.h2.Render(i18n.T("Hely: ")+loc) + "\n")
	switch {
	case len(s.path) == 0:
		b.WriteString("  " + th.mutedS.Render(i18n.T("Válassz egy megosztott mappát (Enter = belépés).")) + "\n")
	case s.backup:
		b.WriteString("  " + th.okS.Render(i18n.T("Ebben a mappában már van Omarchy-mentés.")) + "\n")
	case s.p.restoring():
		b.WriteString("  " + th.mutedS.Render(i18n.T("Itt nincs mentés. Keresd meg azt a mappát, amelyben az omarchy-backup mappa van.")) + "\n")
	default:
		b.WriteString("  " + th.mutedS.Render(i18n.T("Lépj be egy mappába, vagy válaszd ezt; n = új mappa.")) + "\n")
	}
	b.WriteString("\n")
	if s.newDir != nil {
		b.WriteString(s.newDir.view(a, w))
		return b.String()
	}
	if s.loading {
		b.WriteString("  " + th.accentS.Render(i18n.T("Betöltés…")) + "\n")
		return b.String()
	}
	rows := s.rows()
	if len(rows) == 0 {
		b.WriteString("  " + th.dimS.Render(i18n.T("(nincs megosztott mappa, amihez hozzáférsz)")) + "\n")
	}
	room := h - 8
	if room < 5 {
		room = 5
	}
	start := 0
	if s.cur >= room {
		start = s.cur - room + 1
	}
	for i := start; i < len(rows) && i < start+room; i++ {
		var label string
		switch r := rows[i]; {
		case r == "choose" && s.p.restoring():
			label = th.okS.Render("✓ " + i18n.T("Ezt a mentést nyisd meg: ") + loc)
		case r == "choose":
			label = th.okS.Render("✓ " + i18n.T("Ide mentsen: ") + loc)
		case r == "up":
			label = th.mutedS.Render("‹ " + i18n.T("Vissza egy szinttel"))
		default:
			label = th.text.Render("▸ " + strings.TrimPrefix(r, "dir:"))
		}
		if i == s.cur {
			b.WriteString(th.accentS.Render(" › ") + label + "\n")
		} else {
			b.WriteString("   " + label + "\n")
		}
	}
	if s.err != "" {
		b.WriteString("\n  " + th.errS.Render(s.err) + "\n")
	}
	return b.String()
}

// resolve opens a destination with the given passwords.
func (a *App) resolve(d config.Destination, repoPW, nasPW string) (*engine.Location, error) {
	cr := engine.Credentials{RepoPassword: repoPW, NASPassword: nasPW}
	if kh := filepath.Join(a.d.Env.ConfigDir, "known_hosts"); d.Type == config.SFTP && fileExists(kh) {
		cr.KnownHosts = kh
	}
	return engine.Resolve(a.ctx, a.d.Env.Runner, d, cr,
		engine.FindTool(a.d.Env.BinDir, "restic"), engine.FindTool(a.d.Env.BinDir, "rclone"))
}

// ───────────────────────── password screen ─────────────────────────

type pwStage int

const (
	pwProbe pwStage = iota
	pwNAS
	pwEnter
	pwRecovery
)

type probeMsg struct {
	exists bool
	err    error
}
type openMsg struct {
	loc *engine.Location
	err error
}

type passwordScreen struct {
	d        config.Destination
	p        purpose
	next     func(loc *engine.Location, exists bool) tea.Cmd
	stage    pwStage
	exists   bool
	f        form
	busy     string
	recovery string
	loc      *engine.Location
	// AllowNew lets the backup flow create a repository.
	allowNew bool
}

func newPasswordScreen(d config.Destination, p purpose, next func(loc *engine.Location, exists bool) tea.Cmd) *passwordScreen {
	return &passwordScreen{d: d, p: p, next: next, allowNew: p == purposeBackup || p == purposeSchedule}
}

func (s *passwordScreen) Title() string { return i18n.T("Jelszó – ") + s.d.Name }
func (s *passwordScreen) Help() string {
	return i18n.T("Enter tovább  •  Tab mezőváltás  •  Esc vissza")
}

func (s *passwordScreen) Init(a *App) tea.Cmd {
	if s.stage != pwProbe {
		return s.f.focus()
	}
	if s.d.Remote() && a.nasPasswords[s.d.Name] == "" {
		if pw, ok := config.ReadSecret(a.d.Env.ConfigDir, "nas", s.d.Name); ok {
			a.nasPasswords[s.d.Name] = pw
		}
	}
	if s.d.Remote() && a.nasPasswords[s.d.Name] == "" {
		s.stage = pwNAS
		s.f = form{fields: []*field{newField("nas", i18n.T("A NAS jelszava"), "", fmt.Sprintf("%s@%s", s.d.User, s.d.Host), fPassword)}}
		return s.f.focus()
	}
	return s.probe(a)
}

func (s *passwordScreen) probe(a *App) tea.Cmd {
	s.busy = i18n.T("A mentés helyének ellenőrzése…")
	d := s.d
	nas := a.nasPasswords[d.Name]
	return func() tea.Msg {
		loc, err := a.resolve(d, "__obr_probe__", nas)
		if err != nil {
			return probeMsg{err: err}
		}
		ok, err := loc.Repo.Exists(a.ctx)
		if errors.Is(err, engine.ErrBadPass) {
			return probeMsg{exists: true}
		}
		return probeMsg{exists: ok, err: err}
	}
}

func (s *passwordScreen) enterStage(a *App) tea.Cmd {
	s.stage = pwEnter
	if !s.exists && s.allowNew {
		s.f = form{fields: []*field{
			newField("pw", i18n.T("Új jelszó"), "", i18n.T("Ezzel titkosítjuk a mentést. Legalább 8 karakter. Jelszó nélkül a mentés NEM állítható vissza!"), fPassword),
			newField("pw2", i18n.T("Még egyszer"), "", "", fPassword),
			toggleField("recovery", i18n.T("Helyreállító kulcs"), true, i18n.T("Kapsz egy második, véletlen kulcsot is. Írd fel / fotózd le, és tedd el biztos helyre: ha a jelszót elfelejted, ezzel is megnyitható.")),
		}}
	} else {
		s.f = form{fields: []*field{newField("pw", i18n.T("A mentés jelszava"), "", i18n.T("Vagy a helyreállító kulcs."), fPassword)}}
	}
	if pw, ok := config.ReadSecret(a.d.Env.ConfigDir, "repo", s.d.Name); ok && s.exists {
		s.f.fields[0].in.SetValue(pw)
	}
	return s.f.focus()
}

func (s *passwordScreen) Update(a *App, msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case probeMsg:
		s.busy = ""
		if m.err != nil {
			if s.d.Remote() {
				delete(a.nasPasswords, s.d.Name)
			}
			return replace(errorScreen(a, i18n.T("Nem érhető el"), m.err, i18n.T("Ellenőrizd, hogy a lemez csatlakoztatva van / a NAS elérhető, és a NAS jelszava helyes.")))
		}
		s.exists = m.exists
		if !m.exists && !s.allowNew {
			return replace(newMessage(i18n.T("Nincs mentés"), []string{a.th.text.Render(i18n.T("Ezen a helyen még nincs Omarchy-mentés."))}))
		}
		return s.enterStage(a)
	case openMsg:
		s.busy = ""
		if m.err != nil {
			s.f.err = m.err.Error()
			if errors.Is(m.err, engine.ErrBadPass) {
				s.f.err = i18n.T("Hibás jelszó.")
			}
			return s.f.focus()
		}
		s.loc = m.loc
		if s.recovery != "" {
			s.stage = pwRecovery
			return nil
		}
		return tea.Sequence(pop(), s.next(m.loc, s.exists))
	case tea.KeyPressMsg:
		if m.String() == "esc" {
			return pop()
		}
		if s.stage == pwRecovery && m.String() == "enter" {
			return tea.Sequence(pop(), s.next(s.loc, s.exists))
		}
	}
	if s.busy != "" || s.stage == pwRecovery || s.stage == pwProbe {
		return nil
	}
	cmd, submit := s.f.update(msg)
	if !submit {
		return cmd
	}
	if s.stage == pwNAS {
		a.nasPasswords[s.d.Name] = s.f.raw("nas")
		return s.probe(a)
	}
	pw := s.f.raw("pw")
	if pw == "" {
		s.f.err = i18n.T("Add meg a jelszót.")
		return nil
	}
	newRepo := !s.exists && s.allowNew
	if newRepo {
		if len([]rune(pw)) < 8 {
			s.f.err = i18n.T("Legalább 8 karakter legyen.")
			return nil
		}
		if pw != s.f.raw("pw2") {
			s.f.err = i18n.T("A két jelszó nem egyezik.")
			return nil
		}
		if s.f.value("recovery") != "" {
			s.recovery = recoveryKey()
		}
	}
	s.busy = i18n.T("Megnyitás…")
	if newRepo {
		s.busy = i18n.T("Új, titkosított mentési tároló létrehozása…")
	}
	d, nas, rec := s.d, a.nasPasswords[s.d.Name], s.recovery
	return func() tea.Msg {
		loc, err := a.resolve(d, pw, nas)
		if err != nil {
			return openMsg{err: err}
		}
		if newRepo {
			if err := loc.Repo.Init(a.ctx); err != nil {
				return openMsg{err: err}
			}
			if rec != "" {
				if err := loc.Repo.KeyAdd(a.ctx, a.d.Env.RuntimeDir, rec); err != nil {
					return openMsg{err: fmt.Errorf(i18n.T("helyreállító kulcs: %w"), err)}
				}
			}
			return openMsg{loc: loc}
		}
		if _, err := loc.Repo.Snapshots(a.ctx); err != nil {
			return openMsg{err: err}
		}
		return openMsg{loc: loc}
	}
}

func (s *passwordScreen) View(a *App, w, h int) string {
	th := a.th
	if s.stage == pwRecovery {
		lines := []string{"", th.h1.Render(i18n.T("  A helyreállító kulcsod:")), "",
			"      " + th.title.Render(s.recovery), "",
			th.text.Render(i18n.T("  Írd fel papírra vagy fotózd le, és tedd el biztos helyre.")),
			th.text.Render(i18n.T("  Ha a jelszavadat elfelejted, ezzel is megnyitható a mentés.")),
			th.warnS.Render(i18n.T("  Ez a kulcs többet nem jelenik meg.")), "",
			th.mutedS.Render(i18n.T("  Enter: felírtam, tovább"))}
		return strings.Join(lines, "\n")
	}
	out := "\n"
	switch {
	case s.stage == pwProbe || s.busy != "" && len(s.f.fields) == 0:
		return out + "  " + th.accentS.Render(s.busy)
	case s.stage == pwEnter && !s.exists && s.allowNew:
		out += th.text.Render(i18n.T("  Ezen a helyen még nincs mentés – most létrehozunk egy új, titkosítottat.")) + "\n\n"
	case s.stage == pwEnter:
		out += th.text.Render(i18n.T("  Ezen a helyen már van mentés. Add meg a jelszavát.")) + "\n\n"
	}
	out += s.f.view(a, w)
	if s.busy != "" {
		out += "\n  " + th.accentS.Render(s.busy)
	}
	return out
}

func recoveryKey() string {
	const alphabet = "BCDFGHJKLMNPQRSTVWXZ23456789" // no vowels: no accidental words
	var parts []string
	for i := 0; i < 6; i++ {
		var b strings.Builder
		for j := 0; j < 5; j++ {
			n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
			b.WriteByte(alphabet[n.Int64()])
		}
		parts = append(parts, b.String())
	}
	return strings.Join(parts, "-")
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

var _ = inventory.HumanSize
