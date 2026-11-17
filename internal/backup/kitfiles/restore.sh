#!/bin/bash
# Omarchy Backup & Restore – restore launcher / visszaállító indító.
#
# EN: on a fresh Omarchy machine: bash /run/media/$USER/*/omarchy-backup/kit/restore.sh
#
# Friss Omarchy gépen futtasd így (a pendrive / külső lemez csatlakoztatása után):
#
#     bash /run/media/$USER/<LEMEZ>/omarchy-backup/kit/restore.sh
#
# A FAT32 lemezen a fájlok nem futtathatók, ezért kell elé a "bash".
# A szkript a programot a ~/.local/share/obr/bin mappába másolja, és elindítja
# a visszaállítást. Internetkapcsolat a programok újratelepítéséhez kell.
set -euo pipefail

KIT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE="$(dirname "$KIT")"
BIN="$HOME/.local/share/obr/bin"

mkdir -p "$BIN" "$HOME/.local/bin"
for f in obr restic rclone; do
  if [[ -f $KIT/$f ]]; then
    install -m 0755 "$KIT/$f" "$BIN/$f"
  fi
done

# Ha a rendszeren újabb restic van, azt is használhatja az obr (PATH),
# de a sajátunk mindig ott van a BIN mappában.
ln -sf "$BIN/obr" "$HOME/.local/bin/obr"

# indítók az alkalmazásmenübe (Omarchy mentés / visszaállítás)
"$BIN/obr" install >/dev/null 2>&1 || true

echo "Omarchy Backup & Restore installed / telepítve: $BIN/obr"

if [[ -d $BASE/repo ]]; then
  exec "$BIN/obr" tui restore --source "$BASE"
else
  # NAS-ról letöltött kit: a forrást a programban választod ki.
  exec "$BIN/obr" tui restore
fi
