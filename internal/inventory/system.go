package inventory

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gaborkis11/omarchy-backup-restore/internal/manifest"
	"github.com/gaborkis11/omarchy-backup-restore/internal/sys"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

// EtcDeny lists /etc paths (relative to /etc) that obr never backs up or restores.
// A trailing "/" means the whole directory. The root helper enforces the same list.
var EtcDeny = []string{
	"machine-id", "fstab", "crypttab", "passwd", "passwd-", "shadow", "shadow-", "group", "group-",
	"gshadow", "gshadow-", "subuid", "subuid-", "subgid", "subgid-", "hostname", "localtime",
	"locale.conf", "locale.gen", "vconsole.conf", "adjtime", "ld.so.cache", "resolv.conf",
	".pwd.lock", "shells", "sudoers", "sudoers.d/", "pam.d/", "security/", "pacman.conf", "pacman.d/",
	"mkinitcpio.conf", "mkinitcpio.conf.d/", "mkinitcpio.d/", "default/limine", "kernel/",
	"limine-entry-tool.conf", "limine-entry-tool.d/", "limine-snapper-sync.conf", "snapper/", "conf.d/snapper",
	"sddm.conf.d/", "ssh/ssh_host_*", "ssl/", "ca-certificates/", "pki/", "systemd/system/",
	"systemd/user/", "NetworkManager/system-connections/", "cups/", "chromium/policies/",
	"opt/chrome/policies/", "os-release", "mtab", "printcap", "fstab.*", "cni/", "omarchy/",
	"ufw/", "default/ufw", "plymouth/", "nsswitch.conf", "profile.d/omarchy*", "skel/",
	"credstore*", ".updated", "cryptsetup-keys.d/", "audit/", "polkit-1/", "xdg/", "udev/hwdb.bin",
}

// EtcDenied reports whether rel (relative to /etc) is on the deny list.
func EtcDenied(rel string) bool {
	rel = strings.TrimPrefix(rel, "/")
	rel = strings.TrimPrefix(rel, "etc/")
	for _, d := range EtcDeny {
		if strings.HasSuffix(d, "/") {
			if strings.HasPrefix(rel, d) || rel+"/" == d {
				return true
			}
			continue
		}
		if strings.HasSuffix(d, "*") {
			if strings.HasPrefix(rel, strings.TrimSuffix(d, "*")) {
				return true
			}
			continue
		}
		if rel == d {
			return true
		}
	}
	return strings.HasSuffix(rel, ".pacnew") || strings.HasSuffix(rel, ".pacsave") || strings.HasSuffix(rel, ".back") || strings.HasSuffix(rel, "~")
}

var lineKV = regexp.MustCompile(`^\s*([^:]+):\s*(.*)$`)

