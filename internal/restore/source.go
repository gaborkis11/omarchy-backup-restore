// Package restore brings a backup back onto a (fresh) Omarchy machine.
package restore

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gaborkis11/omarchy-backup-restore/internal/engine"
	"github.com/gaborkis11/omarchy-backup-restore/internal/inventory"
	"github.com/gaborkis11/omarchy-backup-restore/internal/manifest"
	"github.com/gaborkis11/omarchy-backup-restore/internal/sys"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

// Backup is one complete backup run in a repository.
type Backup struct {
	Meta  engine.Snapshot
	RunID string
	Time  time.Time
	Host  string
}

// ListBackups returns complete runs (those with a meta snapshot), newest first.
func ListBackups(ctx context.Context, loc *engine.Location) ([]Backup, error) {
	metas, err := loc.Repo.Snapshots(ctx, "obr-meta")
	if err != nil {
		return nil, err
	}
	var out []Backup
	for _, s := range metas {
		host := s.Tag("src-")
		if host == "" {
			host = s.Hostname
		}
		out = append(out, Backup{Meta: s, RunID: s.Tag("run-"), Time: s.Time, Host: host})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out, nil
}

// Source is a loaded backup ready to be restored.
type Source struct {
	Loc      *engine.Location
	Backup   Backup
	Manifest *manifest.Manifest
	WorkDir  string // ~/.local/state/obr/restore-<ts>
	MetaDir  string // restored meta snapshot
}

// Load restores the meta snapshot of a backup into a work directory and parses the manifest.
func Load(ctx context.Context, env *sys.Env, loc *engine.Location, b Backup) (*Source, error) {
	ts := time.Now().Format("20060102-150405")
	work := filepath.Join(env.StateDir, "restore-"+ts)
	meta := filepath.Join(work, "meta")
	if err := os.MkdirAll(meta, 0o700); err != nil {
		return nil, err
	}
	if _, err := loc.Repo.Restore(ctx, engine.RestoreOpts{Snapshot: b.Meta.ID, SubPath: "/", Target: meta, TmpDir: work}, nil); err != nil {
		return nil, fmt.Errorf(i18n.T("leíró adatok letöltése: %w"), err)
	}
	raw, err := os.ReadFile(filepath.Join(meta, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf(i18n.T("a mentés leírója hiányzik: %w"), err)
	}
	m, err := manifest.Parse(raw)
	if err != nil {
		return nil, err
	}
	return &Source{Loc: loc, Backup: b, Manifest: m, WorkDir: work, MetaDir: meta}, nil
}

// LoadManifestOnly reads just the manifest of a backup (for listings).
func LoadManifestOnly(ctx context.Context, loc *engine.Location, b Backup) (*manifest.Manifest, error) {
	var buf bytes.Buffer
	if err := loc.Repo.Dump(ctx, b.Meta.ID, "/manifest.json", &buf); err != nil {
		return nil, err
	}
	return manifest.Parse(buf.Bytes())
}

// Candidate is an item that can be restored, with availability on this machine.
type Candidate struct {
	manifest.Item
	Available bool
	Reason    string // why not available / extra info
	Installed bool   // packages: already installed
}

// Candidates lists what the backup offers and what is possible here.
func Candidates(ctx context.Context, env *sys.Env, src *Source) []*Candidate {
	m := src.Manifest
	hw := sys.DetectHardware(ctx, env)
	var out []*Candidate
	for _, it := range m.Items {
		if !it.Selected {
			continue // not in the backup
		}
		c := &Candidate{Item: it, Available: true}
		if it.Hardware != "" && !hw.Satisfies(it.Hardware, m.Source.Hardware) {
			c.Available = false
			c.Reason = fmt.Sprintf(i18n.T("Más hardver: csak %s gépen állítható vissza."), inventory.HardwareName(it.Hardware))
		}
		switch {
		case it.Kind == inventory.KindPackage:
			for _, p := range it.Packages {
				if sys.Installed(ctx, env, p) {
					c.Installed = true
					c.Reason = strings.TrimSpace(c.Reason + i18n.T(" Már telepítve."))
				}
			}
		case it.NeedsRoot && !m.System.Collected:
			c.Available = false
			c.Reason = i18n.T("A mentés nem tartalmazza (mentéskor nem volt rendszergazdai jog).")
		}
		// default selection on restore
		c.Selected = c.Available
		if strings.HasPrefix(it.ID, "etc:") || it.ID == "sys:hostname" {
			c.Selected = false
		}
		if c.Installed {
			c.Selected = false
		}
		out = append(out, c)
	}
	return out
}

// Migration is an Omarchy migration that is newer than the backup.
type Migration struct {
	Name          string
	Description   string
	TouchesConfig bool
}

// PendingMigrations lists migrations present on this machine but not in the
// backup's marker list (the backed-up configs never received them).
func PendingMigrations(env *sys.Env, src *Source) []Migration {
	had := map[string]bool{}
	for _, n := range src.Manifest.Migrations {
		had[n] = true
	}
	if len(had) == 0 {
		return nil
	}
	dir := filepath.Join(env.OmarchyPath, "migrations")
	ents, _ := os.ReadDir(dir)
	var out []Migration
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".sh") || had[e.Name()] {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		s := string(b)
		mg := Migration{Name: e.Name(), TouchesConfig: strings.Contains(s, ".config/") || strings.Contains(s, "HOME/.config")}
		for _, l := range strings.Split(s, "\n") {
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "echo ") {
				mg.Description = strings.Trim(strings.TrimPrefix(l, "echo "), `"'`)
				break
			}
			if strings.HasPrefix(l, "#") && !strings.HasPrefix(l, "#!") && len(l) > 2 {
				mg.Description = strings.TrimSpace(strings.TrimPrefix(l, "#"))
				break
			}
		}
		out = append(out, mg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
