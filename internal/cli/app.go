// Package cli wires the subcommands and the TUI together.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/gaborkis11/omarchy-backup-restore/internal/catalog"
	"github.com/gaborkis11/omarchy-backup-restore/internal/config"
	"github.com/gaborkis11/omarchy-backup-restore/internal/engine"
	"github.com/gaborkis11/omarchy-backup-restore/internal/helper"
	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
	"github.com/gaborkis11/omarchy-backup-restore/internal/inventory"
	"github.com/gaborkis11/omarchy-backup-restore/internal/manifest"
	"github.com/gaborkis11/omarchy-backup-restore/internal/sys"
	"github.com/gaborkis11/omarchy-backup-restore/internal/tui"
	"github.com/gaborkis11/omarchy-backup-restore/internal/version"
)

// App bundles shared state.
type App struct {
	Env      *sys.Env
	Catalog  *catalog.Catalog
	Override *catalog.Overrides
	Config   *config.Config
	LogPath  string
	logFile  *os.File
}

func newApp(home string, dry bool) (*App, error) {
	env, err := sys.DetectEnv(sys.Options{Home: home, DryRun: dry})
	if err != nil {
		return nil, err
	}
	f, p, err := env.NewLogFile("obr")
	if err == nil {
		env, _ = sys.DetectEnv(sys.Options{Home: home, DryRun: dry, LogFile: f})
	}
	cat, err := catalog.Load()
	if err != nil {
		return nil, err
	}
	ov, err := catalog.LoadOverrides(env.ConfigDir)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(env.ConfigDir)
	if err != nil {
		return nil, err
	}
	i18n.Set(i18n.Pick(cfg.Language))
	return &App{Env: env, Catalog: cat, Override: ov, Config: cfg, LogPath: p, logFile: f}, nil
}

// Close releases the log file.
func (a *App) Close() {
	if a.logFile != nil {
		a.logFile.Close()
	}
}

// Scan runs the inventory.
func (a *App) Scan(ctx context.Context, prev *manifest.Manifest) (*inventory.Inventory, error) {
	s := &inventory.Scanner{Env: a.Env, Catalog: a.Catalog, Override: a.Override, Prev: prev}
	return s.Scan(ctx)
}

func categoryTitle(c string) string { return tui.CategoryTitle(c) }

// Main is the entry point.
func Main(args []string) error {
	if len(args) == 0 || args[0] == "tui" {
		// The TUI reads Ctrl+C as a key. SIGINT only arrives while a child
		// (sudo prompt, install recipe) owns the terminal; it must not kill obr.
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer cancel()
		mode := ""
		if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
			mode = args[1]
		}
		return runTUI(ctx, mode)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch args[0] {
	case "scan":
		return cmdScan(ctx, args[1:])
	case "backup":
		return cmdBackup(ctx, args[1:])
	case "restore":
		return cmdRestore(ctx, args[1:])
	case "list", "snapshots":
		return cmdList(ctx, args[1:])
	case "check":
		return cmdCheck(ctx, args[1:])
	case "prune":
		return cmdPrune(ctx, args[1:])
	case "dest", "destination":
		return cmdDest(ctx, args[1:])
	case "schedule":
		return cmdSchedule(ctx, args[1:])
	case "undo":
		return cmdUndo(ctx, args[1:])
	case "kit":
		return cmdKit(ctx, args[1:])
	case "helper":
		return helper.Main(ctx, args[1:])
	case "install":
		return cmdInstall(ctx, args[1:])
	case "version", "--version", "-v":
		fmt.Println("obr", version.Version)
		return nil
	case "help", "-h", "--help":
		usage()
		return nil
	}
	usage()
	return fmt.Errorf(i18n.T("ismeretlen parancs: %s"), args[0])
}

func usage() {
	fmt.Print("Omarchy Backup & Restore (obr) " + version.Version + i18n.Text("usage"))
}

// ExitCode maps errors to process exit codes.
func ExitCode(err error) int {
	switch {
	case errors.Is(err, engine.ErrBadPass):
		return 12
	case errors.Is(err, engine.ErrNoRepo):
		return 10
	case errors.Is(err, flag.ErrHelp):
		return 0
	}
	return 1
}
