// Package engine wraps restic (and rclone for network destinations).
package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gaborkis11/omarchy-backup-restore/internal/runner"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

// restic exit codes.
const (
	ExitIncomplete = 3
	ExitNoRepo     = 10
	ExitLocked     = 11
	ExitBadPass    = 12
)

// Errors with friendly Hungarian messages.
var (
	ErrNoRepo   = i18n.Err("ezen a helyen még nincs mentés")
	ErrBadPass  = i18n.Err("hibás jelszó (vagy nem ehhez a mentéshez tartozik)")
	ErrLocked   = i18n.Err("a mentést egy másik folyamat zárolja (lehet, hogy egy korábbi futás megszakadt)")
	ErrNoRestic = i18n.Err("a restic nem található; futtasd: make tools (vagy a kit/restore.sh-t)")
)

// Repo is an opened restic repository.
type Repo struct {
	URL     string   // restic -r value
	Env     []string // child-only environment (password, rclone config)
	Restic  string   // path to restic
	Rclone  string   // path to rclone (remote only)
	Runner  runner.Runner
	Options []string // extra -o options
	Host    string   // --host value for snapshots (default "obr")
}

func (r *Repo) cmd(args ...string) runner.Cmd {
	full := []string{"-r", r.URL}
	for _, o := range r.Options {
		full = append(full, "-o", o)
	}
	full = append(full, args...)
	return runner.Cmd{Name: r.Restic, Args: full, Env: r.Env}
}

func (r *Repo) host() string {
	if r.Host == "" {
		return "obr"
	}
	return r.Host
}

// Network errors with friendly messages.
var (
	ErrNASAuth        = i18n.Err("a NAS nem engedett be: hibás felhasználónév vagy jelszó (vagy nincs jogod a megosztáshoz)")
	ErrNASUnreachable = i18n.Err("a NAS nem érhető el (be van kapcsolva? jó a cím? az SFTP/SMB engedélyezve van a DSM-ben?)")
	ErrNASShare       = i18n.Err("a megosztott mappa nem található a NAS-on (ellenőrizd a nevét)")
)

func mapErr(err error) error {
	switch runner.Code(err) {
	case ExitNoRepo:
		return ErrNoRepo
	case ExitBadPass:
		return ErrBadPass
	case ExitLocked:
		return ErrLocked
	}
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "unable to authenticate") || strings.Contains(msg, "logon is invalid") ||
		strings.Contains(msg, "logon failure") || strings.Contains(msg, "permission denied") && strings.Contains(msg, "ssh"):
		return ErrNASAuth
	case strings.Contains(msg, "connection refused") || strings.Contains(msg, "no route to host") ||
		strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "network is unreachable") || strings.Contains(msg, "no such host"):
		return ErrNASUnreachable
	case strings.Contains(msg, "bad network name") || strings.Contains(msg, "network name cannot be found"):
		return ErrNASShare
	}
	return err
}

// Exists reports whether a repository exists (and the password opens it).
func (r *Repo) Exists(ctx context.Context) (bool, error) {
	_, err := r.Runner.Run(ctx, r.cmd("cat", "config", "--no-lock"))
	if err == nil {
		return true, nil
	}
	if runner.Code(err) == ExitNoRepo {
		return false, nil
	}
	return false, mapErr(err)
}

// Init creates a new repository.
func (r *Repo) Init(ctx context.Context) error {
	_, err := r.Runner.Run(ctx, r.cmd("init", "--repository-version", "2"))
	return mapErr(err)
}

// Snapshot as returned by `restic snapshots --json`.
type Snapshot struct {
	ID       string    `json:"id"`
	ShortID  string    `json:"short_id"`
	Time     time.Time `json:"time"`
	Paths    []string  `json:"paths"`
	Tags     []string  `json:"tags"`
	Hostname string    `json:"hostname"`
	Summary  *struct {
		TotalFilesProcessed int64 `json:"total_files_processed"`
		TotalBytesProcessed int64 `json:"total_bytes_processed"`
		DataAdded           int64 `json:"data_added"`
	} `json:"summary,omitempty"`
}

// Tag returns the value of a "key-value" style tag (e.g. run-<id>).
func (s Snapshot) Tag(prefix string) string {
	for _, t := range s.Tags {
		if strings.HasPrefix(t, prefix) {
			return strings.TrimPrefix(t, prefix)
		}
	}
	return ""
}

// HasTag reports whether the snapshot has tag t.
func (s Snapshot) HasTag(t string) bool {
	for _, x := range s.Tags {
		if x == t {
			return true
		}
	}
	return false
}

