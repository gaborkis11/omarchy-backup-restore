package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalBrowserMarksBackups(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "drive/sub/omarchy-backup/repo"), 0o755)
	os.MkdirAll(filepath.Join(root, "drive/other"), 0o755)
	s := &localBrowser{p: purposeRestore}
	s.open(filepath.Join(root, "drive"))
	if !s.marks["sub"] || s.marks["other"] {
		t.Fatalf("marks: %v", s.marks)
	}
	if s.rows()[0] == "choose" {
		t.Error("restore: a folder without a backup must not be choosable")
	}
	s.open(filepath.Join(root, "drive/sub"))
	if !s.backup || s.rows()[0] != "choose" {
		t.Errorf("backup folder should be choosable: %v", s.rows())
	}
	for _, r := range s.rows() {
		if r == "dir:omarchy-backup" {
			t.Error("the omarchy-backup folder itself should be hidden")
		}
	}
	b := &localBrowser{p: purposeBackup}
	b.open(filepath.Join(root, "drive/other"))
	if b.rows()[0] != "choose" {
		t.Error("backup: any folder is choosable")
	}
}
