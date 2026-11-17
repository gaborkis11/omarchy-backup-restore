package inventory

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Matcher decides whether a path is excluded. Patterns follow restic's rules
// closely enough for size estimates and scanning: a pattern without "/" matches
// any path component; an absolute pattern matches the path or one of its
// parents, segment by segment, with "*" inside a segment and "**" spanning
// any number of segments.
type Matcher struct {
	base []string
	abs  [][]string
}

// NewMatcher compiles patterns.
func NewMatcher(patterns ...[]string) *Matcher {
	m := &Matcher{}
	for _, ps := range patterns {
		m.Add(ps...)
	}
	return m
}

// Add compiles more patterns.
func (m *Matcher) Add(ps ...string) {
	for _, p := range ps {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		if !strings.Contains(p, "/") {
			m.base = append(m.base, p)
			continue
		}
		if strings.HasPrefix(p, "/") {
			m.abs = append(m.abs, strings.Split(strings.Trim(p, "/"), "/"))
		}
	}
}

// Match reports whether abs path p is excluded.
func (m *Matcher) Match(p string) bool {
	if m == nil {
		return false
	}
	segs := strings.Split(strings.Trim(p, "/"), "/")
	for _, b := range m.base {
		for _, s := range segs {
			if ok, _ := path.Match(b, s); ok {
				return true
			}
		}
	}
	for _, pat := range m.abs {
		if matchSegs(pat, segs) {
			return true
		}
	}
	return false
}

// matchSegs reports whether pat matches segs or a prefix of segs.
func matchSegs(pat, segs []string) bool {
	if len(pat) == 0 {
		return true // pattern consumed: matches this path or a parent of it
	}
	if pat[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if matchSegs(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	if ok, _ := path.Match(pat[0], segs[0]); !ok {
		return false
	}
	return matchSegs(pat[1:], segs[1:])
}

// WalkStats is the result of measuring a path.
type WalkStats struct {
	Size    int64
	Files   int
	Special []string // sockets, fifos, devices (absolute paths)
	Denied  []string // unreadable directories
}

// Measure walks root (file or dir) and sums regular file sizes, honouring the matcher.
func Measure(root string, m *Matcher) WalkStats {
	var st WalkStats
	info, err := os.Lstat(root)
	if err != nil {
		return st
	}
	if !info.IsDir() {
		if info.Mode().Type()&(fs.ModeSocket|fs.ModeNamedPipe|fs.ModeDevice|fs.ModeCharDevice) != 0 {
			st.Special = append(st.Special, root)
			return st
		}
		if info.Mode().IsRegular() {
			st.Size, st.Files = info.Size(), 1
		}
		return st
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				st.Denied = append(st.Denied, p)
				return fs.SkipDir
			}
			return nil
		}
		if p != root && m.Match(p) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		t := d.Type()
		switch {
		case t&(fs.ModeSocket|fs.ModeNamedPipe|fs.ModeDevice|fs.ModeCharDevice) != 0:
			st.Special = append(st.Special, p)
		case t.IsRegular():
			if fi, err := d.Info(); err == nil {
				st.Size += fi.Size()
				st.Files++
			}
		}
		return nil
	})
	return st
}

// FileSHA returns the hex sha256 of a file ("" on error).
func FileSHA(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SameFile compares two files by content.
func SameFile(a, b string) bool {
	sa, sb := FileSHA(a), FileSHA(b)
	return sa != "" && sa == sb
}

// HumanSize formats bytes in a short Hungarian-friendly form.
func HumanSize(n int64) string {
	const k = 1024
	switch {
	case n < k:
		return itoa(n) + " B"
	case n < k*k:
		return fmt1(float64(n)/k) + " KB"
	case n < k*k*k:
		return fmt1(float64(n)/(k*k)) + " MB"
	default:
		return fmt1(float64(n)/(k*k*k)) + " GB"
	}
}

func itoa(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	var b [24]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			break
		}
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func fmt1(f float64) string {
	whole := int64(f)
	frac := int64((f-float64(whole))*10 + 0.5)
	if frac >= 10 {
		whole++
		frac = 0
	}
	if whole >= 100 {
		return itoa(whole)
	}
	return itoa(whole) + i18n.Decimal() + itoa(frac)
}
