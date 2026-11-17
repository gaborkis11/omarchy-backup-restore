package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gaborkis11/omarchy-backup-restore/internal/config"
	"github.com/gaborkis11/omarchy-backup-restore/internal/runner"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

// BaseDir is the folder obr creates on every destination.
const BaseDir = "omarchy-backup"

// Credentials for opening a destination.
type Credentials struct {
	RepoPassword string
	NASPassword  string
	KnownHosts   string // path to a known_hosts file for SFTP (optional)
}

// Location is a resolved destination.
type Location struct {
	Dest config.Destination
	// Local: absolute path of the omarchy-backup folder.
	// Remote: rclone path "obrnas:<...>/omarchy-backup".
	Base   string
	Repo   *Repo
	rclone []string // rclone env for remote kit uploads
}

// ErrNotMounted means a removable drive is not connected.
var ErrNotMounted = i18n.Err("a meghajtó nincs csatlakoztatva")

// Resolve turns a destination into a Location with an opened (not yet
// verified) restic repo.
func Resolve(ctx context.Context, r runner.Runner, d config.Destination, cr Credentials, resticPath, rclonePath string) (*Location, error) {
	if resticPath == "" {
		return nil, ErrNoRestic
	}
	loc := &Location{Dest: d}
	env := []string{"RESTIC_PASSWORD=" + cr.RepoPassword, "RESTIC_PROGRESS_FPS=4", "RESTIC_CACHE_DIR=" + cacheDir()}
	switch d.Type {
	case config.Local, "":
		p := d.Path
		if d.UUID != "" {
			if mp, err := MountPointByUUID(ctx, r, d.UUID); err == nil && mp != "" {
				p = mp
			} else if p == "" || !isDir(p) {
				return nil, fmt.Errorf("%s: %w", d.Name, ErrNotMounted)
			}
		}
		if p == "" {
			return nil, errors.New(i18n.T("nincs megadva útvonal"))
		}
		loc.Base = filepath.Join(p, BaseDir)
		loc.Repo = &Repo{URL: filepath.Join(loc.Base, "repo"), Env: env, Restic: resticPath, Runner: r}
	case config.SFTP, config.SMB:
		if rclonePath == "" {
			return nil, errors.New(i18n.T("a hálózati mentéshez rclone kell (make tools)"))
		}
		renv, err := rcloneEnv(ctx, r, rclonePath, d, cr)
		if err != nil {
			return nil, err
		}
		remotePath := strings.Trim(path.Join(d.Share, d.Folder, BaseDir), "/")
		if d.Type == config.SFTP {
			remotePath = "/" + remotePath
		}
		loc.Base = "obrnas:" + remotePath
		loc.rclone = renv
		loc.Repo = &Repo{
			URL: "rclone:" + loc.Base + "/repo", Env: append(env, renv...), Restic: resticPath,
			Rclone: rclonePath, Runner: r, Options: []string{"rclone.program=" + rclonePath},
		}
	default:
		return nil, fmt.Errorf(i18n.T("ismeretlen céltípus: %s"), d.Type)
	}
	return loc, nil
}

