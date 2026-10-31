package i18n

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func TestTFallsBackAndFormats(t *testing.T) {
	if got := T("en", "services.selected", 3); got != "3 selected" {
		t.Fatalf("T(en) = %q", got)
	}
	if got := T("xx", "services.selected", 3); got != "3 selected" {
		t.Fatalf("T(unknown lang) = %q, want the English text", got)
	}
	if got := T("ru", "no such key"); got != "no such key" {
		t.Fatalf("T(unknown key) = %q", got)
	}
}

var verbRE = regexp.MustCompile(`%[-+# 0-9.]*[vdsfqxXT%]`)

// A translation that drops or reorders a printf verb would render garbage
// (or %!d(MISSING)) at runtime, and one that loses markup would break the
// page - catch both at test time for every catalog, against the English
// text (its last, "other" form for plurals).
func TestCatalogsKeepVerbsAndMarkup(t *testing.T) {
	for _, l := range Languages {
		if l.Tag == Default {
			continue
		}
		b, err := localeFS.ReadFile("locales/" + l.Tag + ".json")
		if err != nil {
			t.Fatalf("missing catalog for %s: %v", l.Tag, err)
		}
		var m map[string]string
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v", l.Tag, err)
		}
		for k, v := range m {
			enForms := strings.Split(catalogs[Default][k], "|")
			src := enForms[len(enForms)-1]
			// Plural translations (see N) list several forms; each must
			// stand on its own.
			for _, form := range strings.Split(v, "|") {
				if strings.Join(verbRE.FindAllString(src, -1), ",") != strings.Join(verbRE.FindAllString(form, -1), ",") {
					t.Errorf("%s: %s: %q -> %q: format verbs differ", l.Tag, k, src, form)
				}
				if strings.Count(src, "<") != strings.Count(form, "<") {
					t.Errorf("%s: %s: %q -> %q: markup differs", l.Tag, k, src, form)
				}
			}
		}
	}
}

func TestPluralForms(t *testing.T) {
	for _, tc := range []struct {
		lang string
		n    int
		want int
	}{
		{"en", 1, 0}, {"en", 0, 1}, {"en", 5, 1},
		{"ru", 1, 0}, {"ru", 21, 0}, {"ru", 11, 2}, {"ru", 3, 1}, {"ru", 13, 2}, {"ru", 24, 1}, {"ru", 5, 2},
		{"fr", 0, 0}, {"fr", 2, 1},
		{"zh", 7, 0},
	} {
		if got := pluralForm(tc.lang, tc.n); got != tc.want {
			t.Errorf("pluralForm(%s, %d) = %d, want %d", tc.lang, tc.n, got, tc.want)
		}
	}
	if got := N("en", "swarm.containers", 1); got != "1 container" {
		t.Errorf("N(en, 1) = %q", got)
	}
	if got := N("en", "swarm.containers", 3); got != "3 containers" {
		t.Errorf("N(en, 3) = %q", got)
	}
	if got := N("ru", "swarm.containers", 5); got != "5 контейнеров" {
		t.Errorf("N(ru, 5) = %q", got)
	}
	if got := N("xx", "swarm.containers", 1); got != "1 container" {
		t.Errorf("N(unknown lang, 1) = %q, want the English form", got)
	}
}

// en.json is the reference: every key any other catalog translates must be
// in it, so it stays the complete list of UI messages.
func TestEnglishCatalogIsComplete(t *testing.T) {
	en := catalogs[Default]
	if len(en) == 0 {
		t.Fatal("en.json is empty or missing")
	}
	for tag, m := range catalogs {
		for k := range m {
			if _, ok := en[k]; !ok {
				t.Errorf("%s.json translates %q, which en.json lacks", tag, k)
			}
		}
	}
	if got := N("en", "swarm.containers", 2); got != "2 containers" {
		t.Errorf("N(en, 2) = %q", got)
	}
}
