package inventory

import (
	"testing"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

func TestMatcher(t *testing.T) {
	m := NewMatcher([]string{"Cache", "*.sock", "/home/u/.config/**/GPUCache", "/home/u/.config/google-chrome/*/Login Data"})
	cases := map[string]bool{
		"/home/u/.config/google-chrome/Default/Cache/x":      true,
		"/home/u/Documents/Cache":                            true,
		"/home/u/.config/app/x.sock":                         true,
		"/home/u/.config/a/b/GPUCache/data":                  true,
		"/home/u/.local/GPUCache":                            false,
		"/home/u/.config/google-chrome/Default/Login Data":   true,
		"/home/u/.config/google-chrome/Default/Login Data-x": false,
		"/home/u/.config/google-chrome/Default/Preferences":  false,
	}
	for p, want := range cases {
		if got := m.Match(p); got != want {
			t.Errorf("%s: %v, várt %v", p, got, want)
		}
	}
}

func TestEtcDenied(t *testing.T) {
	for _, p := range []string{"fstab", "fstab.20261114.back", "ssh/ssh_host_rsa_key", "pacman.d/hooks/x", "sudoers.d/a", "foo.pacnew", "profile.d/omarchy.sh"} {
		if !EtcDenied(p) {
			t.Errorf("nem tiltott: %s", p)
		}
	}
	for _, p := range []string{"docker/daemon.json", "hosts", "cron.d/x", "fstabx"} {
		if EtcDenied(p) {
			t.Errorf("tiltott, pedig nem kéne: %s", p)
		}
	}
}

func TestHumanSize(t *testing.T) {
	i18n.Set(i18n.HU)
	defer i18n.Set(i18n.EN)
	if s := HumanSize(1536); s != "1,5 KB" {
		t.Errorf("%s", s)
	}
	if s := HumanSize(3 << 30); s != "3,0 GB" {
		t.Errorf("%s", s)
	}
}

func TestParseDesktopUnquote(t *testing.T) {
	if u := unquote(`"https://x.example/a b"`); u != "https://x.example/a b" {
		t.Errorf("%q", u)
	}
}
