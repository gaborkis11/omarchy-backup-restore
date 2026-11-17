package tui

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

func sudoKeepAlive(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(50 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = exec.Command("sudo", "-n", "-v").Run()
			}
		}
	}()
	return cancel
}

// ───────────────────────── option list ─────────────────────────

type option struct {
	label, desc string
	disabled    bool
	run         func() tea.Cmd
}

type optionList struct {
	opts []option
	cur  int
}

func (l *optionList) move(d int) {
	if len(l.opts) == 0 {
		return
	}
	for i := 0; i < len(l.opts); i++ {
		l.cur = (l.cur + d + len(l.opts)) % len(l.opts)
		if !l.opts[l.cur].disabled {
			return
		}
	}
}

// update handles navigation; returns the command of a chosen option.
func (l *optionList) update(msg tea.Msg) (tea.Cmd, bool) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil, false
	}
	switch k.String() {
	case "up", "k", "shift+tab":
		l.move(-1)
		return nil, true
	case "down", "j", "tab":
		l.move(1)
		return nil, true
	case "enter", "space":
		if l.cur < len(l.opts) && !l.opts[l.cur].disabled && l.opts[l.cur].run != nil {
			return l.opts[l.cur].run(), true
		}
		return nil, true
	}
	if s := k.String(); len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
		i := int(s[0] - '1')
		if i < len(l.opts) && !l.opts[i].disabled {
			l.cur = i
			if l.opts[i].run != nil {
				return l.opts[i].run(), true
			}
		}
	}
	return nil, false
}

func (l *optionList) view(a *App, width int) string {
	th := a.th
	var b strings.Builder
	for i, o := range l.opts {
		num := th.dimS.Render(string(rune('1'+i)) + " ")
		if i >= 9 {
			num = "  "
		}
		label := o.label
		switch {
		case o.disabled:
			label = th.dimS.Render("  " + label)
		case i == l.cur:
			label = th.accentS.Render("› ") + th.h2.Render(label)
		default:
			label = "  " + th.text.Render(label)
		}
		b.WriteString(num + label + "\n")
		if o.desc != "" {
			for _, ln := range wrap(o.desc, width-8) {
				b.WriteString("      " + th.mutedS.Render(ln) + "\n")
			}
		}
	}
	return b.String()
}

// ───────────────────────── message / confirm screen ─────────────────────────

type messageScreen struct {
	title string
	body  []string // pre-styled lines
	list  optionList
	help  string
}

func newMessage(title string, body []string, opts ...option) *messageScreen {
	if len(opts) == 0 {
		opts = []option{{label: i18n.T("Vissza"), run: pop}}
	}
	return &messageScreen{title: title, body: body, list: optionList{opts: opts}}
}

func (s *messageScreen) Init(a *App) tea.Cmd { return nil }
func (s *messageScreen) Title() string       { return s.title }
func (s *messageScreen) Help() string {
	return i18n.T("↑/↓ választás  •  Enter  •  Esc vissza")
}
func (s *messageScreen) Update(a *App, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok && (k.String() == "esc" || k.String() == "q") {
		return pop()
	}
	cmd, _ := s.list.update(msg)
	return cmd
}
func (s *messageScreen) View(a *App, w, h int) string {
	var b strings.Builder
	b.WriteString("\n")
	for _, l := range s.body {
		b.WriteString(l + "\n")
	}
	b.WriteString("\n")
	b.WriteString(s.list.view(a, w))
	return b.String()
}

// errorScreen shows an error with a back option.
func errorScreen(a *App, title string, err error, extra ...string) *messageScreen {
	body := []string{a.th.errS.Render(i18n.T("Hiba: ")) + a.th.text.Render(errText(err))}
	for _, e := range extra {
		for _, l := range wrap(e, a.w-6) {
			body = append(body, a.th.mutedS.Render(l))
		}
	}
	return newMessage(title, body)
}

// ───────────────────────── busy screen (spinner) ─────────────────────────

type busyScreen struct {
	title, text string
	sp          spinner.Model
	run         func() tea.Msg // executed once
	done        func(msg tea.Msg) tea.Cmd
	started     bool
}

type busyDone struct{ v tea.Msg }

func newBusy(title, text string, run func() tea.Msg, done func(tea.Msg) tea.Cmd) *busyScreen {
	return &busyScreen{title: title, text: text, run: run, done: done, sp: spinner.New(spinner.WithSpinner(spinner.MiniDot))}
}

func (s *busyScreen) Init(a *App) tea.Cmd {
	if s.started {
		return nil
	}
	s.started = true
	s.sp.Style = a.th.accentS
	run := s.run
	return tea.Batch(s.sp.Tick, func() tea.Msg { return busyDone{run()} })
}
func (s *busyScreen) Title() string { return s.title }
func (s *busyScreen) Help() string  { return i18n.T("Kis türelmet…") }
func (s *busyScreen) Update(a *App, msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case busyDone:
		return s.done(m.v)
	case spinner.TickMsg:
		var cmd tea.Cmd
		s.sp, cmd = s.sp.Update(m)
		return cmd
	}
	return nil
}
func (s *busyScreen) View(a *App, w, h int) string {
	return "\n  " + s.sp.View() + " " + a.th.text.Render(s.text)
}

