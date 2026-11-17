// Package tui is the Bubble Tea terminal interface of obr.
package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gaborkis11/omarchy-backup-restore/internal/catalog"
	"github.com/gaborkis11/omarchy-backup-restore/internal/config"
	"github.com/gaborkis11/omarchy-backup-restore/internal/sys"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

// Deps are handed over by the CLI.
type Deps struct {
	Env            *sys.Env
	Catalog        *catalog.Catalog
	Overrides      *catalog.Overrides
	Config         *config.Config
	LogPath        string
	Mode           string // "" | backup | restore
	Source         string // restore: local omarchy-backup folder
	EnableSchedule func(dest, when string) error
}

// CategoryTitle returns the Hungarian category name.
func CategoryTitle(c string) string {
	switch c {
	case "omarchy":
		return i18n.T("Omarchy és asztal")
	case "webapps":
		return i18n.T("Webappok és indítók")
	case "packages":
		return i18n.T("Programok és git-projektek")
	case "apps":
		return i18n.T("Alkalmazások beállításai és adatai")
	case "secrets":
		return i18n.T("Kulcsok és belépések")
	case "system":
		return i18n.T("Rendszer")
	case "files":
		return i18n.T("Személyes fájlok")
	case "models":
		return i18n.T("AI-modellek")
	case "other":
		return i18n.T("Egyéb (ismeretlen programok adatai)")
	}
	return c
}

// CategoryHint is a one-line explanation per category.
func CategoryHint(c string) string {
	switch c {
	case "omarchy":
		return i18n.T("Csak az Omarchy alapbeállításaitól eltérő részek; a téma és a kapcsolók az Omarchy parancsaival állnak vissza.")
	case "webapps":
		return i18n.T("Saját webappok és indítók; visszaállításkor az Omarchy újra létrehozza őket.")
	case "packages":
		return i18n.T("A program a csomagok listáját menti; visszaállításkor a legfrissebb verzió települ.")
	case "apps":
		return i18n.T("Beállítások és adatok; a gyorsítótárak kimaradnak.")
	case "secrets":
		return i18n.T("Jelszavak, tokenek, kulcsok – a mentés titkosított.")
	case "system":
		return i18n.T("Szolgáltatások, csoportok, billentyűzet, wifi, Bluetooth. Egy része rendszergazdai jogot kér.")
	case "files":
		return i18n.T("A home mappa nem rejtett mappái.")
	case "models":
		return i18n.T("Nagy, újra letölthető modellek – alapból nem mentjük.")
	case "other":
		return i18n.T("Amit a program nem ismer. Kicsik alapból mentve; jelöld meg: m = mindig, s = soha, c = gyorsítótár.")
	}
	return ""
}

// ScheduleWarning explains what storing a passphrase means.
// ScheduleWarning explains (in the current language) what storing a passphrase means.
func ScheduleWarning() string { return i18n.Text("schedule-warning") }

// LearnHostKey fetches the SFTP host key of a NAS into ~/.config/obr/known_hosts
// (trust on first use).
func LearnHostKey(ctx context.Context, env *sys.Env, d *config.Destination) error {
	port := d.Port
	if port == 0 {
		port = 22
	}
	out, err := env.Out(ctx, "ssh-keyscan", "-T", "5", "-p", fmt.Sprint(port), d.Host)
	if err != nil || strings.TrimSpace(out) == "" {
		return fmt.Errorf("ssh-keyscan: %v", err)
	}
	if err := os.MkdirAll(env.ConfigDir, 0o700); err != nil {
		return err
	}
	kh := filepath.Join(env.ConfigDir, "known_hosts")
	old, _ := os.ReadFile(kh)
	var keep []string
	for _, l := range strings.Split(string(old), "\n") {
		if l != "" && !strings.HasPrefix(l, d.Host+" ") && !strings.HasPrefix(l, "["+d.Host+"]") {
			keep = append(keep, l)
		}
	}
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			keep = append(keep, l)
		}
	}
	return os.WriteFile(kh, []byte(strings.Join(keep, "\n")+"\n"), 0o600)
}
