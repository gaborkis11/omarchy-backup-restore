package tui

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/gaborkis11/omarchy-backup-restore/internal/config"
	"github.com/gaborkis11/omarchy-backup-restore/internal/engine"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

// ───────────────────────── schedule ─────────────────────────

type scheduleScreen struct{ list optionList }

func newScheduleScreen() *scheduleScreen { return &scheduleScreen{} }

func (s *scheduleScreen) Title() string { return i18n.T("Ütemezett mentés") }
func (s *scheduleScreen) Help() string  { return i18n.T("↑/↓  •  Enter  •  Esc vissza") }
func (s *scheduleScreen) Init(a *App) tea.Cmd {
	sc := a.d.Config.Schedule
	opts := []option{}
	if sc.Enabled {
		opts = append(opts, option{label: i18n.T("Mentés most (az ütemezett beállításokkal)"), desc: i18n.T("Elindítja a háttérmentést, a végén értesítést kapsz."),
			run: func() tea.Cmd {
				_ = exec.Command("systemctl", "--user", "start", "--no-block", "obr-backup.service").Run()
				return push(newMessage(i18n.T("Elindítva"), []string{a.th.text.Render(i18n.T("  A mentés a háttérben fut; a végén értesítést kapsz."))}))
			}},
			option{label: i18n.T("Kikapcsolás"), desc: i18n.T("Leállítja az időzítőt és törli a tárolt jelszavakat."), run: func() tea.Cmd {
				_ = exec.Command("systemctl", "--user", "disable", "--now", "obr-backup.timer").Run()
				config.DeleteSecret(a.d.Env.ConfigDir, "repo", sc.Destination)
				config.DeleteSecret(a.d.Env.ConfigDir, "nas", sc.Destination)
				a.d.Config.Schedule = config.Schedule{}
				_ = a.d.Config.Save()
				return s.Init(a)
			}})
	} else {
		opts = append(opts, option{label: i18n.T("Bekapcsolás"), desc: i18n.T("Kiválasztod a célhelyet és a gyakoriságot."),
			run: func() tea.Cmd { return push(newDestPicker(purposeSchedule)) }})
	}
	opts = append(opts, option{label: i18n.T("Vissza"), run: pop})
	s.list = optionList{opts: opts}
	return nil
}
func (s *scheduleScreen) Update(a *App, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "esc" {
		return pop()
	}
	cmd, _ := s.list.update(msg)
	return cmd
}
func (s *scheduleScreen) View(a *App, w, h int) string {
	th := a.th
	sc := a.d.Config.Schedule
	out := "\n"
	if sc.Enabled {
		out += th.okS.Render(i18n.T("  Bekapcsolva: ")) + th.text.Render(sc.OnCalendar+" → "+sc.Destination) + "\n"
		if t, err := exec.Command("systemctl", "--user", "show", "obr-backup.timer", "-p", "NextElapseUSecRealtime", "--value").Output(); err == nil && strings.TrimSpace(string(t)) != "" {
			out += th.mutedS.Render(i18n.T("  Következő futás: ")+strings.TrimSpace(string(t))) + "\n"
		}
	} else {
		out += th.mutedS.Render(i18n.T("  Kikapcsolva.")) + "\n"
	}
	return out + "\n" + s.list.view(a, w)
}

type scheduleForm struct {
	d    config.Destination
	f    form
	busy bool
}

type scheduleSaved struct{ err error }

func newScheduleForm(d config.Destination) *scheduleForm {
	fields := []*field{
		choiceField("when", i18n.T("Gyakoriság"), []string{i18n.T("naponta 20:00"), i18n.T("hetente (vasárnap 20:00)"), i18n.T("egyéni")}, 0, ""),
		newField("cal", i18n.T("Egyéni időzítés"), "", i18n.T("systemd OnCalendar formátum, pl. \"*-*-* 12:30\" vagy \"Mon,Thu 19:00\". Csak „egyéni” esetén."), fText),
		newField("pw", i18n.T("A mentés jelszava"), "", i18n.T("Ha még nincs mentés ezen a helyen, ezzel jön létre."), fPassword),
	}
	if d.Remote() {
		fields = append(fields, newField("nas", i18n.T("A NAS jelszava"), "", "", fPassword))
	}
	fields = append(fields, toggleField("ok", i18n.T("Tárolás elfogadva"), false, ScheduleWarning()))
	return &scheduleForm{d: d, f: form{fields: fields}}
}

