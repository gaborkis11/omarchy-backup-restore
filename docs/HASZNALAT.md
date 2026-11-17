# Használati útmutató

Ez az útmutató a mindennapi használatot írja le: mentést, visszaállítást, NAS-t, ütemezést.
A friss gépre költözés részletei: [RESTORE-GUIDE.md](RESTORE-GUIDE.md).

## Indítás

Az alkalmazásmenüben (`Super + Space`) három indító van:

| Indító | Mire való |
|---|---|
| **Omarchy mentés** | Egy kattintás: megnyitja a mentést a legutóbb használt helyre. Ha ott a lemez, egyből a kiválasztó képernyő jön. |
| **Omarchy visszaállítás** | Visszaállítás. Ha csak egy lemezen van mentés, egyből azt nyitja meg. |
| **Omarchy Backup & Restore** | A teljes menü: mentés, visszaállítás, mentések kezelése, ütemezés, beállítások. |

Terminálból: `obr` (menü), `obr tui backup`, `obr tui restore`.

## Billentyűk

| Billentyű | Mit csinál |
|---|---|
| `↑` `↓` vagy `j` `k` | Mozgás |
| `Enter` | Választás. A listában kategória kinyitása / becsukása |
| `Space` | Pipa be / ki (kategórián: az egész kategória) |
| `→` / `←` | Kategória kinyitása / becsukása |
| `a` | Az aktuális kategória összes tétele be / ki |
| `e` | Minden kategória kinyitása / becsukása |
| `d` | Részletek panel ki / be |
| `n` | **Tovább** (a kiválasztó képernyőkről) |
| `Tab` | Űrlapon a következő mező |
| `Esc` | Vissza |
| `Ctrl+C` | Kilépés (futó műveletnél előbb megkérdezi) |

## Első mentés lépésről lépésre

1. **Dugd be a lemezt** (pendrive vagy külső lemez). Az Omarchy magától csatolja.
2. Indítsd el az **Omarchy mentés** indítót.
3. **Hova?** Válaszd ki a lemezt, például „Lemez: BACKUP”. A program megjegyzi, és legközelebb ezt a lépést kihagyja.
4. **Felmérés:** pár másodperc alatt végignézi a gépet.
5. **Mit mentsünk?** Kategóriák pipákkal. Az alapbeállítás jó: minden be van pipálva, kivéve az AI-modelleket és a nagy, ismeretlen mappákat.
   - `Enter` kinyit egy kategóriát. Alul látod, mi van az adott tételben.
   - Ami új az előző mentés óta, azt **ÚJ** címke jelöli.
   - Az **Egyéb** kategóriában az ismeretlen programok mappái vannak. Itt `m` = mindig mentsd, `s` = soha, `c` = gyorsítótár (nem kell). A program ezt megjegyzi.
   - Felül látod, mennyi a kiválasztott adat, és mennyi a szabad hely a lemezen.
   - `n` = tovább.
6. **Ellenőrzés:** ha fut egy program, amelynek az adatait mented (például a Chrome), szól. Ilyenkor:
   - bezárod, és újra ellenőrizteted;
   - vagy a **Konzisztens mentés** lehetőséget választod: pillanatképből ment, így futó programok mellett is ép lesz;
   - vagy folytatod így is.
7. **Rendszergazdai jelszó:** csak akkor kéri, ha rendszerszintű dolgot is mentesz (wifi, Bluetooth). A jelszót a `sudo` kéri, nem az obr. Ha kihagyod, ezek a tételek kimaradnak.
8. **A mentés jelszava:**
   - Első alkalommal kétszer kell megadni, legalább 8 karakter.
   - Kapsz egy **helyreállító kulcsot** (például `ABCDE-FGHJK-…`). **Írd fel vagy fotózd le!** Ha a jelszót elfelejted, ezzel is megnyitható a mentés.
   - Később már csak egyszer kéri.
9. **Mentés:** folyamatjelző, hátralévő idő. A végén a program ellenőrzi a mentést, és a lemezre teszi a visszaállító készletet (`kit/`).
10. **Kész:** az összegzés megmutatja, mennyi adat került a mentésbe. A második mentés már csak a változásokat írja, ezért sokkal gyorsabb.

