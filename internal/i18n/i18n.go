// Package i18n translates user-facing text. The Hungarian text in the source
// is the key; other languages map it to their translation (en.go).
// English is the default language.
package i18n

import (
	"os"
	"strings"
	"sync/atomic"
)

// Supported languages.
const (
	EN = "en"
	HU = "hu"
)

var lang atomic.Value // string

func init() { lang.Store(EN) }

// Set changes the current language ("" or unknown = English).
func Set(l string) {
	l = strings.ToLower(strings.TrimSpace(l))
	if l != HU {
		l = EN
	}
	lang.Store(l)
}

// Lang returns the current language.
func Lang() string { return lang.Load().(string) }

// Pick chooses the language: explicit setting, then $OBR_LANG, else English.
func Pick(configured string) string {
	if configured != "" {
		return configured
	}
	if l := os.Getenv("OBR_LANG"); l != "" {
		return l
	}
	return EN
}

// T translates a Hungarian source string into the current language.
// Missing translations fall back to the Hungarian text.
func T(hu string) string {
	if Lang() == HU {
		return hu
	}
	if en, ok := en[hu]; ok {
		return en
	}
	return hu
}

// Err is an error whose message is translated when it is printed, so
// package-level error values follow the language chosen at runtime.
type Err string

func (e Err) Error() string { return T(string(e)) }

// Missing lists Hungarian keys without an English translation (for tests).
func Missing(keys []string) []string {
	var out []string
	for _, k := range keys {
		if _, ok := en[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}

// Decimal returns the decimal separator of the current language.
func Decimal() string {
	if Lang() == HU {
		return ","
	}
	return "."
}
