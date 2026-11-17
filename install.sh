#!/bin/bash
# Omarchy Backup & Restore – install from source (git clone).
#
#   git clone https://github.com/gaborkis11/omarchy-backup-restore.git ~/Applications/omarchy-backup-restore
#   cd ~/Applications/omarchy-backup-restore
#   ./install.sh
#
# No root needed. The first run builds restic and rclone, which takes a few
# minutes (internet required).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

if [[ ! -d /usr/share/omarchy ]]; then
  echo "This program only works on Omarchy. / Ez a program csak Omarchy-n működik." >&2
  exit 1
fi

GO_VERSION=$(sed -n 's/^go *= *"\(.*\)"/\1/p' mise.toml)
if ! mise where "go@$GO_VERSION" >/dev/null 2>&1; then
  echo "Installing Go $GO_VERSION (mise)…"
  mise install "go@$GO_VERSION"
fi

if [[ ! -x $HOME/.local/share/obr/bin/restic || ! -x $HOME/.local/share/obr/bin/rclone ]]; then
  echo "Building restic and rclone (a few minutes)…"
  make tools
fi

make install
echo
echo "Done. Start it from the dock icon, Super + Space → \"Omarchy Backup & Restore\", or: obr"
