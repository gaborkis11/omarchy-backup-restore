package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/gaborkis11/omarchy-backup-restore/internal/inventory"

	"github.com/gaborkis11/omarchy-backup-restore/internal/i18n"
)

// selItem is one row in the selection tree (backed by an inventory item or a
// restore candidate).
type selItem struct {
	id, label, detail, cat, kind string
	size                         int64
	sel                          *bool
	enabled                      bool
	reason                       string
	status                       string // new | changed
	hardware                     string
	secret                       bool
	requires                     []string
}

type selRow struct {
	cat  string
	item *selItem
}

type selTree struct {
	cats     []string
	items    map[string][]*selItem
	byID     map[string]*selItem
	expanded map[string]bool
	cur, top int
	detail   bool
	sizes    bool // show sizes (backup)
	onToggle func(it *selItem, on bool)
	removed  []string
}

func newSelTree(sizes bool) *selTree {
	return &selTree{items: map[string][]*selItem{}, byID: map[string]*selItem{}, expanded: map[string]bool{}, sizes: sizes, detail: true}
}

func (t *selTree) add(it *selItem) {
	if _, ok := t.items[it.cat]; !ok {
		t.cats = append(t.cats, it.cat)
	}
	t.items[it.cat] = append(t.items[it.cat], it)
	t.byID[it.id] = it
}

// order sorts categories in the canonical order.
func (t *selTree) order() {
	var out []string
	for _, c := range inventory.Categories {
		if _, ok := t.items[c]; ok {
			out = append(out, c)
		}
	}
	t.cats = out
	for _, c := range t.cats {
		for _, it := range t.items[c] {
			if it.status != "" {
				t.expanded[c] = true // show what is new
			}
		}
	}
}

func (t *selTree) rows() []selRow {
	var out []selRow
	for _, c := range t.cats {
		out = append(out, selRow{cat: c})
		if t.expanded[c] {
			for _, it := range t.items[c] {
				out = append(out, selRow{cat: c, item: it})
			}
		}
	}
	return out
}

func (t *selTree) catState(c string) (sel, total int, selSize, allSize int64) {
	for _, it := range t.items[c] {
		if !it.enabled {
			continue
		}
		total++
		allSize += it.size
		if *it.sel {
			sel++
			selSize += it.size
		}
	}
	return
}

func (t *selTree) setItem(it *selItem, on bool) {
	if !it.enabled {
		return
	}
	if t.onToggle != nil {
		t.onToggle(it, on)
		return
	}
	*it.sel = on
	if on {
		for _, r := range it.requires {
			if dep := t.byID[r]; dep != nil && dep.enabled {
				*dep.sel = true
			}
		}
	}
}

func (t *selTree) totalSelected() (n int, size int64) {
	for _, c := range t.cats {
		s, _, sz, _ := t.catState(c)
		n += s
		size += sz
	}
	return
}

func (t *selTree) current() selRow {
	rows := t.rows()
	if t.cur >= len(rows) {
		t.cur = len(rows) - 1
	}
	if t.cur < 0 {
		t.cur = 0
	}
	if len(rows) == 0 {
		return selRow{}
	}
	return rows[t.cur]
}

// update handles keys; returns true when the key was consumed.
func (t *selTree) update(msg tea.Msg) bool {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return false
	}
	rows := t.rows()
	switch k.String() {
	case "up", "k":
		if t.cur > 0 {
			t.cur--
		}
	case "down", "j":
		if t.cur < len(rows)-1 {
			t.cur++
		}
	case "pgup":
		t.cur -= 10
		if t.cur < 0 {
			t.cur = 0
		}
	case "pgdown":
		t.cur += 10
		if t.cur > len(rows)-1 {
			t.cur = len(rows) - 1
		}
	case "home", "g":
		t.cur = 0
	case "end", "G":
		t.cur = len(rows) - 1
	case "right", "l":
		r := t.current()
		if r.item == nil {
			t.expanded[r.cat] = true
		}
	case "left", "h":
		r := t.current()
		t.expanded[r.cat] = false
		for i, rr := range t.rows() {
			if rr.item == nil && rr.cat == r.cat {
				t.cur = i
			}
		}
	case "enter":
		r := t.current()
		if r.item == nil {
			t.expanded[r.cat] = !t.expanded[r.cat]
		} else {
			t.detail = !t.detail
		}
	case "space", "x":
		r := t.current()
		if r.item != nil {
			t.setItem(r.item, !*r.item.sel)
		} else {
			s, n, _, _ := t.catState(r.cat)
			on := s < n
			for _, it := range t.items[r.cat] {
				t.setItem(it, on)
			}
		}
	case "a":
		r := t.current()
		s, n, _, _ := t.catState(r.cat)
		on := s < n
		for _, it := range t.items[r.cat] {
			t.setItem(it, on)
		}
	case "e":
		all := true
		for _, c := range t.cats {
			all = all && t.expanded[c]
		}
		for _, c := range t.cats {
			t.expanded[c] = !all
		}
	case "d":
		t.detail = !t.detail
	default:
		return false
	}
	return true
}

