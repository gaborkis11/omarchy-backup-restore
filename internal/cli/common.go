package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/gaborkis11/omarchy-backup-restore/internal/config"
	"github.com/gaborkis11/omarchy-backup-restore/internal/engine"
	"github.com/gaborkis11/omarchy-backup-restore/internal/runner"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

// destFlags are shared by commands that open a repository.
type destFlags struct {
	dest        string
	path        string
	passFile    string
	nasPassFile string
	unattended  bool
}

func (d *destFlags) register(fs interface {
	StringVar(*string, string, string, string)
	BoolVar(*bool, string, bool, string)
}) {
	fs.StringVar(&d.dest, "dest", "", i18n.T("beállított célhely neve (obr dest list)"))
	fs.StringVar(&d.path, "path", "", i18n.T("helyi mappa (pl. /run/media/$USER/BACKUP) – omarchy-backup/ kerül bele"))
	fs.StringVar(&d.passFile, "password-file", "", i18n.T("a mentés jelszava ebből a fájlból (vagy OBR_PASSWORD)"))
	fs.StringVar(&d.nasPassFile, "nas-password-file", "", i18n.T("a NAS jelszava ebből a fájlból (vagy OBR_NAS_PASSWORD)"))
	fs.BoolVar(&d.unattended, "unattended", false, i18n.T("nincs kérdés: tárolt jelszavak, sudo csak ha nem kér jelszót"))
}

// destination resolves --dest / --path (or the only configured destination).
func (a *App) destination(d *destFlags) (config.Destination, error) {
	if d.path != "" {
		p, err := filepath.Abs(d.path)
		if err != nil {
			return config.Destination{}, err
		}
		// accept both the parent and the omarchy-backup folder itself
		if filepath.Base(p) == engine.BaseDir {
			p = filepath.Dir(p)
		}
		return config.Destination{Name: filepath.Base(p), Type: config.Local, Path: p}, nil
	}
	if d.dest != "" {
		if dd, ok := a.Config.Find(d.dest); ok {
			return dd, nil
		}
		return config.Destination{}, fmt.Errorf(i18n.T("nincs ilyen célhely: %q (obr dest list)"), d.dest)
	}
	if len(a.Config.Destinations) == 1 {
		return a.Config.Destinations[0], nil
	}
	return config.Destination{}, errors.New(i18n.T("add meg a célhelyet: --dest NÉV vagy --path MAPPA"))
}

// open resolves the destination and asks for passwords as needed.
func (a *App) open(ctx context.Context, d *destFlags, confirmNew bool) (*engine.Location, error) {
	dest, err := a.destination(d)
	if err != nil {
		return nil, err
	}
	cr := engine.Credentials{}
	cr.RepoPassword, err = secretFrom(d.passFile, "OBR_PASSWORD")
	if err != nil {
		return nil, err
	}
	if cr.RepoPassword == "" {
		if s, ok := config.ReadSecret(a.Env.ConfigDir, "repo", dest.Name); ok {
			cr.RepoPassword = s
		}
	}
	if dest.Remote() {
		cr.NASPassword, err = secretFrom(d.nasPassFile, "OBR_NAS_PASSWORD")
		if err != nil {
			return nil, err
		}
		if cr.NASPassword == "" {
			if s, ok := config.ReadSecret(a.Env.ConfigDir, "nas", dest.Name); ok {
				cr.NASPassword = s
			}
		}
		if cr.NASPassword == "" && !d.unattended {
			cr.NASPassword, err = askPassword(fmt.Sprintf(i18n.T("A NAS jelszava (%s@%s): "), dest.User, dest.Host), false)
			if err != nil {
				return nil, err
			}
		}
		if kh := filepath.Join(a.Env.ConfigDir, "known_hosts"); fileExists(kh) && dest.Type == config.SFTP {
			cr.KnownHosts = kh
		}
	}
	restic := engine.FindTool(a.Env.BinDir, "restic")
	rclone := engine.FindTool(a.Env.BinDir, "rclone")
	loc, err := engine.Resolve(ctx, a.Env.Runner, dest, cr, restic, rclone)
	if err != nil {
		return nil, err
	}
	if cr.RepoPassword == "" {
		if d.unattended {
			return nil, errors.New(i18n.T("nincs tárolt jelszó ehhez a célhelyhez (ütemezett mentéshez: obr schedule enable)"))
		}
		// find out whether this is a new repository first
		probe := *loc.Repo
		probe.Env = append([]string{}, loc.Repo.Env...)
		probe.Env = append(probe.Env, "RESTIC_PASSWORD=__obr_probe__")
		exists, perr := probe.Exists(ctx)
		if perr != nil && !errors.Is(perr, engine.ErrBadPass) {
			return nil, perr
		}
		if errors.Is(perr, engine.ErrBadPass) {
			exists = true
		}
		pw, err := askPassword(i18n.T("A mentés jelszava: "), !exists && confirmNew)
		if err != nil {
			return nil, err
		}
		setEnv(&loc.Repo.Env, "RESTIC_PASSWORD", pw)
	}
	return loc, nil
}

