# Visszaállítás friss Omarchy telepítésre – lépésről lépésre

## 1. Omarchy telepítése

Telepítsd az Omarchy-t a szokásos módon. A felhasználónév és a lemeztitkosítás lehet
más, mint a régi gépen: a program kijavítja az útvonalakat, és a titkosítással
kapcsolatos beállításokat (automatikus bejelentkezés, `crypttab`, bootloader) nem
írja felül.

Az első bejelentkezés után csatlakozz az internetre. A programok telepítéséhez kell.

## 2. A mentés megnyitása

### Pendrive vagy külső lemez

Dugd be a lemezt. Az Omarchy magától csatolja a `/run/media/<felhasználó>/<LEMEZ>` alá.
Nyiss egy terminált (`Super + Enter`), és írd be:

```bash
bash /run/media/$USER/*/omarchy-backup/kit/restore.sh
```

(A `*` a lemez neve helyett áll, így bármilyen nevű lemezzel működik, ha csak egyen van mentés.)

A FAT32 lemezen a fájlok nem futtathatók, ezért kell elé a `bash`. A szkript a programot
a `~/.local/share/obr/bin` mappába másolja, felveszi az alkalmazásmenübe („Omarchy mentés”,
„Omarchy visszaállítás”), és elindítja a visszaállítást. Ezt a parancsot csak egyszer kell
beírni, utána minden az indítókból megy.

### Synology NAS

```bash
mkdir -p ~/obr-kit && cd ~/obr-kit
smbclient //<NAS címe>/<megosztás> -U <felhasználó> -c 'prompt; cd omarchy-backup/kit; mget *'
bash restore.sh
```

A programban válaszd a **Synology / NAS hozzáadása** lehetőséget, és add meg ugyanazokat az adatokat, mint mentéskor.

## 3. A visszaállítás menete

1. **Jelszó:** a mentés jelszava vagy a helyreállító kulcs.
2. **Melyik mentés:** alapból a legutóbbi.
3. **Előkészítés:**
   - **Rendszer-pillanatkép (ajánlott):** ha valami félremegy, a boot menüből visszaállítható.
   - **Rendszerfrissítés (ajánlott):** így a programok a legfrissebb verzióval települnek.
   - **Omarchy frissítő lépések:** csak akkor jelenik meg, ha az új Omarchy újabb, mint a
     mentéskori. A régebbi beállításaidra lefuttatja azokat a javításokat, amelyeket az
     Omarchy azóta kiadott.
   - **Telepítők:** például a WhisperRocket `install.sh` szkriptje. A kimenetét látni fogod.
4. **Mit kérsz vissza:** pipálható lista.
   - A szürke tételek ezen a gépen nem állíthatók vissza, például az NVIDIA-csomag AMD
     videokártyán, vagy a Bluetooth-párosítás más adapterrel.
   - A már telepített programok alapból nincsenek bepipálva.
5. **Indítás:** a rendszergazdai jelszót a `sudo` kéri.
6. Ha egy Omarchy-beállításfájl alapértéke azóta megváltozott, és a te módosításaid ütköznek
   vele, a program megkérdezi, melyik maradjon. A `v` billentyűvel megnézheted a különbséget.

## 4. A végén

- **Újraindítás:** ez kell ahhoz, hogy a kulcstartó, a csoporttagság (például `docker`) és a szolgáltatások teljesen érvényesüljenek.
- **Tailscale:** jelentkezz be újra a `sudo tailscale up --operator=$USER --accept-routes` paranccsal.
- **Bluetooth:** más gépen a fülhallgatókat újra kell párosítani.
- **Teendők:** az összegző képernyő felsorolja, mit kell még kézzel megcsinálnod.

## Ha valami nem tetszik

```bash
obr undo          # a legutóbbi visszaállítás visszavonása (a felülírt fájlok vissza)
```

A telepített programok és a rendszerbeállítások ettől nem tűnnek el. Ezeket a rendszer-pillanatképből
tudod visszaállítani, a boot menü **Snapshots** pontjában.

Naplók: `~/.local/state/obr/logs/`.