func (t *selTree) view(a *App, width, height int) string {
	th := a.th
	rows := t.rows()
	detailH := 0
	var detail []string
	if t.detail {
		detail = t.detailLines(a, width)
		detailH = len(detail) + 1
	}
	listH := height - detailH
	if listH < 4 {
		listH = 4
	}
	if t.cur < t.top {
		t.top = t.cur
	}
	if t.cur >= t.top+listH {
		t.top = t.cur - listH + 1
	}
	var b strings.Builder
	sizeW := 10
	for i := t.top; i < len(rows) && i < t.top+listH; i++ {
		r := rows[i]
		var line string
		if r.item == nil {
			s, n, sz, _ := t.catState(r.cat)
			arrow := "▸"
			if t.expanded[r.cat] {
				arrow = "▾"
			}
			box := "[ ]"
			switch {
			case n > 0 && s == n:
				box = "[x]"
			case s > 0:
				box = "[-]"
			}
			title := fmt.Sprintf("%s %s %s", arrow, box, CategoryTitle(r.cat))
			info := fmt.Sprintf("%d/%d", s, n)
			if t.sizes {
				info += "  " + inventory.HumanSize(sz)
			}
			newCnt := 0
			for _, it := range t.items[r.cat] {
				if it.status == "new" {
					newCnt++
				}
			}
			badge := ""
			if newCnt > 0 {
				badge = th.badgeNew.Render(fmt.Sprintf(i18n.T("  %d új"), newCnt))
			}
			left := th.h1.Render(title) + badge
			line = pad(left, width-lenW(info)-2) + th.mutedS.Render(info)
		} else {
			it := r.item
			box := "[ ]"
			if *it.sel {
				box = th.okS.Render("[x]")
			}
			label := it.label
			if !it.enabled {
				box = th.dimS.Render(" – ")
			}
			badge := ""
			switch it.status {
			case "new":
				badge = th.badgeNew.Render(i18n.T(" ÚJ"))
			case "changed":
				badge = th.badgeChg.Render(i18n.T(" VÁLTOZOTT"))
			}
			if it.secret {
				badge += th.dimS.Render(" ⚿")
			}
			sz := ""
			if t.sizes && it.size > 0 {
				sz = inventory.HumanSize(it.size)
			}
			avail := width - 8 - sizeW - lenW(badge)
			if it.enabled {
				line = "    " + box + " " + th.text.Render(pad(truncate(label, avail), avail))
			} else {
				line = "    " + box + " " + th.dimS.Render(pad(truncate(label, avail), avail))
			}
			line += badge + th.mutedS.Render(fmt.Sprintf("%*s", sizeW, sz))
		}
		if i == t.cur {
			line = th.accentS.Render("›") + line
		} else {
			line = " " + line
		}
		b.WriteString(line + "\n")
	}
	used := strings.Count(b.String(), "\n")
	for ; used < listH; used++ {
		b.WriteString("\n")
	}
	if t.detail {
		b.WriteString(th.dimS.Render(strings.Repeat("─", width)) + "\n")
		b.WriteString(strings.Join(detail, "\n"))
	}
	return b.String()
}

func (t *selTree) detailLines(a *App, width int) []string {
	th := a.th
	r := t.current()
	var out []string
	if r.item == nil {
		for _, l := range wrap(CategoryHint(r.cat), width-2) {
			out = append(out, th.mutedS.Render(l))
		}
		return limit(out, 4)
	}
	it := r.item
	out = append(out, th.h2.Render(truncate(it.label, width-2)))
	if it.reason != "" {
		out = append(out, th.warnS.Render(truncate(strings.TrimSpace(it.reason), width-2)))
	}
	if it.detail != "" {
		for _, l := range wrap(it.detail, width-2) {
			out = append(out, th.mutedS.Render(l))
		}
	}
	if len(it.requires) > 0 {
		var names []string
		for _, rq := range it.requires {
			if d := t.byID[rq]; d != nil {
				names = append(names, d.label)
			}
		}
		if len(names) > 0 {
			out = append(out, th.dimS.Render(truncate(i18n.T("Ehhez kell: ")+strings.Join(names, ", "), width-2)))
		}
	}
	out = append(out, th.dimS.Render(it.id))
	return limit(out, 6)
}

func limit(xs []string, n int) []string {
	if len(xs) > n {
		return append(xs[:n-1], "…")
	}
	return xs
}

func lenW(s string) int { return len([]rune(stripANSI(s))) }

func stripANSI(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		if r == 0x1b {
			esc = true
			continue
		}
		if esc {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				esc = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
