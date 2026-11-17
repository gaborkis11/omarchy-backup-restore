OMARCHY BACKUP & RESTORE – WHAT IS THIS FOLDER?
================================================

This is an encrypted backup of an Omarchy machine: settings, program list,
keys, files. It was made by "Omarchy Backup & Restore" (obr).
https://github.com/gaborkis11/omarchy-backup-restore

Last backup: {{.Created}}
Omarchy version: {{.OmarchyVersion}}

Folders:
  repo/   the encrypted backup (restic repository) – do NOT edit by hand
  kit/    the program itself (obr, restic, rclone) and two launchers:
            restore.sh – installs AND restores
            install.sh – only installs (e.g. to back up a new machine)

RESTORE ONTO A FRESH OMARCHY MACHINE
------------------------------------
1. Install Omarchy, log in, connect to the internet.
2. Plug in this drive (it shows up under /run/media/<user>/ by itself).
3. Open a terminal (Super + Enter) and type (works with any drive name):

     bash /run/media/$USER/*/omarchy-backup/kit/restore.sh

   You need this only once: the program installs itself (app menu, dock)
   and starts the restore. After that you just click its icon.

4. Enter the backup password, tick what you want back, done.

ONLY INSTALL THE PROGRAM (no restore)
-------------------------------------
     bash /run/media/$USER/*/omarchy-backup/kit/install.sh

If the backup is on a Synology NAS:
   smbclient //<NAS address>/<share> -U <user> -c 'prompt; cd <folder>/omarchy-backup/kit; mget *'
   bash restore.sh

IMPORTANT
---------
- Without the backup password the data can NOT be restored. There is no
  "forgot password" – that is what real encryption means. If you saved the
  recovery key, it works as a password too.
- Do not unplug the drive during a backup (FAT32 can get damaged). The
  program verifies the repository after every backup.
- This file contains no secrets; anyone may read it.