func cacheDir() string {
	if c, err := os.UserCacheDir(); err == nil {
		return filepath.Join(c, "obr", "restic")
	}
	return filepath.Join(os.TempDir(), "obr-restic-cache")
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func rcloneEnv(ctx context.Context, r runner.Runner, rclone string, d config.Destination, cr Credentials) ([]string, error) {
	obscured := ""
	if cr.NASPassword != "" {
		res, err := r.Run(ctx, runner.Cmd{Name: rclone, Args: []string{"obscure", "-"}, Stdin: strings.NewReader(cr.NASPassword)})
		if err != nil {
			return nil, fmt.Errorf("rclone obscure: %w", err)
		}
		obscured = strings.TrimSpace(string(res.Stdout))
	}
	env := []string{
		"RCLONE_CONFIG=/dev/null",
		"RCLONE_CONFIG_OBRNAS_TYPE=" + d.Type,
		"RCLONE_CONFIG_OBRNAS_HOST=" + d.Host,
		"RCLONE_CONFIG_OBRNAS_USER=" + d.User,
	}
	if obscured != "" {
		env = append(env, "RCLONE_CONFIG_OBRNAS_PASS="+obscured)
	}
	if d.Port != 0 {
		env = append(env, "RCLONE_CONFIG_OBRNAS_PORT="+strconv.Itoa(d.Port))
	}
	switch d.Type {
	case config.SFTP:
		env = append(env,
			"RCLONE_CONFIG_OBRNAS_SHELL_TYPE=none",
			"RCLONE_CONFIG_OBRNAS_MD5SUM_COMMAND=none",
			"RCLONE_CONFIG_OBRNAS_SHA1SUM_COMMAND=none",
			"RCLONE_CONFIG_OBRNAS_DISABLE_HASHCHECK=true",
			"RCLONE_CONFIG_OBRNAS_IDLE_TIMEOUT=0")
		if cr.KnownHosts != "" {
			env = append(env, "RCLONE_CONFIG_OBRNAS_KNOWN_HOSTS_FILE="+cr.KnownHosts)
		}
	case config.SMB:
		env = append(env, "RCLONE_CONFIG_OBRNAS_IDLE_TIMEOUT=0")
	}
	return env, nil
}

// nasPath turns a share-relative path ("share/sub") into an rclone path.
func nasPath(d config.Destination, rel string) string {
	p := strings.Trim(rel, "/")
	if d.Type == config.SFTP {
		p = "/" + p
	}
	return "obrnas:" + p
}

// Browse lists the sub folders of rel on a NAS ("" = the shared folders).
// Synology system folders (#recycle, @eaDir…) are hidden.
func Browse(ctx context.Context, r runner.Runner, rclone string, d config.Destination, cr Credentials, rel string) ([]string, error) {
	if rclone == "" {
		return nil, errors.New(i18n.T("a hálózati mentéshez rclone kell (make tools)"))
	}
	env, err := rcloneEnv(ctx, r, rclone, d, cr)
	if err != nil {
		return nil, err
	}
	res, err := r.Run(ctx, runner.Cmd{Name: rclone, Args: []string{"lsf", "--dirs-only", nasPath(d, rel)}, Env: env})
	if err != nil {
		return nil, mapErr(err)
	}
	var out []string
	for _, l := range strings.Split(string(res.Stdout), "\n") {
		n := strings.TrimSuffix(strings.TrimSpace(l), "/")
		if n == "" || strings.HasPrefix(n, "#") || strings.HasPrefix(n, "@") {
			continue
		}
		out = append(out, n)
	}
	return out, nil
}

// MakeDir creates a folder on a NAS.
func MakeDir(ctx context.Context, r runner.Runner, rclone string, d config.Destination, cr Credentials, rel string) error {
	env, err := rcloneEnv(ctx, r, rclone, d, cr)
	if err != nil {
		return err
	}
	_, err = r.Run(ctx, runner.Cmd{Name: rclone, Args: []string{"mkdir", nasPath(d, rel)}, Env: env})
	return mapErr(err)
}

// RemoteListing lists files in a remote folder (for the kit and existence checks).
func (l *Location) RemoteListing(ctx context.Context, sub string) ([]string, error) {
	if !l.Dest.Remote() {
		ents, err := os.ReadDir(filepath.Join(l.Base, sub))
		if err != nil {
			return nil, err
		}
		var out []string
		for _, e := range ents {
			out = append(out, e.Name())
		}
		return out, nil
	}
	res, err := l.Repo.Runner.Run(ctx, runner.Cmd{Name: l.Repo.Rclone, Args: []string{"lsf", l.Base + "/" + sub}, Env: l.rclone})
	if err != nil {
		return nil, mapErr(err)
	}
	var out []string
	for _, s := range strings.Split(string(res.Stdout), "\n") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, strings.TrimSuffix(s, "/"))
		}
	}
	return out, nil
}

