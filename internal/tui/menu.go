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
		option{label: i18n.T("Mappa megadása"), desc: i18n.T("Bármilyen csatolt mappa (pl. /mnt/backup)."),
			run: func() tea.Cmd { return push(newPathForm(s.p)) }},
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

// ───────────────────────── path form ─────────────────────────

type pathForm struct {
	p purpose
	f form
}

func newPathForm(p purpose) *pathForm {
	return &pathForm{p: p, f: form{fields: []*field{
		newField("path", i18n.T("Mappa"), "", i18n.T("A mappába egy omarchy-backup almappa kerül."), fText),
		newField("name", i18n.T("Név"), "", i18n.T("Ezen a néven jelenik meg legközelebb."), fText),
	}}}
}
func (s *pathForm) Init(a *App) tea.Cmd { return s.f.focus() }
func (s *pathForm) Title() string       { return i18n.T("Mappa megadása") }
func (s *pathForm) Help() string {
	return i18n.T("Tab mezőváltás  •  Enter tovább  •  Esc vissza")
}
func (s *pathForm) Update(a *App, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "esc" {
		return pop()
	}
	cmd, submit := s.f.update(msg)
	if !submit {
		return cmd
	}
	p := s.f.value("path")
	if strings.HasPrefix(p, "~/") {
		p = filepath.Join(a.d.Env.Home, p[2:])
	}
	if p == "" || !isDir(p) {
		s.f.err = i18n.T("Nem létező mappa.")
		return nil
	}
	name := s.f.value("name")
	if name == "" {
		name = filepath.Base(p)
	}
	d := config.Destination{Name: name, Type: config.Local, Path: p}
	a.d.Config.Upsert(d)
	_ = a.d.Config.Save()
	return tea.Sequence(pop(), nextAfterDest(a, s.p, d))
}
func (s *pathForm) View(a *App, w, h int) string { return "\n" + s.f.view(a, w) }

// ───────────────────────── NAS form ─────────────────────────

type nasForm struct {
	p    purpose
	f    form
	busy bool
}

type nasTestMsg struct {
	d   config.Destination
	pw  string
	err error
}

func newNASForm(p purpose) *nasForm {
	return &nasForm{p: p, f: form{fields: []*field{
		newField("name", i18n.T("Név"), "Synology", "", fText),
		newField("host", i18n.T("NAS címe"), "", i18n.T("IP-cím vagy név, pl. 192.168.1.10 vagy NAS.local."), fText),
		choiceField("type", i18n.T("Kapcsolat"), []string{"SFTP", "SMB"}, 0,
			i18n.T("SFTP: DSM → Vezérlőpult → Fájlszolgáltatások → FTP → SFTP bekapcsolása. SMB: Windows-megosztás, mindig be van kapcsolva.")),
		newField("share", i18n.T("Megosztott mappa"), "", i18n.T("A Synology megosztott mappa neve (pl. backup vagy homes)."), fText),
		newField("folder", i18n.T("Almappa"), "", i18n.T("Nem kötelező, pl. omarchy."), fText),
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
		a.d.Config.Upsert(m.d)
		_ = a.d.Config.Save()
		a.nasPasswords[m.d.Name] = m.pw
		return tea.Sequence(pop(), nextAfterDest(a, s.p, m.d))
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
	d := config.Destination{Name: s.f.value("name"), Type: strings.ToLower(s.f.value("type")), Host: s.f.value("host"),
		Share: strings.Trim(s.f.value("share"), "/"), Folder: strings.Trim(s.f.value("folder"), "/"), User: s.f.value("user")}
	pw := s.f.raw("pass")
	if d.Name == "" || d.Host == "" || d.Share == "" || d.User == "" || pw == "" {
		s.f.err = i18n.T("Minden mező kell (az almappa kivételével).")
		return nil
	}
	s.busy = true
	s.f.err = ""
	return func() tea.Msg {
		if d.Type == config.SFTP {
			if err := LearnHostKey(a.ctx, a.d.Env, &d); err != nil {
				return nasTestMsg{d, pw, fmt.Errorf(i18n.T("az SFTP nem érhető el (be van kapcsolva a DSM-ben?): %v"), err)}
			}
		}
		loc, err := a.resolve(d, "__probe__", pw)
		if err != nil {
			return nasTestMsg{d, pw, err}
		}
		if _, err := loc.RemoteListing(a.ctx, ""); err != nil && !strings.Contains(err.Error(), "directory not found") {
			if _, err2 := loc.Repo.Exists(a.ctx); err2 != nil && !errors.Is(err2, engine.ErrBadPass) {
				return nasTestMsg{d, pw, err2}
			}
		}
		return nasTestMsg{d, pw, nil}
	}
}
func (s *nasForm) View(a *App, w, h int) string {
	out := "\n" + s.f.view(a, w)
	if s.busy {
		out += "\n  " + a.th.accentS.Render(i18n.T("Kapcsolódás a NAS-hoz…"))
	}
	return out
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
