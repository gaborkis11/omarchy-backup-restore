// Package catalog loads the embedded application catalog and the user's overrides.
package catalog

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

//go:embed catalog.toml
var embedded string

// App describes one known application.
type App struct {
	ID          string   `toml:"id"`
	Label       string   `toml:"label"`
	LabelEN     string   `toml:"label_en"`
	NoteEN      string   `toml:"note_en"`
	SecretEN    string   `toml:"secret_label_en"`
	Category    string   `toml:"category"`
	Packages    []string `toml:"packages"`
	Running     []string `toml:"running"`
	Paths       []string `toml:"paths"`
	Exclude     []string `toml:"exclude"`
	SecretPaths []string `toml:"secret_paths"`
	SecretLabel string   `toml:"secret_label"`
	Requires    []string `toml:"requires"`
	Replace     bool     `toml:"replace"`
	DefaultOn   *bool    `toml:"default_on"`
	Hardware    string   `toml:"hardware"`
	PostRestore []string `toml:"post_restore"`
	Note        string   `toml:"note"`
}

// Localized returns label, note and secret label in the current language.
func (a App) Localized() (label, note, secret string) {
	label, note, secret = a.Label, a.Note, a.SecretLabel
	if i18n.Lang() == i18n.EN {
		if a.LabelEN != "" {
			label = a.LabelEN
		}
		if a.NoteEN != "" {
			note = a.NoteEN
		}
		if a.SecretEN != "" {
			secret = a.SecretEN
		}
	}
	return
}

// On reports the default selection.
func (a App) On() bool { return a.DefaultOn == nil || *a.DefaultOn }

// GitProject holds special rules for a git checkout.
type GitProject struct {
	Path          string   `toml:"path"`
	KeepUntracked []string `toml:"keep_untracked"`
	Exclude       []string `toml:"exclude"`
	Recipe        string   `toml:"recipe"`
	Note          string   `toml:"note"`
	NoteEN        string   `toml:"note_en"`
}

// Catalog is the parsed catalog.
type Catalog struct {
	Apps        []App        `toml:"app"`
	GitProjects []GitProject `toml:"gitproject"`
}

// Override is a user decision about an "Egyéb" entry.
type Override struct {
	Path   string `toml:"path"`   // home-relative
	Action string `toml:"action"` // always | never | cache
}

// Overrides file (~/.config/obr/overrides.toml).
type Overrides struct {
	Entries []Override `toml:"entry"`
}

// Load parses the embedded catalog.
func Load() (*Catalog, error) {
	var c Catalog
	if _, err := toml.Decode(embedded, &c); err != nil {
		return nil, fmt.Errorf(i18n.T("katalógus: %w"), err)
	}
	return &c, nil
}

// App returns an app by id.
func (c *Catalog) App(id string) (App, bool) {
	for _, a := range c.Apps {
		if a.ID == id {
			return a, true
		}
	}
	return App{}, false
}

// LoadOverrides reads the user's overrides (missing file = empty).
func LoadOverrides(configDir string) (*Overrides, error) {
	var o Overrides
	p := filepath.Join(configDir, "overrides.toml")
	if _, err := os.Stat(p); err != nil {
		return &o, nil
	}
	if _, err := toml.DecodeFile(p, &o); err != nil {
		return &o, fmt.Errorf("%s: %w", p, err)
	}
	return &o, nil
}

// Get returns the action for a path ("" when none).
func (o *Overrides) Get(path string) string {
	for _, e := range o.Entries {
		if e.Path == path {
			return e.Action
		}
	}
	return ""
}

// Set records an action ("" removes it).
func (o *Overrides) Set(path, action string) {
	out := o.Entries[:0]
	for _, e := range o.Entries {
		if e.Path != path {
			out = append(out, e)
		}
	}
	if action != "" {
		out = append(out, Override{Path: path, Action: action})
	}
	o.Entries = out
}

// Save writes the overrides file.
func (o *Overrides) Save(configDir string) error {
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return err
	}
	p := filepath.Join(configDir, "overrides.toml")
	f, err := os.OpenFile(p+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	fmt.Fprintln(f, i18n.T("# obr: döntések az „Egyéb\" tételekről (always = mindig mentsd, never = soha, cache = gyorsítótár)"))
	if err := toml.NewEncoder(f).Encode(o); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}