// ───────────────────────── form ─────────────────────────

type fieldKind int

const (
	fText fieldKind = iota
	fPassword
	fChoice
	fToggle
)

type field struct {
	key, label, hint string
	kind             fieldKind
	in               textinput.Model
	choices          []string
	choice           int
	on               bool
}

type form struct {
	fields []*field
	cur    int
	err    string
}

func newField(key, label, value, hint string, kind fieldKind) *field {
	f := &field{key: key, label: label, hint: hint, kind: kind}
	if kind == fText || kind == fPassword {
		ti := textinput.New()
		ti.Prompt = ""
		ti.SetValue(value)
		ti.CharLimit = 256
		ti.SetWidth(44)
		if kind == fPassword {
			ti.EchoMode = textinput.EchoPassword
			ti.EchoCharacter = '•'
		}
		f.in = ti
	}
	return f
}

func choiceField(key, label string, choices []string, sel int, hint string) *field {
	return &field{key: key, label: label, kind: fChoice, choices: choices, choice: sel, hint: hint}
}

func toggleField(key, label string, on bool, hint string) *field {
	return &field{key: key, label: label, kind: fToggle, on: on, hint: hint}
}

func (f *form) value(key string) string {
	for _, fl := range f.fields {
		if fl.key == key {
			switch fl.kind {
			case fChoice:
				return fl.choices[fl.choice]
			case fToggle:
				if fl.on {
					return "1"
				}
				return ""
			default:
				return strings.TrimSpace(fl.in.Value())
			}
		}
	}
	return ""
}

func (f *form) raw(key string) string {
	for _, fl := range f.fields {
		if fl.key == key && (fl.kind == fText || fl.kind == fPassword) {
			return fl.in.Value()
		}
	}
	return ""
}

func (f *form) focus() tea.Cmd {
	var cmd tea.Cmd
	for i, fl := range f.fields {
		if fl.kind == fText || fl.kind == fPassword {
			if i == f.cur {
				cmd = fl.in.Focus()
			} else {
				fl.in.Blur()
			}
		}
	}
	return cmd
}

// update returns submit=true when Enter is pressed on the last field.
func (f *form) update(msg tea.Msg) (cmd tea.Cmd, submit bool) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		cur := f.fields[f.cur]
		switch k.String() {
		case "tab", "down":
			f.cur = (f.cur + 1) % len(f.fields)
			return f.focus(), false
		case "shift+tab", "up":
			f.cur = (f.cur - 1 + len(f.fields)) % len(f.fields)
			return f.focus(), false
		case "enter":
			if f.cur == len(f.fields)-1 {
				return nil, true
			}
			f.cur++
			return f.focus(), false
		case "left", "right", "space":
			if cur.kind == fChoice {
				d := 1
				if k.String() == "left" {
					d = -1
				}
				cur.choice = (cur.choice + d + len(cur.choices)) % len(cur.choices)
				return nil, false
			}
			if cur.kind == fToggle {
				cur.on = !cur.on
				return nil, false
			}
		}
	}
	cur := f.fields[f.cur]
	if cur.kind == fText || cur.kind == fPassword {
		var c tea.Cmd
		cur.in, c = cur.in.Update(msg)
		return c, false
	}
	return nil, false
}

func (f *form) view(a *App, width int) string {
	th := a.th
	var b strings.Builder
	for i, fl := range f.fields {
		marker := "  "
		label := th.mutedS.Render(pad(fl.label, 22))
		if i == f.cur {
			marker = th.accentS.Render("› ")
			label = th.h2.Render(pad(fl.label, 22))
		}
		val := ""
		switch fl.kind {
		case fText, fPassword:
			val = fl.in.View()
		case fChoice:
			var parts []string
			for j, c := range fl.choices {
				if j == fl.choice {
					parts = append(parts, th.selS.Render(" "+c+" "))
				} else {
					parts = append(parts, th.mutedS.Render(" "+c+" "))
				}
			}
			val = strings.Join(parts, " ")
		case fToggle:
			if fl.on {
				val = th.okS.Render(i18n.T("[x] igen"))
			} else {
				val = th.mutedS.Render(i18n.T("[ ] nem"))
			}
		}
		b.WriteString(marker + label + " " + val + "\n")
		if i == f.cur && fl.hint != "" {
			for _, l := range wrap(fl.hint, width-28) {
				b.WriteString(strings.Repeat(" ", 25) + th.dimS.Render(l) + "\n")
			}
		}
	}
	if f.err != "" {
		b.WriteString("\n" + th.errS.Render("  "+f.err) + "\n")
	}
	return b.String()
}
