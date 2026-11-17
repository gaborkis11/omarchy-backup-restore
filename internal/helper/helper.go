// Package helper implements `obr helper <verb>`: the only code that runs as
// root. It is invoked through `sudo -n <absolute path>/obr helper ...` and
// enforces a strict allowlist; it never trusts paths from its input.
package helper

import (
	"archive/tar"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gaborkis11/omarchy-backup-restore/internal/inventory"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

var (
	macRe    = regexp.MustCompile(`^([0-9A-F]{2}:){5}[0-9A-F]{2}$`)
	snapshot = "/home/.obr-snapshot"
)

// Roots used by the helper (overridable only in tests via OBR_HELPER_ROOT, and
// only when not running as root).
func root() string {
	if os.Geteuid() != 0 {
		if r := os.Getenv("OBR_HELPER_ROOT"); r != "" {
			return r
		}
	}
	return "/"
}

func p(rel string) string { return filepath.Join(root(), rel) }

// Main dispatches helper verbs.
func Main(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New(i18n.T("helper: hiányzó ige"))
	}
	if os.Geteuid() != 0 && os.Getenv("OBR_HELPER_ROOT") == "" {
		return errors.New("helper: rendszergazdai jog kell (sudo)")
	}
	switch args[0] {
	case "collect":
		return collect(args[1:], os.Stdout)
	case "apply":
		return apply(ctx, args[1:], os.Stdin, os.Stdout)
	case "snapshot-create":
		return snapshotCreate(ctx)
	case "snapshot-delete":
		return snapshotDelete(ctx)
	}
	return fmt.Errorf("helper: ismeretlen ige: %s", args[0])
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

type sel struct {
	nm, printers bool
	bt           string
	etc          multi
}

func parse(name string, args []string) (*sel, error) {
	s := &sel{}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.BoolVar(&s.nm, "nm", false, "NetworkManager kapcsolatok")
	fs.BoolVar(&s.printers, "printers", false, i18n.T("CUPS nyomtatók"))
	fs.StringVar(&s.bt, "bt", "", "Bluetooth adapter MAC")
	fs.Var(&s.etc, "etc", i18n.T("/etc-hez relatív fájl (ismételhető)"))
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if s.bt != "" && !macRe.MatchString(s.bt) {
		return nil, fmt.Errorf(i18n.T("érvénytelen adapter: %q"), s.bt)
	}
	for _, e := range s.etc {
		if err := validEtc(e); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func validEtc(rel string) error {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") || clean != rel {
		return fmt.Errorf(i18n.T("érvénytelen /etc út: %q"), rel)
	}
	if inventory.EtcDenied(clean) {
		return fmt.Errorf(i18n.T("tiltott /etc fájl: %q"), rel)
	}
	return nil
}

// allowed reports whether an archive path (relative to /) may be written.
func (s *sel) allowed(rel, targetBT string) bool {
	switch {
	case s.nm && strings.HasPrefix(rel, "etc/NetworkManager/system-connections/") && strings.Count(rel, "/") == 3:
		return true
	case s.printers && (rel == "etc/cups/printers.conf" || strings.HasPrefix(rel, "etc/cups/ppd/") && strings.Count(rel, "/") == 3):
		return true
	case s.bt != "" && strings.HasPrefix(rel, "var/lib/bluetooth/"+targetBT+"/"):
		return true
	}
	for _, e := range s.etc {
		if rel == "etc/"+e {
			return true
		}
	}
	return false
}

// collect writes the selected system files as a tar stream.
func collect(args []string, w io.Writer) error {
	s, err := parse("collect", args)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(w)
	add := func(rel string) error {
		full := p(rel)
		st, err := os.Lstat(full)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if st.IsDir() {
			return filepath.WalkDir(full, func(fp string, d fs.DirEntry, err error) error {
				if err != nil || fp == full {
					return nil
				}
				r, _ := filepath.Rel(root(), fp)
				return addFile(tw, fp, r)
			})
		}
		return addFile(tw, full, rel)
	}
	if s.nm {
		ents, _ := os.ReadDir(p("etc/NetworkManager/system-connections"))
		for _, e := range ents {
			if e.Type().IsRegular() {
				if err := add("etc/NetworkManager/system-connections/" + e.Name()); err != nil {
					return err
				}
			}
		}
	}
	if s.printers {
		if err := add("etc/cups/printers.conf"); err != nil {
			return err
		}
		if err := add("etc/cups/ppd"); err != nil {
			return err
		}
	}
	if s.bt != "" {
		if err := add("var/lib/bluetooth/" + s.bt); err != nil {
			return err
		}
	}
	for _, e := range s.etc {
		if err := add("etc/" + e); err != nil {
			return err
		}
	}
	return tw.Close()
}

func addFile(tw *tar.Writer, full, rel string) error {
	st, err := os.Lstat(full)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() && !st.IsDir() {
		return nil
	}
	hdr, err := tar.FileInfoHeader(st, "")
	if err != nil {
		return err
	}
	hdr.Name = rel
	hdr.Uname, hdr.Gname = "", ""
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if st.Mode().IsRegular() {
		f, err := os.Open(full)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	}
	return nil
}

// apply reads a tar from r and writes allowed entries as root.
// Bluetooth entries recorded for --bt-from are written under --bt (the
// target adapter) only when both are equal (same hardware).
func apply(ctx context.Context, args []string, r io.Reader, log io.Writer) error {
	var btFrom string
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--bt-from" && i+1 < len(args) {
			btFrom = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	s, err := parse("apply", rest)
	if err != nil {
		return err
	}
	if s.bt != "" && btFrom != s.bt {
		fmt.Fprintln(log, i18n.T("bluetooth: más adapter, a párosítások kimaradnak"))
		s.bt = ""
	}
	tr := tar.NewReader(r)
	changed := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		rel := filepath.Clean(hdr.Name)
		if filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") || !s.allowed(rel, s.bt) {
			fmt.Fprintln(log, "kihagyva:", hdr.Name)
			continue
		}
		dst := p(rel)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, os.FileMode(hdr.Mode)&0o755|0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(hdr.Mode) & 0o644
			if strings.HasPrefix(rel, "etc/NetworkManager/system-connections/") || strings.HasPrefix(rel, "var/lib/bluetooth/") || rel == "etc/cups/printers.conf" {
				mode = 0o600
			}
			if err := writeAtomic(dst, tr, mode); err != nil {
				return err
			}
			fmt.Fprintln(log, i18n.T("visszaállítva:"), "/"+rel)
			changed[strings.SplitN(rel, "/", 3)[1]] = true
		}
	}
	if root() == "/" {
		if changed["NetworkManager"] {
			_ = exec.CommandContext(ctx, "nmcli", "connection", "reload").Run()
		}
		if s.bt != "" {
			_ = exec.CommandContext(ctx, "systemctl", "restart", "bluetooth").Run()
		}
		if changed["cups"] {
			_ = exec.CommandContext(ctx, "systemctl", "restart", "cups").Run()
		}
	}
	return nil
}

func writeAtomic(dst string, r io.Reader, mode os.FileMode) error {
	tmp := dst + ".obr-tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if root() == "/" {
		_ = os.Chown(tmp, 0, 0)
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func snapshotCreate(ctx context.Context) error {
	if err := exec.CommandContext(ctx, "btrfs", "subvolume", "show", "/home").Run(); err != nil {
		return errors.New(i18n.T("a /home nem btrfs alkötet"))
	}
	if _, err := os.Stat(snapshot); err == nil {
		_ = exec.CommandContext(ctx, "btrfs", "subvolume", "delete", snapshot).Run()
	}
	out, err := exec.CommandContext(ctx, "btrfs", "subvolume", "snapshot", "-r", "/home", snapshot).CombinedOutput()
	if err != nil {
		return fmt.Errorf("btrfs snapshot: %v: %s", err, out)
	}
	fmt.Println(snapshot)
	return nil
}

func snapshotDelete(ctx context.Context) error {
	if _, err := os.Stat(snapshot); err != nil {
		return nil
	}
	out, err := exec.CommandContext(ctx, "btrfs", "subvolume", "delete", snapshot).CombinedOutput()
	if err != nil {
		return fmt.Errorf("btrfs delete: %v: %s", err, out)
	}
	return nil
}
