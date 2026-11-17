# Architecture

```
cmd/obr/                 entry point
internal/cli/            sub-commands (scan, backup, restore, list, check, prune, dest, schedule, undo, kit, install)
internal/tui/            Bubble Tea v2 interface (screen stack: menu → destination → … → summary)
internal/i18n/           translations: Hungarian source text is the key, en.go maps it to English (default)
internal/inventory/      survey: catalog apps, Omarchy seed diffs, replayable state, packages,
                         git projects, system, personal files, unknown data
internal/catalog/        embedded app catalog (catalog.toml) + user decisions (overrides.toml)
internal/backup/         one backup run: data snapshot + meta snapshot, restore kit, retention
internal/restore/        restore: candidates, preflight, ordered steps, journal, undo
internal/engine/         restic/rclone wrapper, destinations (local drive, SFTP, SMB), drive list
internal/merge/          three-way merge (git merge-file), path fixups
internal/helper/         `obr helper` – the only code that runs as root (allowlist based)
internal/sys/            machine facts: paths, Omarchy version, hardware fingerprint, pacman
internal/manifest/       manifest.json (description of one backup)
internal/config/         ~/.config/obr/config.toml (destinations, schedule, retention, language), stored secrets
internal/runner/         running external commands + a recording fake for tests
test/integration/        full backup → restore round trip with real restic
```

## Data model

Every run creates two restic snapshots that share a `run-<id>` tag.

| Snapshot | Tags | Content | Notes |
|---|---|---|---|
| Data | `obr-data` | The selected home-relative paths | The backup runs from the home folder, so the snapshot tree is `/.config/…`. `restore <snap>:/ --target <new home>` therefore works for any user name. |
| Meta | `obr-meta`, `src-<host>`, `user-<user>` | `manifest.json`, `seeds/` (the `/etc/skel` versions at backup time, used for three-way merges), `dconf.ini`, `system.tar` (system files collected by the root helper) | A run is complete only when its meta snapshot exists. |

The destination layout is `<destination>/omarchy-backup/`:

| Path | Content |
|---|---|
| `repo/` | The restic repository. |
| `kit/` | `obr`, `restic`, `rclone`, `restore.sh`, `install.sh`. |
| `README.txt`, `LEIRAS.txt` | Plain-text instructions in English and Hungarian. They contain no secrets. |

`obr` picks the parent snapshot itself (`--parent` = the newest `obr-data`). Restic cannot find it on its own because every run has its own tag.

## Survey

- The **catalog** (`internal/catalog/catalog.toml`) describes known applications:
  - paths and excludes;
  - secret parts, shown as separate items;
  - requirements, packages and post-restore commands;
  - English labels (`label_en`, `note_en`, `secret_label_en`).
- **Seeds.** Every `/etc/skel` file is compared with the user's copy. Only modified and user-only files are backed up, grouped by folder (for example `seed:.config/hypr`).
- **Replay** covers state that is re-created rather than copied:
  - the theme, own themes (as a git URL or as files) and the font;
  - Hyprland and flag toggles, and dconf;
  - web apps (`Exec=omarchy-launch-webapp`) and other launchers.
- **Packages.** `pacman -Qe`, minus `omarchy-pkg-defaults`, the hardware pool (`omarchy-other.packages`) and the ISO pacstrap set, is what the user installed. Hardware-specific packages get a tag.
- **Git projects.** If the tree is clean and pushed, only untracked files are backed up and the project is cloned again on restore. Otherwise the whole tree is backed up, without heavy folders.
- **System:**
  - units (`*.wants` symlinks created after installation);
  - groups and `localectl` / `timedatectl` settings;
  - NetworkManager connections, Bluetooth pairings and printers;
  - `/etc` files changed after installation, except those on the deny list.
- **Other.** Every `~/.config/*`, `~/.local/share/*`, `~/.local/state/*`, `~/.local/*` and `~/.*` entry that nothing covers and that is not junk.
- **Comparison.** The latest local manifest (`~/.local/state/obr/last-<destination>.json`) is used to mark new and changed items and to keep the previous selection.

## Restore steps

Each step's state is written to `~/.local/state/obr/restore-<ts>/journal.json`, which `obr undo` uses.

1. **preflight:** `omarchy-snapshot create`, and optionally `omarchy-update-system-pkgs`.
2. **packages:** `omarchy-pkg-add` / `omarchy-pkg-aur-add`, only for packages that are missing and available.
3. **keyring:** stop gnome-keyring, replace the file, start it again.
4. **system:**
   - `systemctl enable --now`, `usermod -aG`, `localectl`, `timedatectl`, `hostnamectl`;
   - `sudo obr helper apply < system.tar` (allowlist; Bluetooth only onto the same adapter).
5. **git:** clone, or `git pull` when the project is already there.
6. **files:**
   - items marked `replace` are moved to `undo/` first;
   - a dry run (`-vv`) lists the files that would be overwritten, and they are copied to `undo/`;
   - then `restic restore --overwrite if-changed` runs.
7. **seeds:**
   - if the default did not change, the user's version is restored;
   - otherwise `git merge-file` merges the two;
   - on a conflict the user decides, and the other version is kept as `*.obr-uj-alap-*` / `*.obr-sajat-*`;
   - a change in the JSON schema version always asks.
8. **replay:** `omarchy-theme-set`, `omarchy-font-set`, `omarchy-hyprland-toggle`, `omarchy-toggle`, `dconf load`, `omarchy-webapp-install`.
9. **recipes:** project installers, run through `tea.ExecProcess` with visible output.
10. **post:** the catalog's `post_restore` commands (for example `gh auth setup-git`).
11. **migrations:** the selected Omarchy migrations run against a temporary `OMARCHY_MIGRATION_STATE`. The real markers are never touched.
12. **fixups:**
    - old home → new home, `/run/user/<uid>`, symlinks;
    - git credential helper paths;
    - `update-desktop-database`, `hyprctl reload`.

## Root helper

`sudo -n <absolute path of obr> helper <verb>`:

- `collect [--nm] [--bt MAC] [--printers] [--etc REL]...`: writes a tar stream to stdout.
- `apply …`: reads a tar stream from stdin and writes only allowlisted paths.
  - NetworkManager, Bluetooth and printer files get mode 0600 and are owned by root.
  - Bluetooth pairings are written only when `--bt-from` equals `--bt`.
- `snapshot-create` / `snapshot-delete`: a read-only btrfs snapshot of `/home` at `/home/.obr-snapshot`, used for consistent backups.

The deny list is `inventory.EtcDeny`. The helper's tests check that denied files are rejected.

## Translations

The user-facing strings are written in Hungarian in the source and wrapped in `i18n.T(...)`. Package-level errors use `i18n.Err(...)`, which translates when the error is printed. `internal/i18n/en.go` maps every string to English, and `TestEveryKeyTranslated` fails when a translation is missing or the printf verbs differ.
