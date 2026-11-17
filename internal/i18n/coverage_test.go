package i18n

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var keyRe = regexp.MustCompile(`i18n\.(?:T|Err)\(("(?:[^"\\\n]|\\.)*")\)`)

// TestEveryKeyTranslated scans the source tree and fails when a string passed
// to i18n.T / i18n.Err has no English translation.
func TestEveryKeyTranslated(t *testing.T) {
	root := filepath.Join("..", "..")
	var missing []string
	seen := map[string]bool{}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || strings.Contains(p, "/i18n/") {
			return nil
		}
		b, _ := os.ReadFile(p)
		for _, m := range keyRe.FindAllStringSubmatch(string(b), -1) {
			k, err := strconv.Unquote(m[1])
			if err != nil || seen[k] {
				continue
			}
			seen[k] = true
			if _, ok := en[k]; !ok {
				missing = append(missing, p+": "+k)
			}
		}
		return nil
	})
	if len(seen) < 100 {
		t.Fatalf("too few keys found (%d) – scanner broken?", len(seen))
	}
	for _, m := range missing {
		t.Errorf("missing English translation: %s", m)
	}
}

// TestFormatVerbs checks that translations keep the same printf verbs.
func TestFormatVerbs(t *testing.T) {
	verb := regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z%]`)
	for hu, e := range en {
		a, b := verb.FindAllString(hu, -1), verb.FindAllString(e, -1)
		if strings.Join(a, " ") != strings.Join(b, " ") {
			t.Errorf("verbs differ:\n  hu: %q %v\n  en: %q %v", hu, a, e, b)
		}
	}
}

func TestSwitch(t *testing.T) {
	Set(HU)
	if T("Kilépés") != "Kilépés" {
		t.Error("hu")
	}
	Set(EN)
	if T("Kilépés") != "Quit" {
		t.Error("en")
	}
	if Err("Kilépés").Error() != "Quit" {
		t.Error("err")
	}
}
