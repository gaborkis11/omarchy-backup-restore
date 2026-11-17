// Package config stores obr's own settings in ~/.config/obr/config.toml:
// backup destinations, schedule and retention. Passwords are never stored here.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

// Destination types.
const (
	Local = "local"
	SFTP  = "sftp"
	SMB   = "smb"
)

// Destination is a place where backups are kept.
type Destination struct {
	Name string `toml:"name"`
	Type string `toml:"type"` // local | sftp | smb

	// local
	Path  string `toml:"path,omitempty"`  // directory that will contain omarchy-backup/
	UUID  string `toml:"uuid,omitempty"`  // filesystem UUID of a removable drive (to find it again)
	Label string `toml:"label,omitempty"` // filesystem label

	// sftp / smb
	Host   string `toml:"host,omitempty"`
	Port   int    `toml:"port,omitempty"`
	User   string `toml:"user,omitempty"`
	Share  string `toml:"share,omitempty"`  // SMB share or first path element for Synology SFTP
	Folder string `toml:"folder,omitempty"` // sub folder inside the share (default: empty)
}

// Remote reports whether the destination is on the network.
func (d Destination) Remote() bool { return d.Type == SFTP || d.Type == SMB }

// Describe returns a short human label.
func (d Destination) Describe() string {
	switch d.Type {
	case SFTP, SMB:
		return fmt.Sprintf("%s – %s://%s@%s/%s", d.Name, d.Type, d.User, d.Host, strings.Trim(d.Share+"/"+d.Folder, "/"))
	default:
		return fmt.Sprintf("%s – %s", d.Name, d.Path)
	}
}

// Schedule describes unattended backups.
type Schedule struct {
	Enabled     bool   `toml:"enabled"`
	Destination string `toml:"destination"`
	OnCalendar  string `toml:"on_calendar"` // systemd OnCalendar, e.g. "daily" or "Sun 20:00"
}

// Retention policy (zero = keep everything).
type Retention struct {
	KeepLast    int `toml:"keep_last"`
	KeepDaily   int `toml:"keep_daily"`
	KeepWeekly  int `toml:"keep_weekly"`
	KeepMonthly int `toml:"keep_monthly"`
}

// Empty reports whether the policy keeps everything.
func (r Retention) Empty() bool {
	return r.KeepLast == 0 && r.KeepDaily == 0 && r.KeepWeekly == 0 && r.KeepMonthly == 0
}

// Config is the whole file.
type Config struct {
	Destinations []Destination `toml:"destination"`
	Schedule     Schedule      `toml:"schedule"`
	Retention    Retention     `toml:"retention"`
	// ConsistentSnapshot: back up from a read-only btrfs snapshot of /home.
	ConsistentSnapshot bool   `toml:"consistent_snapshot"`
	LastDestination    string `toml:"last_destination,omitempty"`
	Language           string `toml:"language,omitempty"`

	path string
}

// Load reads the config (missing file = empty config).
func Load(dir string) (*Config, error) {
	c := &Config{path: filepath.Join(dir, "config.toml")}
	if _, err := os.Stat(c.path); err != nil {
		return c, nil
	}
	if _, err := toml.DecodeFile(c.path, c); err != nil {
		return c, fmt.Errorf("%s: %w", c.path, err)
	}
	return c, nil
}

// Save writes the config atomically with 0600 permissions.
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	fmt.Fprintln(f, i18n.T("# Omarchy Backup & Restore beállítások. Jelszavak NINCSENEK ebben a fájlban."))
	if err := toml.NewEncoder(f).Encode(c); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// Find returns a destination by name.
func (c *Config) Find(name string) (Destination, bool) {
	for _, d := range c.Destinations {
		if d.Name == name {
			return d, true
		}
	}
	return Destination{}, false
}

// Upsert adds or replaces a destination by name.
func (c *Config) Upsert(d Destination) {
	for i := range c.Destinations {
		if c.Destinations[i].Name == d.Name {
			c.Destinations[i] = d
			return
		}
	}
	c.Destinations = append(c.Destinations, d)
}

// Remove deletes a destination.
func (c *Config) Remove(name string) {
	out := c.Destinations[:0]
	for _, d := range c.Destinations {
		if d.Name != name {
			out = append(out, d)
		}
	}
	c.Destinations = out
}

// SecretPath returns the path of a stored secret (only used when the user
// explicitly allows storing it, e.g. for scheduled backups).
func SecretPath(dir, kind, dest string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '/' || r == ' ' || r == '\\' {
			return '_'
		}
		return r
	}, dest)
	return filepath.Join(dir, "secrets", kind+"-"+safe)
}

// ReadSecret reads a stored secret.
func ReadSecret(dir, kind, dest string) (string, bool) {
	b, err := os.ReadFile(SecretPath(dir, kind, dest))
	if err != nil {
		return "", false
	}
	return strings.TrimRight(string(b), "\r\n"), true
}

// WriteSecret stores a secret in a 0600 file.
func WriteSecret(dir, kind, dest, value string) error {
	p := SecretPath(dir, kind, dest)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(value+"\n"), 0o600)
}

// DeleteSecret removes a stored secret.
func DeleteSecret(dir, kind, dest string) {
	_ = os.Remove(SecretPath(dir, kind, dest))
}