func setEnv(env *[]string, k, v string) {
	out := (*env)[:0]
	for _, e := range *env {
		if !strings.HasPrefix(e, k+"=") {
			out = append(out, e)
		}
	}
	*env = append(out, k+"="+v)
}

func secretFrom(file, envVar string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	return os.Getenv(envVar), nil
}

func askPassword(prompt string, confirm bool) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New(i18n.T("jelszó szükséges (--password-file vagy OBR_PASSWORD)"))
	}
	for {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		pw := string(b)
		if pw == "" {
			fmt.Fprintln(os.Stderr, i18n.T("A jelszó nem lehet üres."))
			continue
		}
		if !confirm {
			return pw, nil
		}
		if len([]rune(pw)) < 8 {
			fmt.Fprintln(os.Stderr, i18n.T("Legalább 8 karakter legyen."))
			continue
		}
		fmt.Fprint(os.Stderr, i18n.T("Még egyszer: "))
		b2, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if string(b2) != pw {
			fmt.Fprintln(os.Stderr, i18n.T("A két jelszó nem egyezik, próbáld újra."))
			continue
		}
		return pw, nil
	}
}

func confirm(prompt string, def bool) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return def
	}
	hint := " [i/N] "
	if def {
		hint = " [I/n] "
	}
	fmt.Fprint(os.Stderr, prompt+hint)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		return def
	}
	return line == "i" || line == i18n.T("igen") || line == "y" || line == "yes"
}

// ensureSudo makes sure sudo credentials are cached; interactive asks on the
// terminal. Returns a stop function for the keep-alive.
func ensureSudo(ctx context.Context, interactive bool) (func(), error) {
	if os.Geteuid() == 0 {
		return func() {}, nil
	}
	if exec.Command("sudo", "-n", "true").Run() != nil {
		if !interactive {
			return func() {}, errors.New(i18n.T("a sudo jelszót kér"))
		}
		fmt.Fprintln(os.Stderr, i18n.T("Rendszergazdai jog kell (a jelszavadat a sudo kéri, nem az obr):"))
		c := exec.Command("sudo", "-v")
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			return func() {}, err
		}
	}
	return SudoKeepAlive(ctx), nil
}

// SudoKeepAlive refreshes the sudo timestamp every minute.
func SudoKeepAlive(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(50 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = exec.Command("sudo", "-n", "-v").Run()
			}
		}
	}()
	return cancel
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// isRunning reports whether a process with that exact name runs.
func isRunning(r runner.Runner) func(string) bool {
	return func(name string) bool {
		_, err := r.Run(context.Background(), runner.Cmd{Name: "pgrep", Args: []string{"-x", name}})
		return err == nil
	}
}

func retentionFlags(r config.Retention) []string {
	var out []string
	if r.KeepLast > 0 {
		out = append(out, "--keep-last", fmt.Sprint(r.KeepLast))
	}
	if r.KeepDaily > 0 {
		out = append(out, "--keep-daily", fmt.Sprint(r.KeepDaily))
	}
	if r.KeepWeekly > 0 {
		out = append(out, "--keep-weekly", fmt.Sprint(r.KeepWeekly))
	}
	if r.KeepMonthly > 0 {
		out = append(out, "--keep-monthly", fmt.Sprint(r.KeepMonthly))
	}
	return out
}

func notify(title, body string) {
	if p, err := exec.LookPath("omarchy-notification-send"); err == nil {
		_ = exec.Command(p, title, body).Run()
		return
	}
	if p, err := exec.LookPath("notify-send"); err == nil {
		_ = exec.Command(p, title, body).Run()
	}
}