func (s *Scanner) scanSystem(ctx context.Context) {
	e := s.Env
	sy := &s.inv.System
	inst := e.InstallTime()

	// system units enabled after installation
	sy.Units = wantsAfter(filepath.Join(e.EtcDir, "systemd/system"), inst)
	if len(sy.Units) > 0 {
		s.add(&Item{ID: "sys:units", Category: "system", Kind: KindSystem,
			Label:    i18n.T("Rendszerszolgáltatások: ") + strings.Join(sy.Units, ", "),
			Detail:   i18n.T("Azok a szolgáltatások, amelyeket a telepítés után kapcsoltál be. Visszaállításkor bekapcsoljuk őket, ha a programjuk települt."),
			Selected: true})
	}
	sy.UserUnits = wantsAfter(filepath.Join(e.Home, ".config/systemd/user"), inst)
	if len(sy.UserUnits) > 0 {
		s.add(&Item{ID: "sys:user-units", Category: "system", Kind: KindSystem,
			Label:    i18n.T("Saját háttérszolgáltatások: ") + strings.Join(sy.UserUnits, ", "),
			Selected: true})
	}

	// groups
	if out, err := e.Out(ctx, "id", "-nG"); err == nil {
		var extra []string
		for _, g := range strings.Fields(out) {
			sy.Groups = append(sy.Groups, g)
			if g != e.User && g != "wheel" && g != "input" {
				extra = append(extra, g)
			}
		}
		if len(extra) > 0 {
			s.add(&Item{ID: "sys:groups", Category: "system", Kind: KindSystem,
				Label: i18n.T("Csoporttagság: ") + strings.Join(extra, ", "), Selected: true,
				Detail: i18n.T("Pl. a docker csoport kell ahhoz, hogy sudo nélkül használd a Dockert.")})
		}
	}

	// locale, keyboard, timezone
	if out, err := e.Out(ctx, "localectl", "status"); err == nil {
		for _, l := range strings.Split(out, "\n") {
			m := lineKV.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			k, v := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
			switch k {
			case "System Locale":
				sy.Lang = strings.TrimPrefix(v, "LANG=")
			case "VC Keymap":
				sy.Keymap = v
			case "X11 Layout":
				sy.X11Layout = v
			case "X11 Model":
				sy.X11Model = v
			case "X11 Variant":
				sy.X11Variant = v
			case "X11 Options":
				sy.X11Options = v
			}
		}
	}
	sy.Timezone, _ = e.Out(ctx, "timedatectl", "show", "-p", "Timezone", "--value")
	if sy.Keymap != "" || sy.Timezone != "" {
		s.add(&Item{ID: "sys:locale", Category: "system", Kind: KindSystem,
			Label:    i18n.T("Billentyűzet és időzóna: ") + strings.Trim(sy.Keymap+", "+sy.Timezone, ", "),
			Selected: true})
	}
	sy.Hostname = sys.Hostname()
	s.add(&Item{ID: "sys:hostname", Category: "system", Kind: KindSystem,
		Label: i18n.T("Gépnév: ") + sy.Hostname, Detail: i18n.T("Alapból nem állítjuk át az új gép nevét."), Selected: false})

	// NetworkManager saved connections
	if out, err := e.Out(ctx, "nmcli", "-t", "-f", "NAME,TYPE,FILENAME", "connection", "show"); err == nil {
		for _, l := range strings.Split(out, "\n") {
			parts := splitNM(l)
			if len(parts) == 3 && strings.HasPrefix(parts[2], "/etc/NetworkManager/system-connections/") {
				sy.Connections = append(sy.Connections, parts[0])
			}
		}
	}
	if len(sy.Connections) > 0 {
		s.add(&Item{ID: "sys:network", Category: "system", Kind: KindSystem, NeedsRoot: true,
			Label: i18n.T("Wifi-hálózatok és kapcsolatok (jelszavakkal): ") + strings.Join(sy.Connections, ", "), Selected: true, Secret: true})
	}

	// Bluetooth pairings
	hw := sys.DetectHardware(ctx, e)
	if len(hw.BTAdapters) > 0 {
		sy.BTAdapter = hw.BTAdapters[0]
	}
	if out, err := e.Out(ctx, "bluetoothctl", "devices", "Paired"); err == nil {
		for _, l := range strings.Split(out, "\n") {
			f := strings.Fields(l)
			if len(f) >= 2 && f[0] == "Device" {
				sy.Bluetooth = append(sy.Bluetooth, manifest.BTDevice{MAC: f[1], Name: strings.Join(f[2:], " ")})
			}
		}
	}
	if len(sy.Bluetooth) > 0 && sy.BTAdapter != "" {
		var names []string
		for _, d := range sy.Bluetooth {
			names = append(names, d.Name)
		}
		s.add(&Item{ID: "sys:bluetooth", Category: "system", Kind: KindSystem, NeedsRoot: true,
			Label:    i18n.T("Bluetooth-párosítások: ") + strings.Join(names, ", "),
			Detail:   i18n.T("Csak ugyanazon a gépen (ugyanazzal a Bluetooth-adapterrel) állítható vissza; máshol újra kell párosítani."),
			Hardware: "bt:" + sy.BTAdapter, Selected: true})
	}

	// printers
	if out, err := e.Out(ctx, "lpstat", "-v"); err == nil {
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, "device for ") {
				if n, _, ok := strings.Cut(strings.TrimPrefix(l, "device for "), ":"); ok {
					sy.Printers = append(sy.Printers, n)
				}
			}
		}
	}
	if len(sy.Printers) > 0 {
		s.add(&Item{ID: "sys:printers", Category: "system", Kind: KindSystem, NeedsRoot: true,
			Label: i18n.T("Nyomtatók: ") + strings.Join(sy.Printers, ", "), Selected: true})
	}

	s.scanEtc(ctx, inst)
}

