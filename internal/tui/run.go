package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/gaborkis11/omarchy-backup-restore/internal/config"
	"github.com/gaborkis11/omarchy-backup-restore/internal/engine"
	"github.com/gaborkis11/omarchy-backup-restore/internal/version"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

// Screen is one page of the interface. Screens are pointers and mutate in place.
type Screen interface {
	Init(a *App) tea.Cmd
	Update(a *App, msg tea.Msg) tea.Cmd
	View(a *App, width, height int) string
	Title() string
	Help() string
}

// App is the root Bubble Tea model.
type App struct {
	d       Deps
	ctx     context.Context
	cancel  context.CancelFunc
	th      theme
	w, h    int
	stack   []Screen
	busy    bool // a worker is running (ctrl+c asks first)
	askQuit bool
	// session secrets (never written to disk unless the user asks for scheduling)
	nasPasswords map[string]string
	stopSudo     func()
}

type pushMsg struct{ s Screen }
type popMsg struct{ n int }
type replaceMsg struct{ s Screen }
type homeMsg struct{}

func push(s Screen) tea.Cmd    { return func() tea.Msg { return pushMsg{s} } }
func pop() tea.Cmd             { return func() tea.Msg { return popMsg{1} } }
func replace(s Screen) tea.Cmd { return func() tea.Msg { return replaceMsg{s} } }
func goHome() tea.Cmd          { return func() tea.Msg { return homeMsg{} } }

// Run starts the interface.
func Run(ctx context.Context, d Deps) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a := &App{d: d, ctx: ctx, cancel: cancel, th: loadTheme(d.Env.Home), w: 100, h: 30, nasPasswords: map[string]string{}}
	defer func() {
		if a.stopSudo != nil {
			a.stopSudo()
		}
	}()
	var first Screen = newMenu()
	a.stack = []Screen{first}
	switch d.Mode {
	case "backup":
		// one click: go straight to the usual destination when it is available;
		// Esc on the selection screen leads back to the destination list.
		a.stack = append(a.stack, newDestPicker(purposeBackup))
		if dest, ok := a.defaultDestination(); ok {
			a.stack = append(a.stack, newScanScreen(dest))
		}
	case "restore":
		switch {
		case d.Source != "":
			a.stack = append(a.stack, newRestoreFromPath(d.Source))
		default:
			a.stack = append(a.stack, newDestPicker(purposeRestore))
			if p, ok := a.singleBackupDrive(); ok {
				a.stack = append(a.stack, newRestoreFromPath(p))
			}
		}
	}
	if d.Config.Language == "" && i18n.Pick("") == i18n.EN && os.Getenv("OBR_LANG") == "" {
		// first start: ask for the language before anything else
		a.stack = append(a.stack, newLangScreen(true))
	}
	p := tea.NewProgram(a, tea.WithContext(ctx))
	_, err := p.Run()
	if err != nil && strings.Contains(err.Error(), "context canceled") {
		return nil
	}
	return err
}

func (a *App) top() Screen { return a.stack[len(a.stack)-1] }

// Init implements tea.Model.
func (a *App) Init() tea.Cmd {
	// only the visible screen starts; the others start when they come to the top
	return a.top().Init(a)
}

// Update implements tea.Model.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		a.w, a.h = m.Width, m.Height
	case tea.KeyPressMsg:
		if m.String() == "ctrl+c" {
			if a.busy && !a.askQuit {
				a.askQuit = true
				return a, nil
			}
			a.cancel()
			return a, tea.Quit
		}
		if a.askQuit {
			a.askQuit = false
			if m.String() == "i" || m.String() == "y" {
				a.cancel()
				return a, tea.Quit
			}
			return a, nil
		}
	case pushMsg:
		a.stack = append(a.stack, m.s)
		return a, m.s.Init(a)
	case popMsg:
		n := m.n
		if n >= len(a.stack) {
			return a, tea.Quit
		}
		a.stack = a.stack[:len(a.stack)-n]
		return a, a.top().Init(a)
	case replaceMsg:
		a.stack[len(a.stack)-1] = m.s
		return a, m.s.Init(a)
	case homeMsg:
		a.stack = a.stack[:1]
		return a, a.top().Init(a)
	case quitMsg:
		return a, tea.Quit
	}
	return a, a.top().Update(a, msg)
}

