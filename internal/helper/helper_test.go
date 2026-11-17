package helper

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func mkTar(t *testing.T, files map[string]string) *bytes.Buffer {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	return &buf
}

func TestApplyAllowlist(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("rootként nem futtatjuk")
	}
	root := t.TempDir()
	t.Setenv("OBR_HELPER_ROOT", root)
	mac := "AA:BB:CC:DD:EE:FF"
	tarball := mkTar(t, map[string]string{
		"etc/NetworkManager/system-connections/home.nmconnection": "[wifi]",
		"etc/fstab":          "EVIL",
		"etc/machine-id":     "EVIL",
		"etc/sudoers.d/evil": "EVIL",
		"../escape":          "EVIL",
		"var/lib/bluetooth/" + mac + "/11:22:33:44:55:66/info": "[LinkKey]",
		"var/lib/bluetooth/00:00:00:00:00:00/x/info":           "EVIL",
		"etc/docker/daemon.json":                               "{}",
		"etc/cups/printers.conf":                               "<Printer>",
	})
	var log bytes.Buffer
	err := apply(context.Background(), []string{"--nm", "--bt", mac, "--bt-from", mac, "--etc", "docker/daemon.json"}, tarball, &log)
	if err != nil {
		t.Fatal(err)
	}
	ok := []string{"etc/NetworkManager/system-connections/home.nmconnection", "var/lib/bluetooth/" + mac + "/11:22:33:44:55:66/info", "etc/docker/daemon.json"}
	for _, p := range ok {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Errorf("hiányzik: %s", p)
		}
	}
	bad := []string{"etc/fstab", "etc/machine-id", "etc/sudoers.d/evil", "var/lib/bluetooth/00:00:00:00:00:00", "etc/cups/printers.conf"}
	for _, p := range bad {
		if _, err := os.Stat(filepath.Join(root, p)); err == nil {
			t.Errorf("nem lett volna szabad kiírni: %s", p)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape")); err == nil {
		t.Error("kiszökött a gyökérből")
	}
	st, _ := os.Stat(filepath.Join(root, ok[0]))
	if st != nil && st.Mode().Perm() != 0o600 {
		t.Errorf("NM fájl jogosultsága: %v", st.Mode().Perm())
	}
}

func TestApplyOtherAdapterSkipsBluetooth(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OBR_HELPER_ROOT", root)
	tarball := mkTar(t, map[string]string{"var/lib/bluetooth/AA:BB:CC:DD:EE:FF/dev/info": "k"})
	var log bytes.Buffer
	if err := apply(context.Background(), []string{"--bt", "11:11:11:11:11:11", "--bt-from", "AA:BB:CC:DD:EE:FF"}, tarball, &log); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "var/lib/bluetooth")); err == nil {
		t.Error("más adapterre nem szabad párosítást írni")
	}
}

func TestDeniedEtcRejected(t *testing.T) {
	for _, f := range []string{"fstab", "shadow", "sudoers.d/x", "pacman.d/mirrorlist", "default/limine", "../etc/passwd", "/etc/passwd", "sddm.conf.d/autologin.conf", "ssh/ssh_host_ed25519_key"} {
		if err := validEtc(f); err == nil {
			t.Errorf("elfogadta: %s", f)
		}
	}
	for _, f := range []string{"docker/daemon.json", "cron.d/backup", "hosts"} {
		if err := validEtc(f); err != nil {
			t.Errorf("elutasította: %s (%v)", f, err)
		}
	}
}

func TestCollect(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OBR_HELPER_ROOT", root)
	os.MkdirAll(filepath.Join(root, "etc/NetworkManager/system-connections"), 0o700)
	os.WriteFile(filepath.Join(root, "etc/NetworkManager/system-connections/a.nmconnection"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(root, "etc/fstab"), []byte("x"), 0o644)
	var out bytes.Buffer
	if err := collect([]string{"--nm"}, &out); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&out)
	var names []string
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
	}
	if len(names) != 1 || names[0] != "etc/NetworkManager/system-connections/a.nmconnection" {
		t.Errorf("gyűjtött: %v", names)
	}
	if err := collect([]string{"--etc", "fstab"}, &out); err == nil {
		t.Error("az fstab gyűjtését el kellett volna utasítani")
	}
}
