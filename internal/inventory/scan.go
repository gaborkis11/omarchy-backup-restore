package inventory

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gaborkis11/omarchy-backup-restore/internal/catalog"
	"github.com/gaborkis11/omarchy-backup-restore/internal/manifest"
	"github.com/gaborkis11/omarchy-backup-restore/internal/sys"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

// cacheNames are directory/file names that hold regenerable caches inside
// application data (Chromium/Electron style). Applied under ~/.config and ~/.local.
var cacheNames = []string{
	"Cache", "Code Cache", "GPUCache", "DawnWebGPUCache", "DawnGraphiteCache", "GrShaderCache",
	"GraphiteDawnCache", "ShaderCache", "CacheStorage", "ScriptCache", "component_crx_cache",
	"extensions_crx_cache", "optimization_guide_model_store", "WasmTtsEngine",
	"OnDeviceHeadSuggestModel", "Crashpad", "crashpad", "Crash Reports", "blob_storage",
	"*.log", "*.log.old", "*.log.[0-9]",
}

// basenameExcludes apply everywhere.
var basenameExcludes = []string{
	"SingletonLock", "SingletonCookie", "SingletonSocket", "__pycache__", "node_modules",
	".venv", "*.pyc", ".obr-tmp-*", "cached_layouts", "*.obr-tmp",
}

// heavyProjectDirs are excluded inside git projects.
var heavyProjectDirs = []string{"venv", ".venv", "node_modules", "dist", "build", "target",
	"__pycache__", ".cache", ".tox", ".mypy_cache", ".pytest_cache", ".gradle", ".next", ".turbo"}

// junk are home-relative paths (or prefixes ending in "*") that are never offered.
var junk = []string{
	".cache", ".nv", ".npm", ".cargo/registry", ".rustup", ".local/share/mise", ".local/share/nvim",
	".local/share/Trash", ".local/share/gvfs-metadata", ".local/share/recently-used.xbel",
	".local/share/obr", ".local/state/obr", ".config/pulse", ".config/ibus", ".config/fcitx",
	".lesshst", ".wget-hsts", ".python_history", ".node_repl_history", ".local/state/nvim",
	".local/state/mise", ".local/state/omarchy", ".config/hypr.backup-*", ".local/share/flatpak",
	".config/dconf", ".local/share/sddm", ".xsession-errors*", ".Xauthority", ".dbus",
	".local/state/gh", ".local/share/applications", ".config/systemd", ".local/state/lazygit",
	".config/obr/secrets", ".local/share/desktop-directories", ".config/menus", ".gtkrc-2.0",
	".local/share/omarchy", ".local/state/herdr", ".local/state/tensaku", ".cache*",
	"go/pkg", ".config/go", ".local/share/go", ".local/share/pnpm", ".bun/install/cache",
	".gradle/caches", ".m2/repository",
}

// omarchySeedKeys decides which seed groups are shown under "Omarchy".
var omarchySeedKeys = map[string]string{
	".config/hypr":                          "Hyprland (ablakkezelő, gyorsbillentyűk, monitor, szélre húzás)",
	".config/omarchy":                       "Omarchy (bar, dock, hookok, alapértelmezések, saját háttérképek)",
	".config/autostart":                     "Automatikusan induló programok",
	".config/uwsm":                          "Munkamenet-környezet (uwsm)",
	".config/git":                           "Git beállítások",
	".bashrc":                               "Bash (.bashrc)",
	".bash_profile":                         "Bash (.bash_profile)",
	".XCompose":                             "Billentyűkombinációk (.XCompose)",
	".config/starship.toml":                 "Parancssor kinézete (starship)",
	".config/foot":                          "Terminál (foot)",
	".config/alacritty":                     "Terminál (Alacritty)",
	".config/ghostty":                       "Terminál (Ghostty)",
	".config/kitty":                         "Terminál (kitty)",
	".config/fcitx5":                        "Beviteli mód (fcitx5)",
	".config/wireplumber":                   "Hangbeállítások (WirePlumber)",
	".config/xdg-desktop-portal":            "Képernyőmegosztás (portal)",
	".config/hyprland-mimeapps.list":        "Hyprland alapértelmezett alkalmazások",
	".config/chromium-flags.conf":           "Chromium indítási kapcsolók",
	".config/tmux":                          "tmux",
	".config/lazygit":                       "lazygit",
	".config/herdr":                         "herdr",
	".config/btop":                          "btop",
	".config/imv":                           "Képnézegető (imv)",
	".config/opencode":                      "opencode",
	".config/obsidian":                      "Obsidian indítási kapcsolók",
	".config/hyprland-preview-share-picker": "Képernyőmegosztás-választó",
}

