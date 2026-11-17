// Command obr is Omarchy Backup & Restore.
package main

import (
	"fmt"
	"os"

	"github.com/gaborkis11/omarchy-backup-restore/internal/cli"
)

func main() {
	if err := cli.Main(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "obr:", err)
		os.Exit(cli.ExitCode(err))
	}
}
