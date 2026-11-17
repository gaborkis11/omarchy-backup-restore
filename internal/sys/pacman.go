package sys

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Package describes one explicitly installed package.
type Package struct {
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Provides []string `json:"provides,omitempty"`
	Foreign  bool     `json:"foreign,omitempty"` // AUR / not in a sync repo
	// Class: user | default | iso | hardware-pool
	Class    string `json:"class"`
	Hardware string `json:"hardware,omitempty"` // nvidia | amd | intel | amd-cpu | intel-cpu
}

// ExplicitPackages returns `pacman -Qe` packages with provides parsed from `pacman -Qi`.
func ExplicitPackages(ctx context.Context, e *Env) ([]Package, error) {
	names, err := e.Out(ctx, "pacman", "-Qqe")
	if err != nil {
		return nil, err
	}
	foreignOut, _ := e.Out(ctx, "pacman", "-Qqm")
	foreign := toSet(strings.Fields(foreignOut))
	list := strings.Fields(names)
	if len(list) == 0 {
		return nil, nil
	}
	info, err := e.Run(ctx, "pacman", append([]string{"-Qi"}, list...)...)
	if err != nil {
		return nil, err
	}
	pkgs := parseQi(string(info.Stdout))
	for i := range pkgs {
		pkgs[i].Foreign = foreign[pkgs[i].Name]
	}
	return pkgs, nil
}

func parseQi(s string) []Package {
	var out []Package
	var cur *Package
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	field := ""
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			if cur != nil {
				out = append(out, *cur)
				cur = nil
			}
			continue
		}
		if !strings.HasPrefix(line, " ") {
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			field = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			switch field {
			case "Name":
				cur = &Package{Name: v}
			case "Version":
				if cur != nil {
					cur.Version = v
				}
			case "Provides":
				if cur != nil && v != "None" {
					cur.Provides = append(cur.Provides, provNames(v)...)
				}
			}
		} else if field == "Provides" && cur != nil {
			cur.Provides = append(cur.Provides, provNames(line)...)
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

func provNames(v string) []string {
	var out []string
	for _, f := range strings.Fields(v) {
		// strip version constraint: "nvim=0.10" -> "nvim"
		if i := strings.IndexAny(f, "=<>"); i > 0 {
			f = f[:i]
		}
		out = append(out, f)
	}
	return out
}

func toSet(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// readPackageList reads an Omarchy *.packages file (one name per line, # comments).
func readPackageList(p string) []string {
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := sc.Text()
		if i := strings.Index(l, "#"); i >= 0 {
			l = l[:i]
		}
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

var runningRe = regexp.MustCompile(`\[PACMAN\] Running '(.*)'`)

// isoPackages returns the package set the installer image pacstrapped,
// parsed from the first "Running ... pacman ... -S..." line of pacman.log.
func isoPackages() []string {
	f, err := os.Open("/var/log/pacman.log")
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		m := runningRe.FindStringSubmatch(sc.Text())
		if m == nil || !strings.Contains(m[1], "pacman") || !strings.Contains(m[1], " -S") {
			continue
		}
		var out []string
		for _, f := range strings.Fields(m[1]) {
			if strings.HasPrefix(f, "-") || strings.Contains(f, "/") || f == "pacman" {
				continue
			}
			out = append(out, f)
		}
		return out
	}
	return nil
}

// DefaultPackageSet returns every name Omarchy installs on its own on this
// platform: the platform default lists, the hardware pool and the ISO set.
func DefaultPackageSet(ctx context.Context, e *Env) map[string]bool {
	set := map[string]bool{}
	if out, err := e.Out(ctx, "omarchy-pkg-defaults"); err == nil {
		for _, n := range strings.Fields(out) {
			set[n] = true
		}
	} else {
		for _, n := range readPackageList(filepath.Join(e.OmarchyPath, "install/omarchy-base.packages")) {
			set[n] = true
		}
	}
	for _, n := range isoPackages() {
		set[n] = true
	}
	// Always part of an Omarchy system.
	for _, n := range []string{"omarchy", "omarchy-settings", "omarchy-keyring", "omarchy-dev", "base", "linux-firmware", "sudo"} {
		set[n] = true
	}
	return set
}

// HardwarePool returns omarchy-other.packages (drivers and machine-specific extras
// that the installer picks per machine).
func HardwarePool(e *Env) map[string]bool {
	return toSet(readPackageList(filepath.Join(e.OmarchyPath, "install/omarchy-other.packages")))
}

var hwPatterns = []struct {
	re  *regexp.Regexp
	tag string
}{
	{regexp.MustCompile(`^(lib32-)?nvidia|^libva-nvidia|^cuda|^nvidia-container`), "nvidia"},
	{regexp.MustCompile(`^(lib32-)?vulkan-radeon$|^xf86-video-amdgpu$|^rocm`), "amd"},
	{regexp.MustCompile(`^(lib32-)?vulkan-intel$|^intel-media-driver$|^xf86-video-intel$`), "intel"},
	{regexp.MustCompile(`^amd-ucode$`), "amd-cpu"},
	{regexp.MustCompile(`^intel-ucode$`), "intel-cpu"},
}

// HardwareTag returns a hardware requirement tag for a package name, or "".
func HardwareTag(name string) string {
	for _, p := range hwPatterns {
		if p.re.MatchString(name) {
			return p.tag
		}
	}
	return ""
}

// systemOnly are packages that belong to the boot/install machinery and must
// never be replayed onto another installation.
var systemOnly = toSet([]string{
	"efibootmgr", "mkinitcpio", "limine", "limine-mkinitcpio-hook", "limine-snapper-sync",
	"linux", "linux-omarchy", "linux-omarchy-headers", "linux-headers", "linux-lts",
	"grub", "systemd-boot", "plymouth", "snapper", "btrfs-progs", "cryptsetup",
})

// ClassifyPackages assigns Class/Hardware to each explicit package.
func ClassifyPackages(ctx context.Context, e *Env, pkgs []Package) []Package {
	defaults := DefaultPackageSet(ctx, e)
	pool := HardwarePool(e)
	for i := range pkgs {
		p := &pkgs[i]
		p.Hardware = HardwareTag(p.Name)
		switch {
		case defaults[p.Name] || anyIn(p.Provides, defaults):
			p.Class = "default"
		case systemOnly[p.Name]:
			p.Class = "iso"
		case pool[p.Name] || anyIn(p.Provides, pool):
			p.Class = "hardware-pool"
		default:
			p.Class = "user"
		}
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Name < pkgs[j].Name })
	return pkgs
}

func anyIn(xs []string, set map[string]bool) bool {
	for _, x := range xs {
		if set[x] {
			return true
		}
	}
	return false
}

// Installed reports whether a package (or something providing it) is installed.
func Installed(ctx context.Context, e *Env, name string) bool {
	_, err := e.Run(ctx, "pacman", "-T", name)
	return err == nil
}

// Available reports whether a package exists in a sync repository.
func Available(ctx context.Context, e *Env, name string) bool {
	_, err := e.Run(ctx, "pacman", "-Si", name)
	return err == nil
}

// AURAvailable checks the AUR through yay (best effort).
func AURAvailable(ctx context.Context, e *Env, name string) bool {
	out, err := e.Out(ctx, "yay", "-Si", "--aur", name)
	return err == nil && strings.Contains(out, name)
}
