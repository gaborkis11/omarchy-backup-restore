// Package manifest defines manifest.json, the description of one backup run
// stored in the run's meta snapshot.
package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/gaborkis11/omarchy-backup-restore/internal/sys"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

// Schema is bumped on incompatible changes.
const Schema = 1

// Manifest describes a backup run.
type Manifest struct {
	Schema       int       `json:"schema"`
	RunID        string    `json:"run_id"`
	Created      time.Time `json:"created"`
	DataSnapshot string    `json:"data_snapshot,omitempty"` // filled in by the meta step
	ObrVersion   string    `json:"obr_version"`

	Source Source `json:"source"`

	// Items is every scanned item with its selection state, so the restore
	// side can offer exactly what was backed up and the next backup can mark
	// what is new.
	Items []Item `json:"items"`

	Packages    []sys.Package `json:"packages"` // all explicit packages, classified
	Seeds       []Seed        `json:"seeds"`
	Replay      Replay        `json:"replay"`
	System      System        `json:"system"`
	GitProjects []GitProject  `json:"git_projects"`
	Migrations  []string      `json:"migrations"` // marker names present at backup time

	Warnings []string `json:"warnings,omitempty"`
	Stats    Stats    `json:"stats"`
}

// Source describes the machine the backup was made on.
type Source struct {
	Host           string       `json:"host"`
	User           string       `json:"user"`
	UID            int          `json:"uid"`
	Home           string       `json:"home"`
	OmarchyVersion string       `json:"omarchy_version"`
	Hardware       sys.Hardware `json:"hardware"`
}

// Item is the serialisable form of an inventory item.
type Item struct {
	ID        string   `json:"id"`
	Category  string   `json:"category"`
	Kind      string   `json:"kind"`
	Label     string   `json:"label"`
	Detail    string   `json:"detail,omitempty"`
	Size      int64    `json:"size"`
	Paths     []string `json:"paths,omitempty"` // home-relative
	Excludes  []string `json:"excludes,omitempty"`
	Secret    bool     `json:"secret,omitempty"`
	Hardware  string   `json:"hardware,omitempty"`
	Requires  []string `json:"requires,omitempty"`
	Replace   bool     `json:"replace,omitempty"`
	Packages  []string `json:"packages,omitempty"`
	Running   []string `json:"running,omitempty"`
	Post      []string `json:"post_restore,omitempty"`
	Selected  bool     `json:"selected"`
	NeedsRoot bool     `json:"needs_root,omitempty"`
}

// Seed records an Omarchy config file the user changed relative to /etc/skel.
type Seed struct {
	Path    string `json:"path"`               // home-relative
	State   string `json:"state"`              // modified | useronly
	SeedSHA string `json:"seed_sha,omitempty"` // sha256 of the skel version at backup time
	UserSHA string `json:"user_sha"`
	ItemID  string `json:"item_id"`
	// The skel version itself is stored in the meta snapshot under seeds/<path>.
}

// Replay holds generated state that is re-applied with Omarchy's own commands.
type Replay struct {
	Theme       string      `json:"theme,omitempty"`
	UserThemes  []UserTheme `json:"user_themes,omitempty"`
	HyprToggles []string    `json:"hypr_toggles,omitempty"`
	FlagToggles []string    `json:"flag_toggles,omitempty"`
	Webapps     []Webapp    `json:"webapps,omitempty"`
	Launchers   []Launcher  `json:"launchers,omitempty"`
	DconfDump   bool        `json:"dconf_dump,omitempty"` // dconf.ini stored in meta
	Font        string      `json:"font,omitempty"`
	OmarchyLang string      `json:"omarchy_language,omitempty"`
}

// UserTheme is a theme in ~/.config/omarchy/themes.
type UserTheme struct {
	Name   string `json:"name"`
	GitURL string `json:"git_url,omitempty"` // when cloned; otherwise files are in the data snapshot
}

