package tui

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/gaborkis11/omarchy-backup-restore/internal/config"
	"github.com/gaborkis11/omarchy-backup-restore/internal/engine"
	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

// localBrowser lets the user pick a folder on this machine (a mounted drive,
// /mnt, the home…). For restores only folders that hold a backup can be chosen.
type localBrowser struct {
	p      purpose
	dir    string
	dirs   []string
	marks  map[string]bool // sub folders that hold a backup
	backup bool
	cur    int
	err    string
	newDir *form
}

func newLocalBrowser(p purpose, a *App) *localBrowser {
	start := a.d.Env.Home
	if u, err := user.Current(); err == nil {
		if m := filepath.Join("/run/media", u.Username); isDir(m) {
			start = m
		}
	}
	s := &localBrowser{p: p}
	s.open(start)
	return s
}

func hasBackup(dir string) bool { return isDir(filepath.Join(dir, engine.BaseDir, "repo")) }

func (s *localBrowser) open(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		s.err = err.Error()
		return
	}
	s.dir, s.err, s.cur = dir, "", 0
	s.dirs, s.marks = nil, map[string]bool{}
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, ".") || n == engine.BaseDir {
			continue
		}
		full := filepath.Join(dir, n)
		if !isDir(full) {
			continue
		}
		s.dirs = append(s.dirs, n)
		if hasBackup(full) {
			s.marks[n] = true
		}
	}
	sort.Slice(s.dirs, func(i, j int) bool { return strings.ToLower(s.dirs[i]) < strings.ToLower(s.dirs[j]) })
	s.backup = hasBackup(dir)
}

func (s *localBrowser) rows() []string {
	var r []string
	if !s.p.restoring() || s.backup {
		r = append(r, "choose")
	}
	if s.dir != "/" {
		r = append(r, "up")
	}
	for _, d := range s.dirs {
		r = append(r, "dir:"+d)
	}
	return r
}

func (s *localBrowser) Title() string {
	if s.p.restoring() {
		return i18n.T("Hol van a mentés?")
	}
	return i18n.T("Hova mentsen?")
}
func (s *localBrowser) Help() string {
	if s.newDir != nil {
		return i18n.T("Enter létrehozás  •  Esc mégse")
	}
	return i18n.T("↑/↓  •  Enter megnyit / választ  •  ←/Backspace fel  •  n új mappa  •  Esc vissza")
}
func (s *localBrowser) Init(a *App) tea.Cmd { return nil }

func (s *localBrowser) choose(a *App) tea.Cmd {
	d := config.Destination{Name: filepath.Base(s.dir), Type: config.Local, Path: s.dir}
	if out, err := a.d.Env.Out(a.ctx, "findmnt", "-n", "-o", "UUID,LABEL,TARGET", "--target", s.dir); err == nil {
		f := strings.Fields(out)
		if len(f) >= 3 && f[len(f)-1] != "/" && f[len(f)-1] != "/home" {
			d.UUID = f[0]
			d.Label = f[1]
			if s.dir == f[len(f)-1] && d.Label != "" {
				d.Name = d.Label
			}
		}
	}
	a.d.Config.Upsert(d)
	_ = a.d.Config.Save()
	return tea.Sequence(pop(), nextAfterDest(a, s.p, d))
}

func (s *localBrowser) Update(a *App, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		if s.newDir != nil {
			cmd, _ := s.newDir.update(msg)
			return cmd
		}
		return nil
	}
	if s.newDir != nil {
		if k.String() == "esc" {
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
		target := filepath.Join(s.dir, name)
		if err := os.MkdirAll(target, 0o755); err != nil {
			s.newDir.err = err.Error()
			return nil
		}
		s.newDir = nil
		s.open(target)
		return nil
	}
	rows := s.rows()
	switch k.String() {
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
		if s.dir != "/" {
			s.open(filepath.Dir(s.dir))
		}
	case "n":
		if s.p.restoring() {
			return nil
		}
		s.newDir = &form{fields: []*field{newField("name", i18n.T("Új mappa neve"), "", "", fText)}}
		return s.newDir.focus()
	case "enter", "right", "l", "space":
		if s.cur >= len(rows) {
			return nil
		}
		switch r := rows[s.cur]; {
		case r == "choose":
			if k.String() == "enter" || k.String() == "space" {
				return s.choose(a)
			}
		case r == "up":
			s.open(filepath.Dir(s.dir))
		default:
			s.open(filepath.Join(s.dir, strings.TrimPrefix(r, "dir:")))
		}
	}
	return nil
}

func (s *localBrowser) View(a *App, w, h int) string {
	th := a.th
	var b strings.Builder
	b.WriteString("\n  " + th.h2.Render(i18n.T("Hely: ")+s.dir) + "\n")
	switch {
	case s.backup && s.p.restoring():
		b.WriteString("  " + th.okS.Render(i18n.T("Ebben a mappában van Omarchy-mentés.")) + "\n")
	case s.backup:
		b.WriteString("  " + th.okS.Render(i18n.T("Ebben a mappában már van Omarchy-mentés.")) + "\n")
	case s.p.restoring():
		b.WriteString("  " + th.mutedS.Render(i18n.T("Lépj be abba a mappába, amelyben a mentés van (a ★ jelölt mappákban van mentés).")) + "\n")
	default:
		b.WriteString("  " + th.mutedS.Render(i18n.T("Lépj be egy mappába, vagy válaszd ezt; n = új mappa.")) + "\n")
	}
	b.WriteString("\n")
	if s.newDir != nil {
		b.WriteString(s.newDir.view(a, w))
		return b.String()
	}
	rows := s.rows()
	room := h - 7
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
			label = th.okS.Render("✓ " + i18n.T("Ezt a mentést nyisd meg: ") + s.dir)
		case r == "choose":
			label = th.okS.Render("✓ " + i18n.T("Ide mentsen: ") + s.dir)
		case r == "up":
			label = th.mutedS.Render("‹ " + i18n.T("Vissza egy szinttel"))
		default:
			n := strings.TrimPrefix(r, "dir:")
			label = th.text.Render("▸ " + n)
			if s.marks[n] {
				label += th.okS.Render(fmt.Sprintf("  ★ %s", i18n.T("van benne mentés")))
			}
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