// Scanner holds scan state.
type Scanner struct {
	Env      *sys.Env
	Catalog  *catalog.Catalog
	Override *catalog.Overrides
	Prev     *manifest.Manifest // previous backup (may be nil)

	inv     *Inventory
	claimed []string // home-relative paths claimed by an item
	matcher *Matcher
}

// Scan runs the full inventory.
func (s *Scanner) Scan(ctx context.Context) (*Inventory, error) {
	e := s.Env
	if s.Override == nil {
		s.Override = &catalog.Overrides{}
	}
	s.inv = &Inventory{Status: map[string]string{}, SeedContents: map[string][]byte{}}
	s.inv.GlobalExcludes = s.globalExcludes()
	s.matcher = NewMatcher(s.inv.GlobalExcludes)
	for _, j := range junk {
		s.claim(j)
	}

	s.scanCatalog()
	s.scanReplay(ctx)
	s.scanSeeds()
	if err := s.scanPackages(ctx); err != nil {
		s.warn(i18n.T("csomaglista: %v"), err)
	}
	s.scanGit(ctx)
	s.scanSystem(ctx)
	s.scanFiles()
	s.scanOther()
	s.inv.Migrations = listNames(filepath.Join(e.Home, ".local/state/omarchy/migrations"))
	s.compare()
	return s.inv, nil
}

func (s *Scanner) warn(f string, a ...any) {
	s.inv.Warnings = append(s.inv.Warnings, fmt.Sprintf(f, a...))
}

func (s *Scanner) globalExcludes() []string {
	out := append([]string{}, basenameExcludes...)
	for _, n := range cacheNames {
		out = append(out,
			filepath.Join(s.Env.Home, ".config/**", n),
			filepath.Join(s.Env.Home, ".local/share/**", n),
			filepath.Join(s.Env.Home, ".local/state/**", n))
	}
	for _, o := range s.Override.Entries {
		if o.Action == "never" || o.Action == "cache" {
			out = append(out, s.Env.Abs(o.Path))
		}
	}
	return out
}

func (s *Scanner) claim(rel string) { s.claimed = append(s.claimed, strings.TrimPrefix(rel, "~/")) }

// isClaimed reports whether rel equals, contains or is inside a claimed path.
func (s *Scanner) isClaimed(rel string) bool {
	for _, c := range s.claimed {
		if strings.HasSuffix(c, "*") {
			if strings.HasPrefix(rel, strings.TrimSuffix(c, "*")) {
				return true
			}
			continue
		}
		if rel == c || strings.HasPrefix(rel, c+"/") || strings.HasPrefix(c, rel+"/") {
			return true
		}
	}
	return false
}

// isInsideClaimed reports whether rel equals or is inside a claimed path (not a parent of one).
func (s *Scanner) isInsideClaimed(rel string) bool {
	for _, c := range s.claimed {
		if strings.HasSuffix(c, "*") {
			if strings.HasPrefix(rel, strings.TrimSuffix(c, "*")) {
				return true
			}
			continue
		}
		if rel == c || strings.HasPrefix(rel, c+"/") {
			return true
		}
	}
	return false
}