func splitNM(l string) []string {
	// nmcli -t escapes ':' inside fields as '\:'
	var parts []string
	var cur strings.Builder
	esc := false
	for _, r := range l {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case r == '\\':
			esc = true
		case r == ':':
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	return append(parts, cur.String())
}

// wantsAfter lists unit symlinks in <dir>/*.wants created after t.
func wantsAfter(dir string, t time.Time) []string {
	set := map[string]bool{}
	wants, _ := filepath.Glob(filepath.Join(dir, "*.wants", "*"))
	for _, w := range wants {
		st, err := os.Lstat(w)
		if err != nil || st.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if !t.IsZero() && !st.ModTime().After(t.Add(2*time.Minute)) {
			continue
		}
		name := filepath.Base(w)
		if strings.HasPrefix(name, "getty@") || strings.HasPrefix(name, "systemd-") {
			continue
		}
		set[name] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// scanEtc offers /etc files changed after installation (never the deny list).
func (s *Scanner) scanEtc(ctx context.Context, inst time.Time) {
	e := s.Env
	if inst.IsZero() {
		return
	}
	cutoff := inst.Add(5 * time.Minute)
	var cands []string
	_ = filepath.WalkDir(e.EtcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(e.EtcDir, p)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if EtcDenied(rel + "/") {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || EtcDenied(rel) {
			return nil
		}
		fi, err := d.Info()
		if err != nil || !fi.ModTime().After(cutoff) {
			return nil
		}
		cands = append(cands, p)
		return nil
	})
	if len(cands) == 0 {
		return
	}
	// Package-owned files only count when they differ from the package version.
	owned := map[string]string{}
	res, _ := e.Run(ctx, "pacman", append([]string{"-Qo"}, cands...)...)
	for _, l := range strings.Split(string(res.Stdout), "\n") {
		if f, rest, ok := strings.Cut(l, " is owned by "); ok {
			if pk := strings.Fields(rest); len(pk) > 0 {
				owned[f] = pk[0]
			}
		}
	}
	modified := map[string]bool{}
	pkgs := map[string]bool{}
	for _, pk := range owned {
		pkgs[pk] = true
	}
	if len(pkgs) > 0 {
		args := []string{"-Qkk"}
		for pk := range pkgs {
			args = append(args, pk)
		}
		r, _ := e.Run(ctx, "pacman", args...)
		for _, l := range strings.Split(string(r.Stdout)+string(r.Stderr), "\n") {
			for _, f := range cands {
				if strings.Contains(l, f+" (") && strings.Contains(strings.ToLower(l), "mismatch") {
					modified[f] = true
				}
			}
		}
	}
	for _, f := range cands {
		if _, isOwned := owned[f]; isOwned && !modified[f] {
			continue
		}
		rel, _ := filepath.Rel(e.EtcDir, f)
		hw := ""
		if strings.Contains(strings.ToLower(rel), "nvidia") || fileContains(f, "nvidia") {
			hw = "nvidia"
		}
		st, _ := os.Stat(f)
		var size int64
		if st != nil {
			size = st.Size()
		}
		s.add(&Item{ID: "etc:" + rel, Category: "system", Kind: KindSystem, NeedsRoot: true,
			Label:    "/etc/" + rel,
			Detail:   i18n.T("A telepítés után módosított rendszerfájl. Alapból nem mentjük; visszaállításkor mindig rákérdezünk."),
			Hardware: hw, Size: size, Selected: false})
		s.inv.System.EtcFiles = append(s.inv.System.EtcFiles, rel)
	}
}

func fileContains(p, needle string) bool {
	b, err := os.ReadFile(p)
	return err == nil && strings.Contains(strings.ToLower(string(b)), needle)
}
