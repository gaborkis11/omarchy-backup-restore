package tui

import (
	"image/color"
	"os"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/BurntSushi/toml"
	"github.com/charmbracelet/x/ansi"
)

// theme follows the current Omarchy theme (~/.local/state/omarchy/current/theme/colors.toml).
type theme struct {
	accent, fg, muted, dim, red, yellow, green, selBg color.Color

	title, h1, h2, text, mutedS, dimS, accentS, okS, warnS, errS, selS, box, key, badgeNew, badgeChg lipgloss.Style
}

func loadTheme(home string) theme {
	c := map[string]string{
		"accent": "#ff9f0a", "foreground": "#e6e6e6", "muted": "#8e8e93", "dark_foreground": "#6e6e73",
		"red": "#ff5f4a", "yellow": "#ffc27a", "green": "#7ee787", "selection": "#3a3a3c",
	}
	p := filepath.Join(home, ".local/state/omarchy/current/theme/colors.toml")
	var raw map[string]any
	if _, err := toml.DecodeFile(p, &raw); err == nil {
		for k, v := range raw {
			if s, ok := v.(string); ok && strings.HasPrefix(s, "#") {
				c[k] = s
			}
		}
	}
	if os.Getenv("NO_COLOR") != "" {
		for k := range c {
			c[k] = ""
		}
	}
	col := func(k string) color.Color {
		if c[k] == "" {
			return lipgloss.NoColor{}
		}
		return lipgloss.Color(c[k])
	}
	t := theme{accent: col("accent"), fg: col("foreground"), muted: col("muted"), dim: col("dark_foreground"),
		red: col("red"), yellow: col("yellow"), green: col("green"), selBg: col("selection")}
	if c["green"] == c["accent"] {
		t.green = col("bright_green")
		if c["bright_green"] == "" {
			t.green = t.accent
		}
	}
	t.title = lipgloss.NewStyle().Foreground(t.accent).Bold(true)
	t.h1 = lipgloss.NewStyle().Foreground(t.accent).Bold(true)
	t.h2 = lipgloss.NewStyle().Foreground(t.fg).Bold(true)
	t.text = lipgloss.NewStyle().Foreground(t.fg)
	t.mutedS = lipgloss.NewStyle().Foreground(t.muted)
	t.dimS = lipgloss.NewStyle().Foreground(t.dim)
	t.accentS = lipgloss.NewStyle().Foreground(t.accent)
	t.okS = lipgloss.NewStyle().Foreground(t.green)
	t.warnS = lipgloss.NewStyle().Foreground(t.yellow)
	t.errS = lipgloss.NewStyle().Foreground(t.red).Bold(true)
	t.selS = lipgloss.NewStyle().Background(t.selBg).Foreground(t.fg)
	t.box = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.accent).Padding(0, 1)
	t.key = lipgloss.NewStyle().Foreground(t.accent).Bold(true)
	t.badgeNew = lipgloss.NewStyle().Foreground(t.accent).Bold(true)
	t.badgeChg = lipgloss.NewStyle().Foreground(t.yellow)
	return t
}

// truncate shortens s to w display cells.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > w {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// pad right-pads s to w cells.
func pad(s string, w int) string {
	n := lipgloss.Width(s)
	if n >= w {
		return s
	}
	return s + strings.Repeat(" ", w-n)
}

// wrap breaks text into lines of at most w cells (word based).
func wrap(s string, w int) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		line := ""
		for _, wd := range words {
			if line == "" {
				line = wd
				continue
			}
			if lipgloss.Width(line)+1+lipgloss.Width(wd) > w {
				out = append(out, line)
				line = wd
				continue
			}
			line += " " + wd
		}
		out = append(out, line)
	}
	return out
}

// ansiTruncate cuts a styled line to w cells without breaking escape codes.
func ansiTruncate(s string, w int) string { return ansi.Truncate(s, w, "…") }
