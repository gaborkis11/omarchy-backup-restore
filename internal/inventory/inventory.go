// Package inventory scans the machine and turns everything worth keeping into
// selectable items grouped into categories.
package inventory

import (
	"sort"
	"strings"

	"github.com/gaborkis11/omarchy-backup-restore/internal/manifest"
	"github.com/gaborkis11/omarchy-backup-restore/internal/sys"
)

// Item kinds.
const (
	KindFiles   = "files"   // paths copied into the data snapshot
	KindPackage = "package" // reinstalled with pacman/yay
	KindReplay  = "replay"  // re-applied with omarchy-* commands
	KindSystem  = "system"  // system-level state (some via root helper)
	KindGit     = "git"     // git project: clone + kept files
	KindSeed    = "seed"    // Omarchy config files that differ from /etc/skel
	KindWebapp  = "webapp"
)

// Category ids in display order.
var Categories = []string{"omarchy", "webapps", "packages", "apps", "secrets", "system", "files", "models", "other"}

// Item is one selectable thing.
type Item = manifest.Item

// Inventory is the result of a scan.
type Inventory struct {
	Items []*Item

	Packages    []sys.Package
	Seeds       []manifest.Seed
	Replay      manifest.Replay
	System      manifest.System
	GitProjects []manifest.GitProject
	Migrations  []string

	// GlobalExcludes are restic exclude patterns applied to every backup.
	GlobalExcludes []string
	// Status per item id compared with the previous backup: new | changed.
	Status map[string]string
	// Removed lists labels of items that were in the previous backup but are gone now.
	Removed []string
	// Warnings collected while scanning (unreadable dirs etc.).
	Warnings []string
	// Seed contents of /etc/skel for modified seed files (home-relative path -> bytes).
	SeedContents map[string][]byte
}

// ByID returns an item.
func (inv *Inventory) ByID(id string) *Item {
	for _, it := range inv.Items {
		if it.ID == id {
			return it
		}
	}
	return nil
}

// InCategory returns the items of a category sorted for display.
func (inv *Inventory) InCategory(cat string) []*Item {
	var out []*Item
	for _, it := range inv.Items {
		if it.Category == cat {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return kindOrder(out[i].Kind) < kindOrder(out[j].Kind)
		}
		return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label)
	})
	return out
}

func kindOrder(k string) int {
	switch k {
	case KindReplay:
		return 0
	case KindSeed:
		return 1
	case KindSystem:
		return 2
	case KindPackage:
		return 3
	case KindGit:
		return 4
	}
	return 5
}

// SelectedSize sums the sizes of selected items.
func (inv *Inventory) SelectedSize() int64 {
	var n int64
	for _, it := range inv.Items {
		if it.Selected {
			n += it.Size
		}
	}
	return n
}

// Select sets the selection of an item and pulls in its requirements.
func (inv *Inventory) Select(id string, on bool) {
	it := inv.ByID(id)
	if it == nil {
		return
	}
	it.Selected = on
	if on {
		for _, r := range it.Requires {
			if dep := inv.ByID(r); dep != nil && !dep.Selected {
				inv.Select(r, true)
			}
		}
	}
}

// Missing reports requirements of selected items that are not selected.
func (inv *Inventory) Missing() map[string][]string {
	out := map[string][]string{}
	for _, it := range inv.Items {
		if !it.Selected {
			continue
		}
		for _, r := range it.Requires {
			if dep := inv.ByID(r); dep != nil && !dep.Selected {
				out[it.ID] = append(out[it.ID], r)
			}
		}
	}
	return out
}

// ApplySelection restores a previous selection: known items keep their old
// state, unknown (new) items keep their defaults.
func (inv *Inventory) ApplySelection(prev map[string]bool, known map[string]bool) {
	for _, it := range inv.Items {
		if known[it.ID] {
			it.Selected = prev[it.ID]
		}
	}
}

// NeedsRoot reports whether any selected item needs root at backup time.
func (inv *Inventory) NeedsRoot() bool {
	for _, it := range inv.Items {
		if it.Selected && it.NeedsRoot {
			return true
		}
	}
	return false
}

// RunningApps returns process names of selected items that are currently running.
func (inv *Inventory) RunningApps(isRunning func(string) bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range inv.Items {
		if !it.Selected {
			continue
		}
		for _, p := range it.Running {
			if !seen[p] && isRunning(p) {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}