// PutFile copies a local file to <base>/<rel> on the destination.
func (l *Location) PutFile(ctx context.Context, local, rel string) error {
	if !l.Dest.Remote() {
		dst := filepath.Join(l.Base, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return copyFile(local, dst)
	}
	_, err := l.Repo.Runner.Run(ctx, runner.Cmd{Name: l.Repo.Rclone, Args: []string{"copyto", local, l.Base + "/" + rel}, Env: l.rclone})
	return err
}

// GetFile copies <base>/<rel> from the destination to a local file.
func (l *Location) GetFile(ctx context.Context, rel, local string) error {
	if !l.Dest.Remote() {
		return copyFile(filepath.Join(l.Base, rel), local)
	}
	_, err := l.Repo.Runner.Run(ctx, runner.Cmd{Name: l.Repo.Rclone, Args: []string{"copyto", l.Base + "/" + rel, local}, Env: l.rclone})
	return err
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".obr-tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// Sync flushes filesystem buffers for local destinations.
func (l *Location) Sync(ctx context.Context) {
	if l.Dest.Remote() {
		return
	}
	_, _ = l.Repo.Runner.Run(ctx, runner.Cmd{Name: "sync", Args: []string{"-f", l.Base}})
}

// Drive is a mounted (or mountable) block device.
type Drive struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Label      string `json:"label"`
	UUID       string `json:"uuid"`
	FSType     string `json:"fstype"`
	Size       string `json:"size"`
	FSAvail    string `json:"fsavail"`
	Mountpoint string `json:"mountpoint"`
	RM         bool   `json:"rm"`
	Hotplug    bool   `json:"hotplug"`
	Tran       string `json:"tran"`
	HasBackup  bool   `json:"-"`
}

// ListDrives returns mounted filesystems suitable as destinations (removable or
// mounted under /run/media or /mnt), excluding the system disk.
func ListDrives(ctx context.Context, r runner.Runner) ([]Drive, error) {
	res, err := r.Run(ctx, runner.Cmd{Name: "lsblk", Args: []string{"-J", "-b", "-o", "NAME,PATH,LABEL,UUID,FSTYPE,SIZE,FSAVAIL,MOUNTPOINT,RM,HOTPLUG,TRAN"}})
	if err != nil {
		return nil, err
	}
	var doc struct {
		Blockdevices []json.RawMessage `json:"blockdevices"`
	}
	if err := json.Unmarshal(res.Stdout, &doc); err != nil {
		return nil, err
	}
	var out []Drive
	var walk func(raw json.RawMessage, parentHot bool)
	walk = func(raw json.RawMessage, parentHot bool) {
		var d struct {
			Drive
			Size     json.Number       `json:"size"`
			FSAvail  json.Number       `json:"fsavail"`
			Children []json.RawMessage `json:"children"`
		}
		if json.Unmarshal(raw, &d) != nil {
			return
		}
		hot := parentHot || d.RM || d.Hotplug || d.Tran == "usb"
		mp := d.Mountpoint
		if mp != "" && (strings.HasPrefix(mp, "/run/media/") || strings.HasPrefix(mp, "/mnt") || strings.HasPrefix(mp, "/media")) {
			if d.FSType != "iso9660" && d.FSType != "squashfs" {
				dr := d.Drive
				dr.Size = humanBytes(d.Size.String())
				dr.FSAvail = humanBytes(d.FSAvail.String())
				dr.Hotplug = hot
				dr.HasBackup = isDir(filepath.Join(mp, BaseDir, "repo"))
				out = append(out, dr)
			}
		}
		for _, c := range d.Children {
			walk(c, hot)
		}
	}
	for _, b := range doc.Blockdevices {
		walk(b, false)
	}
	return out, nil
}

// MountPointByUUID finds where a filesystem is mounted.
func MountPointByUUID(ctx context.Context, r runner.Runner, uuid string) (string, error) {
	res, err := r.Run(ctx, runner.Cmd{Name: "findmnt", Args: []string{"-n", "-o", "TARGET", "-S", "UUID=" + uuid}})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.Split(string(res.Stdout), "\n")[0]), nil
}

// FreeBytes returns free space at a local path (0 if unknown).
func FreeBytes(ctx context.Context, r runner.Runner, p string) int64 {
	res, err := r.Run(ctx, runner.Cmd{Name: "df", Args: []string{"-B1", "--output=avail", p}})
	if err != nil {
		return 0
	}
	lines := strings.Fields(string(res.Stdout))
	if len(lines) < 2 {
		return 0
	}
	n, _ := strconv.ParseInt(lines[len(lines)-1], 10, 64)
	return n
}

func humanBytes(s string) string {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n == 0 {
		return ""
	}
	const k = 1024.0
	f := float64(n)
	switch {
	case f >= k*k*k*k:
		return fmt.Sprintf("%.1f TB", f/(k*k*k*k))
	case f >= k*k*k:
		return fmt.Sprintf("%.0f GB", f/(k*k*k))
	case f >= k*k:
		return fmt.Sprintf("%.0f MB", f/(k*k))
	}
	return fmt.Sprintf("%d B", n)
}