> Ne húzd ki a lemezt, amíg a mentés fut!

## Mentés a Synology NAS-ra

Egyszer kell beállítani:

1. **A DSM-ben:**
   - Vezérlőpult → Fájlszolgáltatások → FTP fül → **SFTP szolgáltatás engedélyezése**.
   - Hozz létre egy megosztott mappát (például `backup`), vagy használj egy meglévőt. A felhasználódnak írási joga legyen hozzá.
2. **Omarchy mentés** → **Synology / NAS hozzáadása**:

| Mező | Mit írj be |
|---|---|
| NAS címe | a NAS IP-címe, pl. `192.168.1.10` (vagy `NAS.local`) |
| Kapcsolat | **SFTP** (ajánlott) vagy SMB |
| Megosztott mappa | például `backup` |
| Almappa | nem kötelező, például `omarchy` |
| Felhasználó / Jelszó | a DSM-belépésed |

A program kapcsolódik, és a NAS bekerül a célhelyek közé. Innentől ugyanúgy megy, mint a lemezes mentés.
A NAS jelszavát csak az adott munkamenetre jegyzi meg, kivéve, ha bekapcsolod az ütemezett mentést.

## Ütemezett (automatikus) mentés

Menü → **Ütemezett mentés** → **Bekapcsolás**:

1. Válaszd ki a célhelyet.
2. Válaszd ki a gyakoriságot: naponta 20:00, hetente vasárnap 20:00, vagy egyéni.
3. Add meg a mentés jelszavát (és a NAS jelszavát, ha NAS-ra mentesz).
4. Fogadd el a jelszó tárolását. Felügyelet nélküli mentéshez a jelszót a gépen kell tárolni: `~/.config/obr/secrets/`, csak a te felhasználód olvashatja.

A mentés a háttérben fut, a végén értesítést kapsz. Ha a gép ki volt kapcsolva, bekapcsolás után pótolja.
Lemezre ütemezett mentésnél a lemeznek csatlakoztatva kell lennie, ezért ez NAS-ra a legkényelmesebb.

## Mentések kezelése

Menü → **Mentések kezelése** → célhely → jelszó. A listában:

| Billentyű | Mit csinál |
|---|---|
| `Enter` | Visszaállítás ebből a mentésből |
| `c` | Gyors ellenőrzés (szerkezet) |
| `C` | Teljes ellenőrzés: minden adatot beolvas, sokáig tart |
| `u` | A visszaállító készlet (`kit/`) frissítése |
| `t` | Régi mentések törlése a megőrzési szabály szerint (Beállítások) |
| `x` | Egy mentés törlése |

## Beállítások

- **Napi / heti / havi mentések:** hány régi mentés maradjon meg. Üresen minden marad. Javaslat: 7 / 4 / 12.
- **Konzisztens mentés:** mindig pillanatképből mentsen. Rendszergazdai jelszót kér.
- **Törlés: …:** célhely eltávolítása a listából. A mentést magát nem törli.

## Mi kerül vissza, és honnan?

| Mi | Honnan jön vissza |
|---|---|
| Telepített programok (pacman, AUR) | **Újratelepítés a tárolókból, a legfrissebb verzióval.** A mentés csak a listát tartalmazza. |
| Git-projektek, amelyek fent vannak a GitHubon (például WhisperRocket) | **Letöltés a GitHubról**, utána a helyi, nem verziókezelt fájlok (például `config.json`) a mentésből. A telepítőt (`install.sh`) felajánlja. |
| Git-projektek feltöltetlen változásokkal | A mentésből, egészben (a `venv`, `node_modules` stb. nélkül). |
| Kézzel letöltött programok (AppImage, `~/Applications`, `~/.local/bin`) | A mentésből, fájlként. |
| Omarchy webappok | Az Omarchy hozza létre újra (`omarchy-webapp-install`). |
| Téma, kapcsolók, betűtípus | Az Omarchy saját parancsai állítják be újra. |
| Alkalmazások beállításai és adatai, kulcsok, fájlok | A mentésből. |
| mise-eszközök (Claude Code, gh…) | Az Omarchy mise-beállítása telepíti őket első használatkor. A beállításaik (például `~/.claude`) a mentésből jönnek. |