func (s *scheduleForm) Title() string { return i18n.T("Ütemezett mentés – ") + s.d.Name }
func (s *scheduleForm) Help() string {
	return i18n.T("Tab mezőváltás  •  ←/→ választás  •  Space kapcsol  •  Enter az utolsón: mentés  •  Esc")
}
func (s *scheduleForm) Init(a *App) tea.Cmd {
	if pw := a.nasPasswords[s.d.Name]; pw != "" {
		for _, f := range s.f.fields {
			if f.key == "nas" {
				f.in.SetValue(pw)
			}
		}
	}
	return s.f.focus()
}
func (s *scheduleForm) Update(a *App, msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case scheduleSaved:
		s.busy = false
		if m.err != nil {
			s.f.err = m.err.Error()
			return nil
		}
		return tea.Sequence(goHome(), push(newScheduleScreen()))
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
	if s.f.value("ok") == "" {
		s.f.err = i18n.T("A jelszó tárolását el kell fogadni (Space az utolsó mezőn).")
		return nil
	}
	when := map[string]string{i18n.T("naponta 20:00"): "daily", i18n.T("hetente (vasárnap 20:00)"): "weekly"}[s.f.value("when")]
	if when == "" {
		when = s.f.value("cal")
		if when == "" {
			s.f.err = i18n.T("Add meg az egyéni időzítést.")
			return nil
		}
	}
	pw, nas := s.f.raw("pw"), s.f.raw("nas")
	if len([]rune(pw)) < 8 {
		s.f.err = i18n.T("A jelszó legalább 8 karakter.")
		return nil
	}
	s.busy = true
	d := s.d
	return func() tea.Msg {
		loc, err := a.resolve(d, pw, nas)
		if err != nil {
			return scheduleSaved{err}
		}
		ok, err := loc.Repo.Exists(a.ctx)
		if err != nil {
			return scheduleSaved{err}
		}
		if !ok {
			if err := loc.Repo.Init(a.ctx); err != nil {
				return scheduleSaved{err}
			}
		}
		if err := config.WriteSecret(a.d.Env.ConfigDir, "repo", d.Name, pw); err != nil {
			return scheduleSaved{err}
		}
		if d.Remote() {
			if err := config.WriteSecret(a.d.Env.ConfigDir, "nas", d.Name, nas); err != nil {
				return scheduleSaved{err}
			}
		}
		if a.d.EnableSchedule == nil {
			return scheduleSaved{fmt.Errorf("%s", i18n.T("ütemezés nem elérhető"))}
		}
		return scheduleSaved{a.d.EnableSchedule(d.Name, when)}
	}
}
func (s *scheduleForm) View(a *App, w, h int) string {
	out := "\n" + s.f.view(a, w)
	if s.busy {
		out += "\n  " + a.th.accentS.Render(i18n.T("Beállítás…"))
	}
	return out
}

// ───────────────────────── settings ─────────────────────────

type settingsScreen struct {
	f    form
	list optionList
	msg  string
}

func newSettingsScreen() *settingsScreen { return &settingsScreen{} }

func (s *settingsScreen) Title() string { return i18n.T("Beállítások") }
func (s *settingsScreen) Help() string {
	return i18n.T("Tab mezőváltás  •  Space kapcsol  •  Enter az utolsón: mentés  •  Esc vissza (mentés nélkül)")
}
func (s *settingsScreen) Init(a *App) tea.Cmd {
	r := a.d.Config.Retention
	num := func(n int) string {
		if n == 0 {
			return ""
		}
		return strconv.Itoa(n)
	}
	langIdx := 0
	if i18n.Lang() == i18n.HU {
		langIdx = 1
	}
	fields := []*field{
		choiceField("lang", "Language / Nyelv", []string{"English", "Magyar"}, langIdx, ""),
		newField("daily", i18n.T("Napi mentések"), num(r.KeepDaily), i18n.T("Ennyi napra visszamenőleg naponta egy mentés marad. Üres = mind marad."), fText),
		newField("weekly", i18n.T("Heti mentések"), num(r.KeepWeekly), i18n.T("Ennyi hétre visszamenőleg hetente egy."), fText),
		newField("monthly", i18n.T("Havi mentések"), num(r.KeepMonthly), i18n.T("Ennyi hónapra visszamenőleg havonta egy."), fText),
		toggleField("consistent", i18n.T("Konzisztens mentés"), a.d.Config.ConsistentSnapshot,
			i18n.T("Mindig btrfs pillanatképből mentsen (futó programok mellett is ép adatbázisok). Rendszergazdai jogot kér.")),
	}
	for _, d := range a.d.Config.Destinations {
		fields = append(fields, toggleField("del:"+d.Name, i18n.T("Törlés: ")+d.Name, false, d.Describe()+i18n.T(" – csak a beállítást törli, a mentést nem.")))
	}
	s.f = form{fields: fields}
	return s.f.focus()
}
func (s *settingsScreen) Update(a *App, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "esc" {
		return pop()
	}
	cmd, submit := s.f.update(msg)
	if !submit {
		return cmd
	}
	atoi := func(k string) (int, bool) {
		v := s.f.value(k)
		if v == "" {
			return 0, true
		}
		n, err := strconv.Atoi(v)
		return n, err == nil && n >= 0
	}
	var ok1, ok2, ok3 bool
	r := config.Retention{}
	r.KeepDaily, ok1 = atoi("daily")
	r.KeepWeekly, ok2 = atoi("weekly")
	r.KeepMonthly, ok3 = atoi("monthly")
	if !ok1 || !ok2 || !ok3 {
		s.f.err = i18n.T("Számot adj meg (vagy hagyd üresen).")
		return nil
	}
	a.d.Config.Retention = r
	if s.f.value("lang") == "Magyar" {
		a.d.Config.Language = i18n.HU
	} else {
		a.d.Config.Language = i18n.EN
	}
	i18n.Set(a.d.Config.Language)
	a.d.Config.ConsistentSnapshot = s.f.value("consistent") != ""
	for _, d := range append([]config.Destination{}, a.d.Config.Destinations...) {
		if s.f.value("del:"+d.Name) != "" {
			a.d.Config.Remove(d.Name)
			config.DeleteSecret(a.d.Env.ConfigDir, "repo", d.Name)
			config.DeleteSecret(a.d.Env.ConfigDir, "nas", d.Name)
		}
	}
	if err := a.d.Config.Save(); err != nil {
		s.f.err = err.Error()
		return nil
	}
	return pop()
}
func (s *settingsScreen) View(a *App, w, h int) string {
	return "\n" + s.f.view(a, w)
}

var _ = engine.BaseDir
