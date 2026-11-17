package merge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gaborkis11/omarchy-backup-restore/internal/runner"
)

func TestThreeWay(t *testing.T) {
	r := &runner.Exec{}
	dir := t.TempDir()
	base := []byte("a\nb\nc\n")
	ours := []byte("a\nB-mine\nc\n")
	clean, err := ThreeWay(context.Background(), r, dir, ours, base, []byte("a\nb\nc\nd-new\n"))
	if err != nil || clean.Conflicts || string(clean.Merged) != "a\nB-mine\nc\nd-new\n" {
		t.Fatalf("tiszta összefésülés: %v %+v %q", err, clean.Conflicts, clean.Merged)
	}
	conf, err := ThreeWay(context.Background(), r, dir, ours, base, []byte("a\nB-theirs\nc\n"))
	if err != nil || !conf.Conflicts || !strings.Contains(string(conf.Merged), "<<<<<<<") {
		t.Fatalf("ütközés: %v %+v", err, conf)
	}
}

func TestFixup(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, ".config/app/x.conf")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("path=/home/old/Applications/x\nrun=/run/user/1000/sock\nexact=/home/old\nother=/home/oldish\n"), 0o644)
	os.Symlink("/home/old/target", filepath.Join(home, ".config/app/link"))
	bin := filepath.Join(home, ".config/app/bin")
	os.WriteFile(bin, []byte("/home/old/\x00"), 0o644)
	changed := Fixup(home, []string{".config/app"}, "/home/old", "/home/new", 1000, 1001)
	b, _ := os.ReadFile(p)
	want := "path=/home/new/Applications/x\nrun=/run/user/1001/sock\nexact=/home/new\nother=/home/oldish\n"
	if string(b) != want {
		t.Errorf("javítás:\n%s\nvárt:\n%s", b, want)
	}
	if l, _ := os.Readlink(filepath.Join(home, ".config/app/link")); l != "/home/new/target" {
		t.Errorf("symlink: %s", l)
	}
	if bb, _ := os.ReadFile(bin); string(bb) != "/home/old/\x00" {
		t.Error("bináris fájlt nem szabad módosítani")
	}
	if len(changed) != 2 {
		t.Errorf("módosított: %v", changed)
	}
}

func TestFixGitCredentialHelper(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	os.WriteFile(p, []byte("[credential \"https://github.com\"]\n\thelper =\n\thelper = !/home/u/.local/share/mise/installs/gh/2.104.0/gh_2.104.0_linux_amd64/bin/gh auth git-credential\n"), 0o644)
	if !FixGitCredentialHelper(p) {
		t.Fatal("nem javított")
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "helper = !gh auth git-credential") || strings.Contains(string(b), "mise") {
		t.Errorf("%s", b)
	}
}

func TestSchemaVersion(t *testing.T) {
	if SchemaVersion([]byte(`{"version":2}`)) != 2 || SchemaVersion([]byte(`x`)) != -1 {
		t.Error("séma")
	}
}
