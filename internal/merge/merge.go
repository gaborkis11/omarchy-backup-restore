// Package merge does three-way merges of Omarchy config files and path fixups
// after a restore onto a different user/home.
package merge

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gaborkis11/omarchy-backup-restore/internal/runner"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

// Result of a three-way merge.
type Result struct {
	Merged    []byte
	Conflicts bool
}

// ThreeWay merges ours (the user's backed-up file) and theirs (the new Omarchy
// default) using base (the Omarchy default at backup time). It uses
// `git merge-file -p`, which exits with the number of conflicts.
func ThreeWay(ctx context.Context, r runner.Runner, tmpDir string, ours, base, theirs []byte) (Result, error) {
	dir, err := os.MkdirTemp(tmpDir, "obr-merge-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)
	o, b, t := filepath.Join(dir, "ours"), filepath.Join(dir, "base"), filepath.Join(dir, "theirs")
	for p, c := range map[string][]byte{o: ours, b: base, t: theirs} {
		if err := os.WriteFile(p, c, 0o600); err != nil {
			return Result{}, err
		}
	}
	res, err := r.Run(ctx, runner.Cmd{Name: "git", Args: []string{"merge-file", "-p",
		"-L", i18n.T("a te beállításod"), "-L", i18n.T("régi alap"), "-L", i18n.T("új Omarchy alap"), o, b, t}})
	if err != nil {
		if c := runner.Code(err); c > 0 && c < 127 {
			return Result{Merged: res.Stdout, Conflicts: true}, nil
		}
		return Result{}, err
	}
	return Result{Merged: res.Stdout}, nil
}

// SchemaVersion extracts the top-level "version" from a JSON document (-1 if none).
func SchemaVersion(b []byte) int {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return -1
	}
	if v, ok := m["version"].(float64); ok {
		return int(v)
	}
	return -1
}

// Diff returns a unified diff of a and b (via `git diff --no-index`).
func Diff(ctx context.Context, r runner.Runner, tmpDir string, a, b []byte, la, lb string) string {
	dir, err := os.MkdirTemp(tmpDir, "obr-diff-")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(dir)
	pa, pb := filepath.Join(dir, la), filepath.Join(dir, lb)
	_ = os.WriteFile(pa, a, 0o600)
	_ = os.WriteFile(pb, b, 0o600)
	res, _ := r.Run(ctx, runner.Cmd{Name: "git", Args: []string{"diff", "--no-index", "--no-color", "--", la, lb}, Dir: dir})
	return string(res.Stdout)
}

// fixupRoots are home-relative trees whose text files may contain absolute
// paths of the old home. Big application data stores are not touched.
var fixupRoots = []string{".config", ".local/share/applications", ".bashrc", ".bash_profile", ".zshrc", ".profile", ".XCompose", ".local/state/omarchy"}

var fixupSkip = []string{".config/google-chrome", ".config/chromium", ".config/BraveSoftware", ".config/obsidian", ".config/Code", ".config/libreoffice", ".config/spotify"}

// Fixup rewrites oldHome -> newHome (and /run/user/<oldUID> -> /run/user/<newUID>)
// in small text files and symlinks under the given home-relative paths.
// It returns the files that were changed.
func Fixup(home string, rels []string, oldHome, newHome string, oldUID, newUID int) []string {
	var changed []string
	if oldHome == newHome && oldUID == newUID {
		return nil
	}
	oldRun := "/run/user/" + itoa(oldUID) + "/"
	newRun := "/run/user/" + itoa(newUID) + "/"
	for _, rel := range rels {
		if !underAny(rel, fixupRoots) || underAny(rel, fixupSkip) {
			continue
		}
		root := filepath.Join(home, rel)
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			r, _ := filepath.Rel(home, p)
			if d.IsDir() {
				if underAny(r, fixupSkip) {
					return fs.SkipDir
				}
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 {
				if t, err := os.Readlink(p); err == nil && strings.HasPrefix(t, oldHome+"/") {
					_ = os.Remove(p)
					if os.Symlink(newHome+strings.TrimPrefix(t, oldHome), p) == nil {
						changed = append(changed, r)
					}
				}
				return nil
			}
			info, err := d.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil || bytes.IndexByte(b, 0) >= 0 {
				return nil
			}
			nb := b
			if oldHome != newHome {
				nb = bytes.ReplaceAll(nb, []byte(oldHome+"/"), []byte(newHome+"/"))
				nb = replaceExact(nb, oldHome, newHome)
			}
			if oldUID != newUID {
				nb = bytes.ReplaceAll(nb, []byte(oldRun), []byte(newRun))
			}
			if !bytes.Equal(b, nb) {
				if os.WriteFile(p, nb, info.Mode().Perm()) == nil {
					changed = append(changed, r)
				}
			}
			return nil
		})
	}
	return changed
}

// replaceExact replaces oldHome when it is followed by a quote, whitespace or end of line.
func replaceExact(b []byte, oldHome, newHome string) []byte {
	re := regexp.MustCompile(regexp.QuoteMeta(oldHome) + `(["'\s]|$)`)
	return re.ReplaceAll(b, []byte(newHome+"$1"))
}

func underAny(rel string, roots []string) bool {
	for _, r := range roots {
		if rel == r || strings.HasPrefix(rel, r+"/") {
			return true
		}
	}
	return false
}

var ghHelperRe = regexp.MustCompile(`(?m)^(\s*helper\s*=\s*!)\S*/gh(\s+auth\s+git-credential.*)$`)

// FixGitCredentialHelper replaces version-pinned gh paths (mise installs) in a
// git config with a plain `gh` that is resolved from PATH.
func FixGitCredentialHelper(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	nb := ghHelperRe.ReplaceAll(b, []byte("${1}gh${2}"))
	if bytes.Equal(b, nb) {
		return false
	}
	st, _ := os.Stat(path)
	mode := os.FileMode(0o644)
	if st != nil {
		mode = st.Mode().Perm()
	}
	return os.WriteFile(path, nb, mode) == nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
