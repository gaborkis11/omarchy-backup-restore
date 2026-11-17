package backup

import (
	"bytes"
	"context"
	_ "embed"
	"os"
	"path/filepath"
	"text/template"
	"time"

	"github.com/gaborkis11/omarchy-backup-restore/internal/engine"
	"github.com/gaborkis11/omarchy-backup-restore/internal/inventory"
	"github.com/gaborkis11/omarchy-backup-restore/internal/sys"
)

//go:embed kitfiles/restore.sh
var restoreSh []byte

//go:embed kitfiles/install.sh
var installSh []byte

//go:embed kitfiles/LEIRAS.txt
var leirasTmpl string

//go:embed kitfiles/README.txt
var readmeTmpl string

// WriteKit copies obr, restic (and rclone for network destinations), the
// restore.sh launcher and LEIRAS.txt next to the repository.
func WriteKit(ctx context.Context, env *sys.Env, loc *engine.Location) error {
	tmp, err := os.MkdirTemp(env.RuntimeDir, "obr-kit-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	files := map[string]string{} // rel -> local source
	exe, err := os.Executable()
	if err == nil {
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		}
		files["kit/obr"] = exe
	}
	if loc.Repo.Restic != "" {
		files["kit/restic"] = loc.Repo.Restic
	}
	// rclone always goes along, so a new machine can also reach the NAS
	if rc := engine.FindTool(env.BinDir, "rclone"); rc != "" {
		files["kit/rclone"] = rc
	}
	sh := filepath.Join(tmp, "restore.sh")
	if err := os.WriteFile(sh, restoreSh, 0o755); err != nil {
		return err
	}
	files["kit/restore.sh"] = sh
	ish := filepath.Join(tmp, "install.sh")
	if err := os.WriteFile(ish, installSh, 0o755); err != nil {
		return err
	}
	files["kit/install.sh"] = ish

	m := LoadLast(env, "")
	data := map[string]any{"Created": time.Now().Format("2006-01-02 15:04"), "Host": sys.Hostname(),
		"User": env.User, "OmarchyVersion": env.OmarchyVersion(ctx), "Remote": loc.Dest.Remote()}
	if m != nil {
		data["Created"] = m.Created.Format("2006-01-02 15:04")
	}
	for name, tmpl := range map[string]string{"LEIRAS.txt": leirasTmpl, "README.txt": readmeTmpl} {
		var buf bytes.Buffer
		if err := template.Must(template.New(name).Parse(tmpl)).Execute(&buf, data); err != nil {
			return err
		}
		lp := filepath.Join(tmp, name)
		if err := os.WriteFile(lp, buf.Bytes(), 0o644); err != nil {
			return err
		}
		files[name] = lp
	}

	for rel, src := range files {
		if !loc.Dest.Remote() && inventory.SameFile(src, filepath.Join(loc.Base, rel)) {
			continue // unchanged, save time on slow drives
		}
		if err := loc.PutFile(ctx, src, rel); err != nil {
			return err
		}
	}
	return nil
}