// Snapshots lists snapshots with all of the given tags, newest first.
func (r *Repo) Snapshots(ctx context.Context, tags ...string) ([]Snapshot, error) {
	args := []string{"snapshots", "--json", "--no-lock"}
	if len(tags) > 0 {
		args = append(args, "--tag", strings.Join(tags, ","))
	}
	res, err := r.Runner.Run(ctx, r.cmd(args...))
	if err != nil {
		return nil, mapErr(err)
	}
	var out []Snapshot
	if err := json.Unmarshal(bytes.TrimSpace(res.Stdout), &out); err != nil && len(bytes.TrimSpace(res.Stdout)) > 0 {
		return nil, fmt.Errorf("restic snapshots: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out, nil
}

// Progress is a normalised progress event (backup or restore).
type Progress struct {
	Percent      float64
	FilesDone    int64
	TotalFiles   int64
	BytesDone    int64
	TotalBytes   int64
	SecondsLeft  int64
	CurrentFiles []string
	Error        string // non-fatal error line
}

// BackupOpts configures one backup.
type BackupOpts struct {
	Dir           string   // working directory; paths are relative to it
	Paths         []string // relative paths
	Excludes      []string
	Tags          []string
	Parent        string
	TmpDir        string // where to write the files-from/exclude lists
	ExcludeCaches bool
}

// BackupSummary from restic's JSON summary.
type BackupSummary struct {
	SnapshotID          string   `json:"snapshot_id"`
	FilesNew            int64    `json:"files_new"`
	FilesChanged        int64    `json:"files_changed"`
	FilesUnmodified     int64    `json:"files_unmodified"`
	DataAdded           int64    `json:"data_added"`
	TotalFilesProcessed int64    `json:"total_files_processed"`
	TotalBytesProcessed int64    `json:"total_bytes_processed"`
	TotalDuration       float64  `json:"total_duration"`
	Incomplete          bool     `json:"-"`
	Errors              []string `json:"-"`
}

// Backup runs `restic backup` and streams progress.
func (r *Repo) Backup(ctx context.Context, o BackupOpts, progress func(Progress)) (*BackupSummary, error) {
	if len(o.Paths) == 0 {
		return nil, errors.New(i18n.T("nincs mit menteni"))
	}
	tmp := o.TmpDir
	if tmp == "" {
		tmp = os.TempDir()
	}
	listF, err := writeList(tmp, "files-", o.Paths)
	if err != nil {
		return nil, err
	}
	defer os.Remove(listF)
	exF, err := writeList(tmp, "excl-", o.Excludes)
	if err != nil {
		return nil, err
	}
	defer os.Remove(exF)
	args := []string{"backup", "--json", "--host", r.host(), "--files-from-verbatim", listF, "--exclude-file", exF}
	for _, t := range o.Tags {
		args = append(args, "--tag", t)
	}
	if o.Parent != "" {
		args = append(args, "--parent", o.Parent)
	}
	if o.ExcludeCaches {
		args = append(args, "--exclude-caches")
	}
	c := r.cmd(args...)
	c.Dir = o.Dir
	sum := &BackupSummary{}
	var mu sync.Mutex
	lw := &lineWriter{fn: func(line []byte) {
		var m map[string]any
		if json.Unmarshal(line, &m) != nil {
			return
		}
		switch m["message_type"] {
		case "status":
			if progress != nil {
				progress(statusToProgress(m))
			}
		case "error":
			msg := errMessage(m)
			mu.Lock()
			sum.Errors = append(sum.Errors, msg)
			mu.Unlock()
			if progress != nil {
				progress(Progress{Error: msg})
			}
		case "summary":
			_ = json.Unmarshal(line, sum)
		}
	}}
	c.Stdout = lw
	_, err = r.Runner.Run(ctx, c)
	lw.Flush()
	if runner.Code(err) == ExitLocked {
		if r.Unlock(ctx) == nil {
			_, err = r.Runner.Run(ctx, c)
			lw.Flush()
		}
	}
	if err != nil {
		if runner.Code(err) == ExitIncomplete && sum.SnapshotID != "" {
			sum.Incomplete = true
			return sum, nil
		}
		return sum, mapErr(err)
	}
	return sum, nil
}

func errMessage(m map[string]any) string {
	item, _ := m["item"].(string)
	msg := ""
	if e, ok := m["error"].(map[string]any); ok {
		msg, _ = e["message"].(string)
	}
	if item != "" {
		return item + ": " + msg
	}
	return msg
}

func num(m map[string]any, k string) int64 {
	switch v := m[k].(type) {
	case float64:
		return int64(v)
	}
	return 0
}

func statusToProgress(m map[string]any) Progress {
	p := Progress{
		TotalFiles:  num(m, "total_files"),
		TotalBytes:  num(m, "total_bytes"),
		SecondsLeft: num(m, "seconds_remaining"),
	}
	if v, ok := m["percent_done"].(float64); ok {
		p.Percent = v
	}
	p.FilesDone = num(m, "files_done")
	if p.FilesDone == 0 {
		p.FilesDone = num(m, "files_restored")
	}
	p.BytesDone = num(m, "bytes_done")
	if p.BytesDone == 0 {
		p.BytesDone = num(m, "bytes_restored")
	}
	if cf, ok := m["current_files"].([]any); ok {
		for _, f := range cf {
			if s, ok := f.(string); ok {
				p.CurrentFiles = append(p.CurrentFiles, s)
			}
		}
	}
	return p
}

// Dump writes one file of a snapshot to w.
func (r *Repo) Dump(ctx context.Context, snapshot, path string, w io.Writer) error {
	c := r.cmd("dump", "--no-lock", snapshot, path)
	var buf bytes.Buffer
	c.Stdout = &buf
	_, err := r.Runner.Run(ctx, c)
	if err != nil {
		return mapErr(err)
	}
	_, err = io.Copy(w, &buf)
	return err
}

// RestoreOpts configures a restore.
type RestoreOpts struct {
	Snapshot  string // id
	SubPath   string // path inside the snapshot ("/" for all)
	Target    string
	Includes  []string // paths relative to SubPath (with leading "/")
	Overwrite string   // always | if-changed | if-newer | never
	DryRun    bool
	TmpDir    string
	Excludes  []string // snapshot paths (relative to SubPath, leading "/") to skip
}

// RestoreWithExcludes is Restore with extra exclude patterns.
func (r *Repo) RestoreWithExcludes(ctx context.Context, o RestoreOpts, excludes []string, progress func(Progress)) (*RestoreSummary, error) {
	o.Excludes = append(o.Excludes, excludes...)
	return r.Restore(ctx, o, progress)
}

// RestoreSummary from restic.
type RestoreSummary struct {
	TotalFiles    int64 `json:"total_files"`
	FilesRestored int64 `json:"files_restored"`
	FilesSkipped  int64 `json:"files_skipped"`
	BytesRestored int64 `json:"bytes_restored"`
	// Changed lists items restic reported as updated/restored (dry-run with verbose).
	Updated  []string `json:"-"`
	Restored []string `json:"-"`
	Errors   []string `json:"-"`
}

// Restore runs `restic restore`.
func (r *Repo) Restore(ctx context.Context, o RestoreOpts, progress func(Progress)) (*RestoreSummary, error) {
	sub := o.SubPath
	if sub == "" {
		sub = "/"
	}
	args := []string{"restore", "--json", o.Snapshot + ":" + sub, "--target", o.Target}
	if o.Overwrite != "" {
		args = append(args, "--overwrite", o.Overwrite)
	}
	if o.DryRun {
		args = append(args, "--dry-run", "-vv")
	}
	if len(o.Includes) > 0 {
		tmp := o.TmpDir
		if tmp == "" {
			tmp = os.TempDir()
		}
		incF, err := writeList(tmp, "incl-", o.Includes)
		if err != nil {
			return nil, err
		}
		defer os.Remove(incF)
		args = append(args, "--include-file", incF)
	}
	if len(o.Excludes) > 0 {
		tmp := o.TmpDir
		if tmp == "" {
			tmp = os.TempDir()
		}
		exF, err := writeList(tmp, "rexcl-", o.Excludes)
		if err != nil {
			return nil, err
		}
		defer os.Remove(exF)
		args = append(args, "--exclude-file", exF)
	}
	sum := &RestoreSummary{}
	var mu sync.Mutex
	lw := &lineWriter{fn: func(line []byte) {
		var m map[string]any
		if json.Unmarshal(line, &m) != nil {
			return
		}
		switch m["message_type"] {
		case "status":
			if progress != nil {
				progress(statusToProgress(m))
			}
		case "verbose_status":
			act, _ := m["action"].(string)
			item, _ := m["item"].(string)
			mu.Lock()
			switch act {
			case "updated":
				sum.Updated = append(sum.Updated, item)
			case "restored":
				sum.Restored = append(sum.Restored, item)
			}
			mu.Unlock()
		case "error":
			msg := errMessage(m)
			mu.Lock()
			sum.Errors = append(sum.Errors, msg)
			mu.Unlock()
			if progress != nil {
				progress(Progress{Error: msg})
			}
		case "summary":
			_ = json.Unmarshal(line, sum)
		}
	}}
	c := r.cmd(args...)
	c.Stdout = lw
	_, err := r.Runner.Run(ctx, c)
	lw.Flush()
	if err != nil {
		return sum, mapErr(err)
	}
	return sum, nil
}

// retryStale runs f; when the repository is locked it removes stale locks
// (`restic unlock` never removes locks of live processes) and tries once more.
func (r *Repo) retryStale(ctx context.Context, f func() error) error {
	err := f()
	if errors.Is(err, ErrLocked) {
		if uerr := r.Unlock(ctx); uerr == nil {
			err = f()
		}
	}
	return err
}

// Check verifies the repository; subset like "5%" also reads data.
func (r *Repo) Check(ctx context.Context, subset string) error {
	args := []string{"check"}
	if subset != "" {
		args = append(args, "--read-data-subset", subset)
	}
	return r.retryStale(ctx, func() error {
		_, err := r.Runner.Run(ctx, r.cmd(args...))
		return mapErr(err)
	})
}

// CheckLock reports ErrLocked when an exclusive lock cannot be taken right now
// (it tries `list locks` and returns ErrLocked if any lock files exist).
func (r *Repo) CheckLock(ctx context.Context) error {
	res, err := r.Runner.Run(ctx, r.cmd("list", "locks", "--no-lock"))
	if err != nil {
		return mapErr(err)
	}
	if len(bytes.TrimSpace(res.Stdout)) > 0 {
		return ErrLocked
	}
	return nil
}

// Unlock removes stale locks.
func (r *Repo) Unlock(ctx context.Context) error {
	_, err := r.Runner.Run(ctx, r.cmd("unlock"))
	return mapErr(err)
}

// KeyAdd adds a second password (recovery key).
func (r *Repo) KeyAdd(ctx context.Context, tmpDir, newPassword string) error {
	f, err := os.CreateTemp(tmpDir, "obr-key-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(newPassword); err != nil {
		f.Close()
		return err
	}
	f.Close()
	_, err = r.Runner.Run(ctx, r.cmd("key", "add", "--new-password-file", f.Name()))
	return mapErr(err)
}

// Forget removes snapshots by id and optionally prunes data.
func (r *Repo) Forget(ctx context.Context, ids []string, prune bool) error {
	if len(ids) == 0 {
		return nil
	}
	args := append([]string{"forget"}, ids...)
	if prune {
		args = append(args, "--prune")
	}
	return r.retryStale(ctx, func() error {
		_, err := r.Runner.Run(ctx, r.cmd(args...))
		return mapErr(err)
	})
}

// ForgetPolicy asks restic which meta snapshots a policy would remove (dry run).
func (r *Repo) ForgetPolicy(ctx context.Context, tag string, policy []string) ([]string, error) {
	args := append([]string{"forget", "--dry-run", "--json", "--tag", tag, "--group-by", "host"}, policy...)
	res, err := r.Runner.Run(ctx, r.cmd(args...))
	if err != nil {
		return nil, mapErr(err)
	}
	var groups []struct {
		Remove []Snapshot `json:"remove"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(res.Stdout), &groups); err != nil {
		return nil, err
	}
	var ids []string
	for _, g := range groups {
		for _, s := range g.Remove {
			ids = append(ids, s.ID)
		}
	}
	return ids, nil
}

// Stats returns the raw repository size in bytes.
func (r *Repo) Stats(ctx context.Context) (int64, error) {
	res, err := r.Runner.Run(ctx, r.cmd("stats", "--json", "--mode", "raw-data", "--no-lock"))
	if err != nil {
		return 0, mapErr(err)
	}
	var s struct {
		TotalSize int64 `json:"total_size"`
	}
	_ = json.Unmarshal(bytes.TrimSpace(res.Stdout), &s)
	return s.TotalSize, nil
}

func writeList(dir, prefix string, lines []string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".obr-tmp-"+prefix)
	if err != nil {
		return "", err
	}
	w := bufio.NewWriter(f)
	for _, l := range lines {
		if strings.ContainsAny(l, "\n") {
			continue
		}
		fmt.Fprintln(w, l)
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return "", err
	}
	return f.Name(), f.Close()
}

// lineWriter splits a stream into lines.
type lineWriter struct {
	mu  sync.Mutex
	buf []byte
	fn  func([]byte)
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimSpace(l.buf[:i])
		l.buf = l.buf[i+1:]
		if len(line) > 0 {
			l.fn(append([]byte(nil), line...))
		}
	}
	return len(p), nil
}

func (l *lineWriter) Flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if line := bytes.TrimSpace(l.buf); len(line) > 0 {
		l.fn(line)
	}
	l.buf = nil
}

// FindTool locates a helper binary: obr's bin dir, next to the running
// executable, then PATH.
func FindTool(binDir, name string) string {
	cands := []string{filepath.Join(binDir, name)}
	if exe, err := os.Executable(); err == nil {
		cands = append(cands, filepath.Join(filepath.Dir(exe), name))
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return c
		}
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		c := filepath.Join(dir, name)
		if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return c
		}
	}
	return ""
}
