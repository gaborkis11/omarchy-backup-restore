#!/bin/bash
# Omarchy Backup & Restore – install only / telepítés visszaállítás nélkül.
#
# EN: bash /run/media/$USER/*/omarchy-backup/kit/install.sh
#
# Új vagy újratelepített Omarchy gépen, ha csak a programot akarod feltenni
# (például hogy mentést készíts róla):
#
#     bash /run/media/$USER/*/omarchy-backup/kit/install.sh
#
# Utána a program ikonja ott van a dockban és az alkalmazásmenüben
# („Omarchy Backup & Restore”).
set -euo pipefail

KIT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN="$HOME/.local/share/obr/bin"

mkdir -p "$BIN" "$HOME/.local/bin"
for f in obr restic rclone; do
  if [[ -f $KIT/$f ]]; then
    install -m 0755 "$KIT/$f" "$BIN/$f"
  fi
done

# indítók az alkalmazásmenübe + ikon a dockba
"$BIN/obr" install

echo
echo "Done. Start it from the dock icon, Super + Space → \"Omarchy Backup & Restore\", or: obr"
echo "Kész. Indítás: dock-ikon, Super + Space → „Omarchy Backup & Restore”, vagy: obr"
