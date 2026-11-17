# M0 próba (spike) – eredmények

Dátum: 2026-11-17. restic 0.19.1 és rclone 1.76.0, mindkettő forrásból fordítva
(`CGO_ENABLED=0 go install …`), statikusan linkelve, a `~/.local/share/obr/bin` mappában.

A hivatalos kiadási binárisok letöltését és futtatását a munkakörnyezet nem engedte,
ezért a pinelt verziókat a Go modulrendszer fordítja (a `sum.golang.org` ellenőrzi
őket). Ez ugyanolyan statikus binárist ad. A `make tools` cél ezt csinálja.

## Mit próbáltunk ki

| Próba | Eredmény |
|---|---|
| `udisksctl loop-setup` vfat image-dzsel, root nélkül | ✅ Automatikusan csatolódik a `/run/media/$USER/<LABEL>` alá, a csatolási opciók ugyanazok, mint a valódi lemeznél (`showexec`, `fmask=0022`) |
| `init` / `backup` / `check` / `restore` FAT32-n (loop) | ✅ |
| Ugyanez a valódi `BACKUP` lemezen (`omarchy-backup-spike/`, utána törölve) | ✅ |
| `--files-from-verbatim` relatív utakkal, cwd = home | ✅ A snapshot fája **a home-hoz relatív** (`/.config/...`), ezért `restore <snap>:/ --target <új home>` felhasználónévtől függetlenül működik |
| `restore <snap>:/home/olduser --target X` (alútvonal) | ✅ Akkor is működik, ha a mentés `/` cwd-ből készült |
| `--include-file` visszaállításnál (az alútvonalhoz relatív utak) | ✅ |
| `--stdin-from-command --stdin-filename meta.tar -- tar …` | ✅ |
| `dump <snap> /meta.tar` | ✅ |
| `--dry-run --json` visszaállításnál | ✅ Az összegzés kiírja a `files_restored` és `files_skipped` értéket |
| 0600-as jogosultság és a symlinkek megmaradnak (a tároló FAT32-n van) | ✅ |
| Kilépési kódok | ✅ Rossz jelszó: 12. Nincs tároló: 10. Olvashatatlan fájl: 3 (a snapshot ekkor is elkészül) |
| Növekményes mentés | ⚠️ A `--group-by host,tags` nem találja meg a szülőt, ha a futáshoz egyedi tag tartozik (`run-<id>`). **Megoldás:** az `obr` saját maga adja meg a `--parent <előző adat-snapshot>` kapcsolót. Így a második futás csak a változást írja (1634 bájt) |
| Kizárás abszolút úttal (`$HOME/.config/app/*/Login Data`), relatív mentésnél | ✅ Működik. A snapshotbeli `/.config/...` alak nem működik kizárásként |
| Kizárás alapnévvel (`Code Cache`) | ✅ Minden szinten illeszkedik |
| rclone backend csak környezeti változókkal (`RCLONE_CONFIG=/dev/null`, `RCLONE_CONFIG_OBRT_TYPE=local`) | ✅ |
| `rclone obscure -` (stdinről) | ✅ |
| Unix socket hossza | Nem releváns: a Python 108 karakteres korlátba ütközött. A scanner úgyis kizárja a socketeket |

## Ami eltér a tervtől

1. Az eszközöket nem letöltjük, hanem forrásból fordítjuk (lásd fent).
2. A Go-t a mise telepíti (`mise.toml`: `go = "1.27.4"`), mert a `sudo` jelszót kér. Az `omarchy-pkg-add go` is működne, de interaktív.
3. A meta snapshot nem `--stdin-from-command`-dal készül. Helyette egy `$XDG_RUNTIME_DIR`-ben lévő
   (tmpfs, tehát RAM, 0700-as) ideiglenes mappából mentjük, így ugyanúgy nem kerül lemezre.
   A rendszerfájlokat a `sudo obr helper collect` stdout-ra írja, és ez is a tmpfs mappába kerül.
   Egyszerűbb és jobban tesztelhető megoldás.
4. A szülő-snapshotot az `obr` választja ki (`--parent`), nem a `--group-by`.
5. A Huh helyett elég a Bubbles (textinput, spinner, progress, viewport). Kevesebb függőség,
   és ugyanaz a felület.
