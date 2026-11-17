package i18n

// Text returns a longer, multi-line text by name in the current language.
func Text(name string) string {
	if Lang() == HU {
		return textsHU[name]
	}
	return textsEN[name]
}

var textsHU = map[string]string{
	"usage": `

Használat:
  obr                      terminálos felület (mentés, visszaállítás, kezelés)
  obr tui backup|restore   felület egyenesen a mentéssel / visszaállítással
  obr scan [--json]        a gép felmérése: mit mentene a program
  obr backup [kapcsolók]   mentés felület nélkül (pl. ütemezett futásnál)
  obr restore [kapcsolók]  visszaállítás felület nélkül
  obr list --dest NÉV      mentések listája
  obr check --dest NÉV     mentés ellenőrzése
  obr prune --dest NÉV     régi mentések törlése a megőrzési szabály szerint
  obr dest list|add|remove célhelyek kezelése
  obr schedule enable|disable|status   ütemezett mentés
  obr undo [mappa]         egy visszaállítás visszavonása (a felülírt fájlok vissza)
  obr kit --dest NÉV       a visszaállító készlet (kit) frissítése a célhelyen
  obr install              obr telepítése a ~/.local/bin alá + indító az alkalmazásmenübe
  obr version

Részletek: obr <parancs> -h
`,
	"schedule-warning": `Ütemezett mentéshez a programnak felügyelet nélkül kell megnyitnia a mentést,
ezért a jelszót a gépen tárolja: ~/.config/obr/secrets/ (csak a te felhasználód
olvashatja, 0600). Aki hozzáfér a bejelentkezett gépedhez, az a mentést is
meg tudja nyitni. A mentés ettől még titkosított marad a lemezen / NAS-on.`,
}

var textsEN = map[string]string{
	"usage": `

Usage:
  obr                      terminal interface (backup, restore, management)
  obr tui backup|restore   interface that starts directly with backup / restore
  obr scan [--json]        survey this machine: what would be backed up
  obr backup [flags]       backup without the interface (e.g. scheduled runs)
  obr restore [flags]      restore without the interface
  obr list --dest NAME     list backups
  obr check --dest NAME    verify a backup
  obr prune --dest NAME    delete old backups according to the retention policy
  obr dest list|add|remove manage destinations
  obr schedule enable|disable|status   scheduled backups
  obr undo [folder]        undo a restore (puts overwritten files back)
  obr kit --dest NAME      refresh the restore kit on a destination
  obr install              install obr into ~/.local/bin + app launchers and dock icon
  obr version

Details: obr <command> -h
`,
	"schedule-warning": `Scheduled backups have to open the backup without anyone present, so the
password is stored on this machine: ~/.config/obr/secrets/ (readable only by
your user, 0600). Anyone with access to your logged-in machine could open the
backup too. The backup itself stays encrypted on the drive / NAS.`,
}