type quitMsg struct{}

func quit() tea.Cmd { return func() tea.Msg { return quitMsg{} } }

// View implements tea.Model.
func (a *App) View() tea.View {
	th := a.th
	w := a.w
	if w > 140 {
		w = 140
	}
	s := a.top()
	head := th.title.Render(" Omarchy Backup & Restore ") + th.dimS.Render(" "+version.Version)
	if t := s.Title(); t != "" {
		head += th.mutedS.Render("  ›  ") + th.h2.Render(t)
	}
	help := s.Help()
	if a.askQuit {
		help = th.warnS.Render(i18n.T("Fut egy művelet. Biztosan kilépsz? (i = igen, bármi más = mégse)"))
	}
	foot := th.dimS.Render(help)
	bodyH := a.h - 4
	if bodyH < 5 {
		bodyH = 5
	}
	body := s.View(a, w-2, bodyH)
	lines := strings.Split(body, "\n")
	if len(lines) > bodyH {
		lines = lines[:bodyH]
	}
	for i, l := range lines {
		if lipgloss.Width(l) > w {
			lines[i] = ansiTruncate(l, w)
		}
	}
	for len(lines) < bodyH {
		lines = append(lines, "")
	}
	content := head + "\n" + th.dimS.Render(strings.Repeat("─", w)) + "\n" + strings.Join(lines, "\n") + "\n" + foot
	v := tea.NewView(lipgloss.NewStyle().PaddingLeft(1).Render(content))
	v.AltScreen = true
	v.WindowTitle = "Omarchy Backup & Restore"
	return v
}

// helpLine formats key hints.
func (a *App) helpLine(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, a.th.key.Render(pairs[i])+" "+pairs[i+1])
	}
	return strings.Join(parts, "  •  ")
}

// ensureSudo returns a command that makes sure sudo credentials are cached,
// asking for the password in the terminal (not inside obr) when needed.
func (a *App) ensureSudo(next func(ok bool) tea.Msg) tea.Cmd {
	if exec.Command("sudo", "-n", "true").Run() == nil {
		a.startSudoKeepAlive()
		return func() tea.Msg { return next(true) }
	}
	c := exec.Command("sudo", "-v", "-p", i18n.T("\n  Rendszergazdai jog kell az obr-nek. A sudo kéri a jelszavadat: "))
	return tea.ExecProcess(c, func(err error) tea.Msg {
		ok := err == nil && exec.Command("sudo", "-n", "true").Run() == nil
		if ok {
			a.startSudoKeepAlive()
		}
		return next(ok)
	})
}

func (a *App) startSudoKeepAlive() {
	if a.stopSudo != nil {
		return
	}
	a.stopSudo = sudoKeepAlive(a.ctx)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprint(err)
}

// defaultDestination returns the last used (or only) destination when it can
// be used right now (removable drives must be connected).
func (a *App) defaultDestination() (config.Destination, bool) {
	cfg := a.d.Config
	var d config.Destination
	var ok bool
	if cfg.LastDestination != "" {
		d, ok = cfg.Find(cfg.LastDestination)
	}
	if !ok && len(cfg.Destinations) == 1 {
		d, ok = cfg.Destinations[0], true
	}
	if !ok {
		return d, false
	}
	if d.Type == config.Local {
		if d.UUID != "" {
			mp, err := engine.MountPointByUUID(a.ctx, a.d.Env.Runner, d.UUID)
			if err != nil || mp == "" {
				return d, false
			}
		} else if !isDir(d.Path) {
			return d, false
		}
	}
	return d, true
}

// singleBackupDrive returns the mount point when exactly one connected drive
// holds a backup and no network destination is configured.
func (a *App) singleBackupDrive() (string, bool) {
	for _, d := range a.d.Config.Destinations {
		if d.Remote() {
			return "", false
		}
	}
	drives, err := engine.ListDrives(a.ctx, a.d.Env.Runner)
	if err != nil {
		return "", false
	}
	found := ""
	for _, dr := range drives {
		if dr.HasBackup {
			if found != "" {
				return "", false
			}
			found = dr.Mountpoint
		}
	}
	return found, found != ""
}