// expand resolves home-relative globs to existing home-relative paths.
func (s *Scanner) expand(patterns []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range patterns {
		abs := s.Env.Abs(p)
		matches, _ := filepath.Glob(abs)
		for _, m := range matches {
			r := s.Env.Rel(m)
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	sort.Strings(out)
	return out
}

// absPatterns converts catalog excludes ("~/x" or basename) to restic patterns.
func (s *Scanner) absPatterns(ps []string) []string {
	var out []string
	for _, p := range ps {
		if strings.HasPrefix(p, "~/") {
			out = append(out, s.Env.Abs(p))
		} else {
			out = append(out, p)
		}
	}
	return out
}

func (s *Scanner) measure(rels []string, extra []string) (int64, []string) {
	m := NewMatcher(s.inv.GlobalExcludes, extra)
	var total int64
	var special []string
	for _, r := range rels {
		st := Measure(s.Env.Abs(r), m)
		total += st.Size
		special = append(special, st.Special...)
		for _, d := range st.Denied {
			s.warn(i18n.T("nem olvasható: %s"), d)
		}
	}
	return total, special
}

func (s *Scanner) add(it *Item) *Item {
	s.inv.Items = append(s.inv.Items, it)
	for _, p := range it.Paths {
		s.claim(p)
	}
	return it
}

// ───────────────────────── catalog apps ─────────────────────────

func (s *Scanner) scanCatalog() {
	for _, a := range s.Catalog.Apps {
		var paths []string
		for _, p := range s.expand(a.Paths) {
			if !onlyNativeMessaging(s.Env.Abs(p)) {
				paths = append(paths, p)
			}
		}
		secrets := s.expand(a.SecretPaths)
		if len(paths) == 0 && len(secrets) == 0 {
			continue
		}
		prefix := "app:"
		if a.Category == "secrets" {
			prefix = "secret:"
		}
		aLabel, aNote, aSecret := a.Localized()
		excl := s.absPatterns(a.Exclude)
		size, special := s.measure(paths, excl)
		it := &Item{
			ID: prefix + a.ID, Category: a.Category, Kind: KindFiles, Label: aLabel, Detail: aNote,
			Size: size, Paths: paths, Excludes: append(excl, special...), Replace: a.Replace,
			Packages: a.Packages, Running: a.Running, Post: a.PostRestore, Hardware: a.Hardware,
			Selected: a.On(), Secret: a.Category == "secrets",
		}
		if a.Category == "secrets" {
			it.Requires = a.Requires
		}
		if len(paths) > 0 {
			s.add(it)
		}
		if len(secrets) > 0 && a.Category != "secrets" {
			ssize, _ := s.measure(secrets, nil)
			req := append([]string{}, a.Requires...)
			if len(paths) > 0 {
				req = append(req, it.ID)
			}
			label := aSecret
			if label == "" {
				label = aLabel + i18n.T(" – titkos részek")
			}
			s.add(&Item{ID: "secret:" + a.ID, Category: "secrets", Kind: KindFiles, Label: label,
				Size: ssize, Paths: secrets, Secret: true, Requires: req, Selected: a.On()})
		}
	}
}

// ───────────────────────── Omarchy replay ─────────────────────────

func (s *Scanner) scanReplay(ctx context.Context) {
	e := s.Env
	st := filepath.Join(e.Home, ".local/state/omarchy")
	r := &s.inv.Replay

	// Theme
	if b, err := os.ReadFile(filepath.Join(st, "current/theme.name")); err == nil {
		r.Theme = strings.TrimSpace(string(b))
	}
	themesDir := filepath.Join(e.Home, ".config/omarchy/themes")
	var themePaths []string
	if ents, err := os.ReadDir(themesDir); err == nil {
		for _, d := range ents {
			if !d.IsDir() {
				continue
			}
			dir := filepath.Join(themesDir, d.Name())
			ut := manifest.UserTheme{Name: d.Name()}
			if sys.IsDir(filepath.Join(dir, ".git")) {
				if url, err := e.Out(ctx, "git", "-C", dir, "remote", "get-url", "origin"); err == nil {
					ut.GitURL = url
				}
			}
			if ut.GitURL == "" {
				themePaths = append(themePaths, e.Rel(dir))
			}
			r.UserThemes = append(r.UserThemes, ut)
		}
	}
	s.claim(".config/omarchy/themes")
	if font, err := e.Out(ctx, "omarchy-font-current"); err == nil {
		r.Font = font
	}
	if r.Theme != "" || len(r.UserThemes) > 0 {
		label := i18n.T("Téma: ") + r.Theme
		if r.Font != "" {
			label += i18n.T(", betűtípus: ") + r.Font
		}
		detail := ""
		for _, t := range r.UserThemes {
			if t.GitURL != "" {
				detail += fmt.Sprintf(i18n.T("Saját téma: %s (%s)\n"), t.Name, t.GitURL)
			} else {
				detail += fmt.Sprintf(i18n.T("Saját téma: %s (fájlokkal)\n"), t.Name)
			}
		}
		size, _ := s.measure(themePaths, nil)
		s.add(&Item{ID: "replay:theme", Category: "omarchy", Kind: KindReplay, Label: label,
			Detail: strings.TrimSpace(detail + i18n.T("Visszaállításkor az Omarchy saját parancsával (omarchy-theme-set) állítjuk be, így az új verzióhoz igazodik.")),
			Paths:  themePaths, Size: size, Selected: true})
	}

	// Toggles
	skelToggles := toSet(listNames(filepath.Join(e.SkelDir, ".local/state/omarchy/toggles/hypr")))
	for _, n := range listNames(filepath.Join(st, "toggles/hypr")) {
		name := strings.TrimSuffix(n, ".lua")
		if skelToggles[n] || !strings.HasSuffix(n, ".lua") {
			continue
		}
		if sys.Exists(filepath.Join(e.OmarchyPath, "default/hypr/toggles", n)) {
			r.HyprToggles = append(r.HyprToggles, name)
		}
	}
	if ents, err := os.ReadDir(filepath.Join(st, "toggles")); err == nil {
		for _, d := range ents {
			if d.Type().IsRegular() {
				r.FlagToggles = append(r.FlagToggles, d.Name())
			}
		}
	}
	if len(r.HyprToggles)+len(r.FlagToggles) > 0 {
		s.add(&Item{ID: "replay:toggles", Category: "omarchy", Kind: KindReplay,
			Label:    i18n.T("Megjelenés- és viselkedéskapcsolók: ") + strings.Join(append(append([]string{}, r.HyprToggles...), r.FlagToggles...), ", "),
			Detail:   i18n.T("Pl. üveghatás a baron és az ablakokon. Az Omarchy saját kapcsolóival állítjuk vissza."),
			Selected: true})
	}

	// dconf
	if out, err := e.Out(ctx, "dconf", "dump", "/"); err == nil && strings.TrimSpace(out) != "" {
		r.DconfDump = true
		s.add(&Item{ID: "replay:dconf", Category: "omarchy", Kind: KindReplay,
			Label: i18n.T("GTK/GNOME beállítások (sötét mód, ikontéma, fájlválasztó)"), Size: int64(len(out)), Selected: true})
	}

	s.scanDesktopFiles()
}

func (s *Scanner) scanDesktopFiles() {
	e := s.Env
	dir := filepath.Join(e.Home, ".local/share/applications")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, d := range ents {
		if !strings.HasSuffix(d.Name(), ".desktop") || d.IsDir() {
			continue
		}
		p := filepath.Join(dir, d.Name())
		if skel := filepath.Join(e.SkelDir, ".local/share/applications", d.Name()); SameFile(p, skel) {
			continue // stock Omarchy webapp, the installer recreates it
		}
		de := ParseDesktop(p)
		rel := e.Rel(p)
		paths := []string{rel}
		paths = append(paths, s.iconFiles(de.Icon)...)
		size, _ := s.measure(paths, nil)
		name := de.Name
		if name == "" {
			name = strings.TrimSuffix(d.Name(), ".desktop")
		}
		switch {
		case strings.HasPrefix(de.Exec, "omarchy-launch-webapp "):
			url := unquote(strings.TrimSpace(strings.TrimPrefix(de.Exec, "omarchy-launch-webapp ")))
			id := "webapp:" + name
			s.inv.Replay.Webapps = append(s.inv.Replay.Webapps, manifest.Webapp{
				Name: name, URL: url, Icon: de.Icon, MimeTypes: de.MimeType, File: rel, ItemID: id})
			s.add(&Item{ID: id, Category: "webapps", Kind: KindWebapp, Label: name, Detail: url,
				Paths: paths, Size: size, Selected: true, Requires: []string{"app:google-chrome"}})
		default:
			kind, label, req := "custom", name, []string(nil)
			switch {
			case strings.Contains(de.Exec, "xdg-terminal-exec") && strings.Contains(de.Exec, " -e "):
				kind, label = "tui", name+i18n.T(" (terminálos indító)")
			case strings.HasPrefix(d.Name(), "chrome-") || strings.Contains(de.Exec, "--app-id="):
				kind, label, req = "pwa", name+i18n.T(" (Chrome-alkalmazás)"), []string{"app:google-chrome"}
				for _, extra := range []string{".local/share/desktop-directories/chrome-apps.directory", ".config/menus/applications-merged/user-chrome-apps.menu"} {
					if sys.Exists(e.Abs(extra)) {
						paths = append(paths, extra)
					}
				}
			case strings.HasPrefix(de.Exec, "omarchy-webapp-handler"):
				kind, label = "custom", name+i18n.T(" (webapp-kezelő)")
			default:
				label = name + i18n.T(" (saját indító)")
			}
			id := "launcher:" + d.Name()
			s.inv.Replay.Launchers = append(s.inv.Replay.Launchers, manifest.Launcher{File: rel, Name: name, Kind: kind, ItemID: id})
			s.add(&Item{ID: id, Category: "webapps", Kind: KindFiles, Label: label, Detail: de.Exec,
				Paths: paths, Size: size, Selected: true, Requires: req})
		}
	}
}

// iconFiles finds user icon files for an Icon= value.
func (s *Scanner) iconFiles(icon string) []string {
	if icon == "" {
		return nil
	}
	if filepath.IsAbs(icon) {
		if strings.HasPrefix(icon, s.Env.Home+"/") && sys.Exists(icon) {
			return []string{s.Env.Rel(icon)}
		}
		return nil
	}
	var out []string
	base := filepath.Join(s.Env.Home, ".local/share/icons")
	_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n := d.Name()
			if strings.TrimSuffix(n, filepath.Ext(n)) == icon {
				out = append(out, s.Env.Rel(p))
			}
		}
		return nil
	})
	return out
}

