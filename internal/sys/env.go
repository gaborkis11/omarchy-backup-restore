// Package sys collects facts about the machine obr runs on: paths, Omarchy
// version, hardware fingerprint and package state.
package sys

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gaborkis11/omarchy-backup-restore/internal/runner"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

// Env carries everything a module needs to know about the machine. Every path
// that obr touches is derived from this struct so tests can redirect it.
type Env struct {
	Home        string
	User        string
	UID         int
	GID         int
	OmarchyPath string // /usr/share/omarchy
	SkelDir     string // /etc/skel
	EtcDir      string // /etc (tests: fixture)
	RuntimeDir  string // $XDG_RUNTIME_DIR (tmpfs)
	StateDir    string // ~/.local/state/obr
	ConfigDir   string // ~/.config/obr
	BinDir      string // ~/.local/share/obr/bin
	Runner      runner.Runner
	DryRun      bool
	Log         *log.Logger
	// Test hooks: when true, privileged and system-wide side effects are only
	// recorded through Runner (which is a Fake in tests).
	Sandbox bool
}

// Options for DetectEnv.
type Options struct {
	Home    string // override $HOME (tests, restore into another user)
	DryRun  bool
	Runner  runner.Runner
	LogFile io.Writer
}

// DetectEnv builds an Env for the current user.
func DetectEnv(o Options) (*Env, error) {
	u, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf(i18n.T("felhasználó lekérdezése: %w"), err)
	}
	home := o.Home
	if home == "" {
		home = os.Getenv("HOME")
	}
	if home == "" {
		home = u.HomeDir
	}
	home, _ = filepath.Abs(home)
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		rt = os.TempDir()
	}
	e := &Env{
		Home:        home,
		User:        u.Username,
		UID:         uid,
		GID:         gid,
		OmarchyPath: envOr("OMARCHY_PATH", "/usr/share/omarchy"),
		SkelDir:     envOr("OBR_SKEL_DIR", "/etc/skel"),
		EtcDir:      envOr("OBR_ETC_DIR", "/etc"),
		RuntimeDir:  rt,
		StateDir:    filepath.Join(home, ".local/state/obr"),
		ConfigDir:   filepath.Join(home, ".config/obr"),
		BinDir:      filepath.Join(home, ".local/share/obr/bin"),
		Runner:      o.Runner,
		DryRun:      o.DryRun,
		Sandbox:     os.Getenv("OBR_SANDBOX") == "1",
	}
	if e.Runner == nil {
		e.Runner = &runner.Exec{}
	}
	if o.LogFile != nil {
		e.Log = log.New(o.LogFile, "", log.LstdFlags)
	} else {
		e.Log = log.New(io.Discard, "", 0)
	}
	if ex, ok := e.Runner.(*runner.Exec); ok {
		if ex.Logf == nil {
			ex.Logf = e.Log.Printf
		}
		if home != os.Getenv("HOME") {
			// child processes (omarchy-*, git, dconf…) must act on this home
			ex.Env = append(ex.Env, "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
				"XDG_DATA_HOME="+filepath.Join(home, ".local/share"), "XDG_STATE_HOME="+filepath.Join(home, ".local/state"))
		}
	}
	return e, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Abs turns a home-relative path ("~/.config/x" or ".config/x") into an absolute one.
func (e *Env) Abs(p string) string {
	p = strings.TrimPrefix(p, "~/")
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(e.Home, p)
}

// Rel turns an absolute path inside the home into a home-relative one.
func (e *Env) Rel(p string) string {
	r, err := filepath.Rel(e.Home, p)
	if err != nil || strings.HasPrefix(r, "..") {
		return p
	}
	return r
}

// Run is a shortcut for e.Runner.Run.
func (e *Env) Run(ctx context.Context, name string, args ...string) (runner.Result, error) {
	return e.Runner.Run(ctx, runner.Cmd{Name: name, Args: args})
}

// Out runs a command and returns trimmed stdout.
func (e *Env) Out(ctx context.Context, name string, args ...string) (string, error) {
	r, err := e.Run(ctx, name, args...)
	return strings.TrimSpace(string(r.Stdout)), err
}

// LogDir returns (and creates) the log directory.
func (e *Env) LogDir() string {
	d := filepath.Join(e.StateDir, "logs")
	_ = os.MkdirAll(d, 0o700)
	return d
}

// NewLogFile opens a timestamped log file.
func (e *Env) NewLogFile(kind string) (*os.File, string, error) {
	p := filepath.Join(e.LogDir(), fmt.Sprintf("%s-%s.log", kind, time.Now().Format("20060102-150405")))
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	return f, p, err
}

// IsOmarchy reports whether this machine runs Omarchy.
func (e *Env) IsOmarchy() bool {
	_, err := os.Stat(filepath.Join(e.OmarchyPath, "bin"))
	return err == nil
}

// OmarchyVersion returns the installed Omarchy package version.
func (e *Env) OmarchyVersion(ctx context.Context) string {
	if v, err := e.Out(ctx, "omarchy-version"); err == nil && v != "" {
		return v
	}
	if b, err := os.ReadFile(filepath.Join(e.OmarchyPath, "version")); err == nil {
		return strings.TrimSpace(string(b))
	}
	return "ismeretlen"
}

// InstallTime estimates when this system was installed (mtime of machine-id).
func (e *Env) InstallTime() time.Time {
	for _, p := range []string{filepath.Join(e.EtcDir, "machine-id"), filepath.Join(e.EtcDir, "hostname")} {
		if st, err := os.Stat(p); err == nil {
			return st.ModTime()
		}
	}
	return time.Time{}
}

// Hostname of the machine.
func Hostname() string {
	h, _ := os.Hostname()
	return h
}

// Exists reports whether a path exists (without following the final symlink).
func Exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// IsDir reports whether p is a directory (following symlinks).
func IsDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// ErrNotOmarchy is returned when obr runs on something that is not Omarchy.
var ErrNotOmarchy = i18n.Err("ez a gép nem Omarchy (nincs /usr/share/omarchy)")
