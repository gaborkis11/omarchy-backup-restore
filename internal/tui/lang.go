package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

// langScreen lets the user pick the interface language (shown on first start
// and from Settings). It is always bilingual.
type langScreen struct {
	first bool
	list  optionList
}

func newLangScreen(first bool) *langScreen { return &langScreen{first: first} }

func (s *langScreen) Title() string { return "Language / Nyelv" }
func (s *langScreen) Help() string  { return "↑/↓  •  Enter" }
func (s *langScreen) Init(a *App) tea.Cmd {
	pick := func(l string) func() tea.Cmd {
		return func() tea.Cmd {
			i18n.Set(l)
			a.d.Config.Language = l
			_ = a.d.Config.Save()
			return pop()
		}
	}
	s.list = optionList{opts: []option{
		{label: "English", run: pick(i18n.EN)},
		{label: "Magyar", run: pick(i18n.HU)},
	}}
	if i18n.Lang() == i18n.HU {
		s.list.cur = 1
	}
	return nil
}
func (s *langScreen) Update(a *App, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "esc" && !s.first {
		return pop()
	}
	cmd, _ := s.list.update(msg)
	return cmd
}
func (s *langScreen) View(a *App, w, h int) string {
	th := a.th
	var b strings.Builder
	b.WriteString("\n")
	if s.first {
		b.WriteString(th.h1.Render("  Welcome to Omarchy Backup & Restore!") + "\n")
		b.WriteString(th.mutedS.Render("  Üdv az Omarchy Backup & Restore-ban!") + "\n\n")
	}
	b.WriteString(th.text.Render("  Choose the language of the program:") + "\n")
	b.WriteString(th.mutedS.Render("  Válaszd ki a program nyelvét:") + "\n\n")
	b.WriteString(s.list.view(a, w))
	b.WriteString("\n" + th.dimS.Render("  You can change it later in Settings. / Később a Beállításokban módosítható.") + "\n")
	return b.String()
}
