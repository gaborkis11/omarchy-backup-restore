package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/gaborkis11/omarchy-backup-restore/internal/inventory"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

func cmdScan(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, i18n.T("JSON kimenet"))
	home := fs.String("home", "", i18n.T("más home mappa felmérése (teszteléshez)"))
	if err := fs.Parse(args); err != nil {
		return err
	}
	app, err := newApp(*home, false)
	if err != nil {
		return err
	}
	defer app.Close()
	inv, err := app.Scan(ctx, nil)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(inv.Items)
	}
	for _, cat := range inventory.Categories {
		items := inv.InCategory(cat)
		if len(items) == 0 {
			continue
		}
		var total int64
		for _, it := range items {
			if it.Selected {
				total += it.Size
			}
		}
		fmt.Printf(i18n.T("\n== %s (%s kiválasztva)\n"), categoryTitle(cat), inventory.HumanSize(total))
		for _, it := range items {
			mark := "[ ]"
			if it.Selected {
				mark = "[x]"
			}
			extra := ""
			if st := inv.Status[it.ID]; st != "" {
				extra = " <" + st + ">"
			}
			if it.Hardware != "" {
				extra += " {" + it.Hardware + "}"
			}
			fmt.Printf("  %s %-70s %9s%s\n", mark, trunc(it.Label, 70), inventory.HumanSize(it.Size), extra)
		}
	}
	fmt.Printf(i18n.T("\nÖsszesen kiválasztva: %s\n"), inventory.HumanSize(inv.SelectedSize()))
	if len(inv.Warnings) > 0 {
		fmt.Printf(i18n.T("\nFigyelmeztetések:\n  %s\n"), strings.Join(dedupe(inv.Warnings), "\n  "))
	}
	return nil
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