// Desktop is a parsed .desktop entry (main group only).
type Desktop struct {
	Name, Exec, Icon, MimeType, StartupWMClass string
}

// ParseDesktop reads the [Desktop Entry] group of a .desktop file.
func ParseDesktop(p string) Desktop {
	var d Desktop
	f, err := os.Open(p)
	if err != nil {
		return d
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	inMain := false
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(l, "[") {
			inMain = l == "[Desktop Entry]"
			continue
		}
		if !inMain {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "Name":
			d.Name = strings.TrimSpace(v)
		case "Exec":
			d.Exec = strings.TrimSpace(v)
		case "Icon":
			d.Icon = strings.TrimSpace(v)
		case "MimeType":
			d.MimeType = strings.TrimSpace(v)
		case "StartupWMClass":
			d.StartupWMClass = strings.TrimSpace(v)
		}
	}
	return d
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		s = s[1 : len(s)-1]
	}
	return strings.ReplaceAll(s, `\\`, `\`)
}

// ───────────────────────── seeds (Omarchy config files) ─────────────────────────

func seedKey(rel string) string {
	parts := strings.Split(rel, "/")
	switch {
	case len(parts) == 1:
		return rel
	case parts[0] == ".config" && len(parts) >= 2:
		return ".config/" + parts[1]
	case len(parts) >= 3:
		return strings.Join(parts[:3], "/")
	}
	return rel
}

func (s *Scanner) scanSeeds() {
	e := s.Env
	type group struct {
		files  []string
		states []string
	}
	groups := map[string]*group{}
	addTo := func(rel, state, seedSHA, userSHA string) {
		k := seedKey(rel)
		g := groups[k]
		if g == nil {
			g = &group{}
			groups[k] = g
		}
		g.files = append(g.files, rel)
		g.states = append(g.states, state)
		s.inv.Seeds = append(s.inv.Seeds, manifest.Seed{Path: rel, State: state, SeedSHA: seedSHA, UserSHA: userSHA, ItemID: "seed:" + k})
	}
	skelFiles := map[string]bool{}
	skelDirs := map[string]bool{}
	_ = filepath.WalkDir(e.SkelDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(e.SkelDir, p)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(rel, ".local/state/omarchy") || rel == ".local/share/applications" {
				return fs.SkipDir
			}
			skelDirs[rel] = true
			return nil
		}
		skelFiles[rel] = true
		if s.isInsideClaimed(rel) {
			return nil
		}
		home := filepath.Join(e.Home, rel)
		if _, err := os.Lstat(home); err != nil {
			return nil // user deleted a default file; nothing to back up
		}
		hs, ss := FileSHA(home), FileSHA(p)
		if hs == ss {
			return nil
		}
		addTo(rel, "modified", ss, hs)
		if b, err := os.ReadFile(p); err == nil {
			s.inv.SeedContents[rel] = b
		}
		return nil
	})
	// user-only files inside seed directories under ~/.config (one level of seed dirs)
	for dir := range skelDirs {
		if !strings.HasPrefix(dir, ".config/") || strings.Count(dir, "/") != 1 {
			continue
		}
		if s.isInsideClaimed(dir) {
			continue
		}
		root := filepath.Join(e.Home, dir)
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel := e.Rel(p)
			if d.IsDir() {
				if p != root && (s.isInsideClaimed(rel) || s.matcher.Match(p)) {
					return fs.SkipDir
				}
				return nil
			}
			if skelFiles[rel] || s.isInsideClaimed(rel) || s.matcher.Match(p) {
				return nil
			}
			if d.Type()&(fs.ModeSocket|fs.ModeNamedPipe) != 0 {
				return nil
			}
			if strings.HasSuffix(rel, ".bak") || strings.Contains(rel, ".bak.") {
				return nil
			}
			addTo(rel, "useronly", "", FileSHA(p))
			return nil
		})
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		g := groups[k]
		label, ok := omarchySeedKeys[k]
		label = i18n.T(label)
		cat := "omarchy"
		if !ok {
			label = k
			cat = "apps"
		}
		var detail []string
		for i, f := range g.files {
			tag := i18n.T("módosított")
			if g.states[i] == "useronly" {
				tag = i18n.T("saját")
			}
			detail = append(detail, fmt.Sprintf("%s (%s)", strings.TrimPrefix(f, k+"/"), tag))
		}
		size, _ := s.measure(g.files, nil)
		s.add(&Item{ID: "seed:" + k, Category: cat, Kind: KindSeed, Label: label,
			Detail: i18n.T("Csak az eltérések az Omarchy alapbeállításaitól:\n") + strings.Join(detail, "\n"),
			Paths:  g.files, Size: size, Selected: true})
	}
	s.claim(".config/omarchy")
	for dir := range skelDirs {
		if strings.HasPrefix(dir, ".config/") && strings.Count(dir, "/") == 1 {
			s.claim(dir)
		}
	}
	for f := range skelFiles {
		if !strings.Contains(f, "/") || strings.HasPrefix(f, ".config/") && strings.Count(f, "/") == 1 {
			s.claim(f)
		}
	}
}

// ───────────────────────── packages ─────────────────────────

func (s *Scanner) scanPackages(ctx context.Context) error {
	pkgs, err := sys.ExplicitPackages(ctx, s.Env)
	if err != nil {
		return err
	}
	pkgs = sys.ClassifyPackages(ctx, s.Env, pkgs)
	s.inv.Packages = pkgs
	for _, p := range pkgs {
		if p.Class != "user" {
			continue
		}
		label := p.Name
		detail := i18n.T("Visszaállításkor a legfrissebb verzió települ.")
		if p.Foreign {
			label += " (AUR)"
		}
		if p.Hardware != "" {
			label += fmt.Sprintf(i18n.T(" – csak %s hardveren"), hwName(p.Hardware))
			detail += fmt.Sprintf(i18n.T(" Hardverfüggő csomag: csak akkor települ, ha az új gépen is %s van."), hwName(p.Hardware))
		}
		s.add(&Item{ID: "pkg:" + p.Name, Category: "packages", Kind: KindPackage, Label: label,
			Detail: detail, Hardware: p.Hardware, Packages: []string{p.Name}, Selected: true})
	}
	return nil
}

func hwName(t string) string {
	switch t {
	case "nvidia":
		return i18n.T("NVIDIA videokártyás")
	case "amd":
		return i18n.T("AMD videokártyás")
	case "intel":
		return i18n.T("Intel videokártyás")
	case "amd-cpu":
		return i18n.T("AMD processzoros")
	case "intel-cpu":
		return i18n.T("Intel processzoros")
	case "same-board":
		return i18n.T("ugyanazon a")
	}
	if strings.HasPrefix(t, "bt:") {
		return i18n.T("ugyanazzal a Bluetooth-adapterrel rendelkező")
	}
	return t
}

// HardwareName is exported for the UI.
func HardwareName(t string) string { return hwName(t) }

// ───────────────────────── git projects ─────────────────────────

func (s *Scanner) findRepos() []string {
	var repos []string
	ents, _ := os.ReadDir(s.Env.Home)
	for _, d := range ents {
		if strings.HasPrefix(d.Name(), ".") || !d.IsDir() {
			continue
		}
		root := filepath.Join(s.Env.Home, d.Name())
		_ = filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
			if err != nil || !de.IsDir() {
				return nil
			}
			depth := strings.Count(strings.TrimPrefix(p, s.Env.Home), "/")
			if depth > 4 {
				return fs.SkipDir
			}
			base := de.Name()
			if base == "node_modules" || base == "venv" || base == ".venv" {
				return fs.SkipDir
			}
			if sys.Exists(filepath.Join(p, ".git")) {
				repos = append(repos, p)
				return fs.SkipDir
			}
			return nil
		})
	}
	return repos
}

func (s *Scanner) scanGit(ctx context.Context) {
	e := s.Env
	rules := map[string]catalog.GitProject{}
	for _, g := range s.Catalog.GitProjects {
		rules[e.Rel(e.Abs(g.Path))] = g
	}
	for _, repo := range s.findRepos() {
		rel := e.Rel(repo)
		rule := rules[rel]
		g := manifest.GitProject{Path: rel, Recipe: rule.Recipe, ItemID: "git:" + rel}
		g.Remote, _ = e.Out(ctx, "git", "-C", repo, "remote", "get-url", "origin")
		g.Branch, _ = e.Out(ctx, "git", "-C", repo, "rev-parse", "--abbrev-ref", "HEAD")
		g.Commit, _ = e.Out(ctx, "git", "-C", repo, "rev-parse", "HEAD")
		dirty, _ := e.Out(ctx, "git", "-C", repo, "status", "--porcelain", "--untracked-files=no")
		pushed := false
		if g.Remote != "" && g.Commit != "" {
			if out, err := e.Out(ctx, "git", "-C", repo, "branch", "-r", "--contains", g.Commit); err == nil && strings.TrimSpace(out) != "" {
				pushed = true
			}
		}
		g.Clean = g.Remote != "" && dirty == "" && pushed
		heavy := append([]string{}, heavyProjectDirs...)
		heavy = append(heavy, rule.Exclude...)
		var excl []string
		for _, h := range heavy {
			excl = append(excl, filepath.Join(repo, "**", h))
		}
		it := &Item{ID: g.ItemID, Category: "packages", Kind: KindGit, Selected: true}
		if g.Clean {
			kept := s.keptFiles(ctx, repo, rule, heavy)
			g.KeptFiles = kept
			for _, k := range kept {
				it.Paths = append(it.Paths, filepath.Join(rel, k))
			}
			it.Size, _ = s.measure(it.Paths, nil)
			it.Label = fmt.Sprintf(i18n.T("Git-projekt: %s (újra letöltve innen: %s)"), rel, g.Remote)
			it.Detail = fmt.Sprintf(i18n.T("Ág: %s. A verziókezelt fájlokat a GitHubról töltjük le, csak a helyi, nem verziókezelt fájlok kerülnek a mentésbe (%d db)."), g.Branch, len(kept))
		} else {
			g.Full = true
			it.Paths = []string{rel}
			it.Excludes = excl
			it.Size, _ = s.measure(it.Paths, excl)
			reason := i18n.T("nincs feltöltve távoli tárolóba")
			if g.Remote != "" && dirty != "" {
				reason = i18n.T("nem mentett (commitolatlan) változásai vannak")
			} else if g.Remote != "" && !pushed {
				reason = i18n.T("a legutóbbi commit nincs feltöltve")
			}
			it.Label = fmt.Sprintf(i18n.T("Git-projekt: %s (teljes mentés)"), rel)
			it.Detail = i18n.T("Teljes mentés, mert ") + reason + i18n.T(". A nehéz, újragenerálható mappák (venv, node_modules, dist…) kimaradnak.")
		}
		if rule.Recipe != "" {
			it.Detail += i18n.T("\nTelepítő recept: ") + rule.Recipe + i18n.T(" (visszaállításkor felajánljuk).")
		}
		if note := rule.Note; note != "" {
			if i18n.Lang() == i18n.EN && rule.NoteEN != "" {
				note = rule.NoteEN
			}
			it.Detail += "\n" + note
		}
		s.inv.GitProjects = append(s.inv.GitProjects, g)
		s.inv.Items = append(s.inv.Items, it)
		s.claim(rel)
	}
}

func (s *Scanner) keptFiles(ctx context.Context, repo string, rule catalog.GitProject, heavy []string) []string {
	set := map[string]bool{}
	for _, k := range rule.KeepUntracked {
		if sys.Exists(filepath.Join(repo, k)) {
			set[k] = true
		}
	}
	heavySet := toSet(heavy)
	inHeavy := func(f string) bool {
		for _, part := range strings.Split(f, "/") {
			if heavySet[part] {
				return true
			}
		}
		return false
	}
	for _, args := range [][]string{
		{"ls-files", "--others", "--exclude-standard"},
		{"ls-files", "--others", "--ignored", "--exclude-standard"},
	} {
		out, err := s.Env.Out(ctx, "git", append([]string{"-C", repo}, args...)...)
		if err != nil {
			continue
		}
		for _, f := range strings.Split(out, "\n") {
			f = strings.TrimSpace(f)
			if f == "" || inHeavy(f) || strings.HasSuffix(f, ".pyc") || s.matcher.Match(filepath.Join(repo, f)) {
				continue
			}
			if st, err := os.Lstat(filepath.Join(repo, f)); err == nil && st.Mode().IsRegular() && st.Size() < 10<<20 {
				set[f] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ───────────────────────── personal files ─────────────────────────

func (s *Scanner) scanFiles() {
	e := s.Env
	ents, _ := os.ReadDir(e.Home)
	for _, d := range ents {
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		rel := name
		if s.isInsideClaimed(rel) {
			continue
		}
		for _, p := range s.expandAround(rel) {
			st := Measure(e.Abs(p), NewMatcher(s.inv.GlobalExcludes))
			size, special := st.Size, st.Special
			if st.Files == 0 {
				continue
			}
			s.add(&Item{ID: "files:" + p, Category: "files", Kind: KindFiles, Label: "~/" + p,
				Size: size, Paths: []string{p}, Excludes: special, Selected: true})
		}
	}
}

// expandAround returns rel itself, or – when it contains claimed paths (e.g. git
// projects) – its children that are not claimed, recursively.
func (s *Scanner) expandAround(rel string) []string {
	if !s.isClaimed(rel) {
		return []string{rel}
	}
	if s.isInsideClaimed(rel) {
		return nil
	}
	var out []string
	for _, n := range listNames(s.Env.Abs(rel)) {
		out = append(out, s.expandAround(filepath.Join(rel, n))...)
	}
	return out
}

// ───────────────────────── other (unknown) ─────────────────────────

func (s *Scanner) scanOther() {
	e := s.Env
	var cands []string
	for _, root := range []string{".config", ".local/share", ".local/state"} {
		for _, n := range listNames(e.Abs(root)) {
			cands = append(cands, filepath.Join(root, n))
		}
	}
	for _, n := range listNames(e.Abs(".local")) {
		if n != "share" && n != "state" {
			cands = append(cands, filepath.Join(".local", n))
		}
	}
	for _, n := range listNames(e.Home) {
		if strings.HasPrefix(n, ".") && n != ".config" && n != ".local" && n != ".cache" {
			cands = append(cands, n)
		}
	}
	for _, rel := range cands {
		if s.isClaimed(rel) {
			continue
		}
		abs := e.Abs(rel)
		if s.matcher.Match(abs) {
			continue
		}
		if onlyNativeMessaging(abs) {
			continue
		}
		act := s.Override.Get(rel)
		if act == "never" || act == "cache" {
			continue
		}
		size, special := s.measure([]string{rel}, nil)
		if size == 0 && act == "" {
			continue
		}
		sel := size < 50<<20
		if act == "always" {
			sel = true
		}
		s.add(&Item{ID: "other:" + rel, Category: "other", Kind: KindFiles, Label: "~/" + rel,
			Detail: i18n.T("Ismeretlen alkalmazás adatai. Jelöld meg, ha mindig vagy soha nem kell menteni."),
			Size:   size, Paths: []string{rel}, Excludes: special, Selected: sel})
	}
}

func onlyNativeMessaging(dir string) bool {
	names := listNames(dir)
	return len(names) == 1 && names[0] == "NativeMessagingHosts"
}

// ───────────────────────── compare with previous backup ─────────────────────────

func (s *Scanner) compare() {
	if s.Prev == nil {
		return
	}
	prev := map[string]manifest.Item{}
	for _, it := range s.Prev.Items {
		prev[it.ID] = it
	}
	prevSeeds := map[string]string{}
	for _, sd := range s.Prev.Seeds {
		prevSeeds[sd.Path] = sd.UserSHA
	}
	now := map[string]bool{}
	for _, it := range s.inv.Items {
		now[it.ID] = true
		p, ok := prev[it.ID]
		if !ok {
			s.inv.Status[it.ID] = "new"
			continue
		}
		switch it.Kind {
		case KindSeed:
			for _, sd := range s.inv.Seeds {
				if sd.ItemID == it.ID && prevSeeds[sd.Path] != sd.UserSHA {
					s.inv.Status[it.ID] = "changed"
					break
				}
			}
		case KindReplay:
			if p.Label != it.Label {
				s.inv.Status[it.ID] = "changed"
			}
		}
	}
	for _, it := range s.Prev.Items {
		if it.Selected && !now[it.ID] {
			s.inv.Removed = append(s.inv.Removed, it.Label)
		}
	}
	// restore the previous selection for known items
	known := map[string]bool{}
	sel := map[string]bool{}
	for _, it := range s.Prev.Items {
		known[it.ID] = true
		sel[it.ID] = it.Selected
	}
	s.inv.ApplySelection(sel, known)
}

// ───────────────────────── helpers ─────────────────────────

func listNames(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(ents))
	for _, d := range ents {
		out = append(out, d.Name())
	}
	sort.Strings(out)
	return out
}

func toSet(xs []string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}
