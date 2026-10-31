// Package i18n translates the admin UI. Templates and code refer to
// messages by key - {{t "common.services"}} - and catalogs map keys to
// text. Keys are "<namespace>.<slug>": the namespace is the template the
// message belongs to ("common" when several share it, "ui" for the static
// scripts, "errors"/"flash" for server-side notices), the slug a short
// snake_case form of the English text. Catalogs live in locales/<tag>.json
// as flat {"key": "text"} objects, embedded into the binary: en.json is
// the reference catalog holding every key, the others translate it. A key
// missing from a catalog falls back to English, then to the key itself.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed locales/*.json
var localeFS embed.FS

// Default is the language used until a user picks one, and the one
// missing translations fall back to.
const Default = "en"

// Language is one selectable UI language.
type Language struct {
	Tag  string // BCP 47 primary tag, e.g. "ru"
	Name string // the language's own name for itself, e.g. "Русский"
}

// Languages lists every supported UI language, in the order the picker
// shows them.
var Languages = []Language{
	{"en", "English"},
	{"ru", "Русский"},
	{"uk", "Українська"},
	{"de", "Deutsch"},
	{"es", "Español"},
	{"pt", "Português"},
	{"fr", "Français"},
	{"zh", "中文"},
	{"ka", "ქართული"},
}

var catalogs = loadCatalogs()

func loadCatalogs() map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, l := range Languages {
		b, err := localeFS.ReadFile("locales/" + l.Tag + ".json")
		if err != nil {
			continue
		}
		m := map[string]string{}
		if err := json.Unmarshal(b, &m); err != nil {
			panic(fmt.Sprintf("i18n: locales/%s.json: %v", l.Tag, err))
		}
		out[l.Tag] = m
	}
	return out
}

// Supported reports whether tag is one of Languages.
func Supported(tag string) bool {
	for _, l := range Languages {
		if l.Tag == tag {
			return true
		}
	}
	return false
}

// lookup returns key's text in lang, or in Default when lang lacks it, and
// the language the text came from (for picking plural forms).
func lookup(lang, key string) (string, string, bool) {
	if tr := catalogs[lang][key]; tr != "" {
		return tr, lang, true
	}
	if tr := catalogs[Default][key]; tr != "" {
		return tr, Default, true
	}
	return key, lang, false
}

// Has reports whether key is in the reference (English) catalog.
func Has(key string) bool {
	_, ok := catalogs[Default][key]
	return ok
}

// T translates key into lang (falling back to English, then to key
// itself, so untranslatable text such as an error string passes through),
// then formats it with args (fmt verbs) if any are given.
func T(lang, key string, args ...any) string {
	msg, _, _ := lookup(lang, key)
	if len(args) > 0 {
		return fmt.Sprintf(msg, args...)
	}
	return msg
}

// N translates a message with a count, picking the plural form for n. The
// catalog entry lists its language's forms separated by "|", in the order
// pluralForm numbers them (en.json: "one|other"). n is the first format
// argument, followed by args.
func N(lang, key string, n int, args ...any) string {
	fargs := append([]any{n}, args...)
	msg, from, _ := lookup(lang, key)
	forms := strings.Split(msg, "|")
	i := pluralForm(from, n)
	if i >= len(forms) {
		i = len(forms) - 1
	}
	return fmt.Sprintf(forms[i], fargs...)
}

// pluralForm is the index of the plural form lang uses for n (CLDR
// cardinal rules, integers only).
func pluralForm(lang string, n int) int {
	if n < 0 {
		n = -n
	}
	switch lang {
	case "ru", "uk": // one, few, many
		switch {
		case n%10 == 1 && n%100 != 11:
			return 0
		case n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14):
			return 1
		default:
			return 2
		}
	case "fr": // one (0 and 1), other
		if n <= 1 {
			return 0
		}
		return 1
	case "zh", "ka": // a single form
		return 0
	default: // one, other
		if n == 1 {
			return 0
		}
		return 1
	}
}

// Catalog returns lang's translations of the given keys, for handing a
// subset to client-side JS.
func Catalog(lang string, keys []string) map[string]string {
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		out[k] = T(lang, k)
	}
	return out
}