// Webapp is an Omarchy webapp launcher.
type Webapp struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	Icon       string `json:"icon"`
	CustomExec string `json:"custom_exec,omitempty"`
	MimeTypes  string `json:"mime_types,omitempty"`
	File       string `json:"file"` // home-relative .desktop path
	ItemID     string `json:"item_id"`
}

// Launcher is any other user .desktop file (TUI launchers, PWAs, custom).
type Launcher struct {
	File   string `json:"file"` // home-relative
	Name   string `json:"name"`
	Kind   string `json:"kind"` // tui | pwa | custom
	ItemID string `json:"item_id"`
}

// System holds system-level state.
type System struct {
	Units       []string   `json:"units,omitempty"`      // system units the user enabled
	UserUnits   []string   `json:"user_units,omitempty"` // user units the user enabled
	Groups      []string   `json:"groups,omitempty"`
	Keymap      string     `json:"keymap,omitempty"`
	X11Layout   string     `json:"x11_layout,omitempty"`
	X11Model    string     `json:"x11_model,omitempty"`
	X11Variant  string     `json:"x11_variant,omitempty"`
	X11Options  string     `json:"x11_options,omitempty"`
	Timezone    string     `json:"timezone,omitempty"`
	Lang        string     `json:"lang,omitempty"`
	Hostname    string     `json:"hostname,omitempty"`
	Connections []string   `json:"connections,omitempty"` // NM connection names (files in system.tar)
	Bluetooth   []BTDevice `json:"bluetooth,omitempty"`
	BTAdapter   string     `json:"bt_adapter,omitempty"`
	Printers    []string   `json:"printers,omitempty"`
	EtcFiles    []string   `json:"etc_files,omitempty"` // extra /etc files chosen by the user
	Collected   bool       `json:"collected"`           // system.tar present in meta
}

// BTDevice is a paired Bluetooth device.
type BTDevice struct {
	MAC  string `json:"mac"`
	Name string `json:"name"`
}

// GitProject is a git checkout restored by cloning.
type GitProject struct {
	Path      string   `json:"path"` // home-relative
	Remote    string   `json:"remote,omitempty"`
	Branch    string   `json:"branch,omitempty"`
	Commit    string   `json:"commit,omitempty"`
	Clean     bool     `json:"clean"`                // tree clean and HEAD pushed
	Full      bool     `json:"full"`                 // whole tree is in the data snapshot
	KeptFiles []string `json:"kept_files,omitempty"` // project-relative untracked files in the snapshot
	Recipe    string   `json:"recipe,omitempty"`
	ItemID    string   `json:"item_id"`
}

// Stats about the run.
type Stats struct {
	FilesNew       int64   `json:"files_new"`
	FilesChanged   int64   `json:"files_changed"`
	FilesUnchanged int64   `json:"files_unmodified"`
	BytesProcessed int64   `json:"bytes_processed"`
	BytesAdded     int64   `json:"bytes_added"`
	Seconds        float64 `json:"seconds"`
	Incomplete     bool    `json:"incomplete,omitempty"` // restic exit 3
}

// New creates an empty manifest.
func New(runID string) *Manifest {
	return &Manifest{Schema: Schema, RunID: runID, Created: time.Now()}
}

// Write saves the manifest as indented JSON.
func (m *Manifest) Write(path string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// Parse decodes a manifest and checks the schema.
func Parse(b []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if m.Schema > Schema {
		return nil, fmt.Errorf(i18n.T("ezt a mentést egy újabb obr készítette (séma %d); frissítsd a programot"), m.Schema)
	}
	return &m, nil
}

// Item returns an item by id.
func (m *Manifest) Item(id string) (Item, bool) {
	for _, it := range m.Items {
		if it.ID == id {
			return it, true
		}
	}
	return Item{}, false
}

// SelectedIDs returns the ids of selected items.
func (m *Manifest) SelectedIDs() map[string]bool {
	out := map[string]bool{}
	for _, it := range m.Items {
		if it.Selected {
			out[it.ID] = true
		}
	}
	return out
}
