package web

import (
	"encoding/json"
	"html/template"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"swarmdash/internal/i18n"
	"swarmdash/internal/store"
)

var catalogRE = regexp.MustCompile(`(?s)<script type="application/json" id="sd-i18n">(.*?)</script>`)

func TestRenderEmbedsJSCatalogAndLang(t *testing.T) {
	r := NewRenderer()
	w := httptest.NewRecorder()
	r.Render(w, "login_2fa.html", map[string]any{}, "csrf", "nonce", "de")
	body := w.Body.String()
	if !strings.Contains(body, `<html lang="de">`) {
		t.Fatal("layout does not carry the request language")
	}
	m := catalogRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no JS catalog in page")
	}
	var cat map[string]string
	if err := json.Unmarshal([]byte(m[1]), &cat); err != nil {
		t.Fatalf("JS catalog is not valid JSON: %v\n%s", err, m[1])
	}
	if len(cat) != len(JSMessages) {
		t.Fatalf("JS catalog has %d entries, want %d", len(cat), len(JSMessages))
	}
}

// TestRenderNewPages renders the pages added alongside two-factor auth,
// favorites, per-service usage and volume backups with representative
// data, catching template execution errors (bad field names, wrong func
// arguments) that only surface at render time.
func TestRenderNewPages(t *testing.T) {
	user := &store.User{Username: "alice", Role: "admin", Favorites: []string{"service:web"}}
	cases := map[string]any{
		"account_security.html": map[string]any{
			"User": user, "Languages": i18n.Languages, "Enrolling": true,
			"QR": template.HTML("<svg></svg>"), "Secret": "ABCD EFGH",
		},
		"account_recovery_codes.html": map[string]any{"User": user, "Codes": []string{"abcde-12345"}},
		"login_2fa.html":              map[string]any{"Error": "errors.invalid_code"},
		"volume_backups.html": map[string]any{
			"User": user, "SelectedID": "n1",
			"List": struct {
				Enabled  bool
				Backups  []map[string]any
				Restores []map[string]any
			}{
				Enabled: true,
				Backups: []map[string]any{
					{"ID": "data-1", "Volume": "data", "Status": "done", "SizeBytes": int64(2048), "CreatedAt": time.Now(), "Source": "backup", "Error": ""},
					{"ID": "data-2", "Volume": "data", "Status": "failed", "SizeBytes": int64(0), "CreatedAt": time.Now(), "Source": "upload", "Error": "boom"},
				},
				Restores: []map[string]any{
					{"ID": "r1", "BackupID": "data-1", "Volume": "data2", "Status": "running", "CreatedAt": time.Now(), "Error": ""},
				},
			},
			"Active": true,
		},
	}
	r := NewRenderer()
	for page, data := range cases {
		for _, lang := range []string{"en", "ru", "ka"} {
			w := httptest.NewRecorder()
			r.Render(w, page, data, "csrf", "nonce", lang)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "</html>") {
				t.Errorf("%s (%s): status %d, body %.300s", page, lang, w.Code, w.Body.String())
			}
		}
	}
}

var tmplCallRE = regexp.MustCompile(`(?:^|[^\w.$])(?:t|tHTML|tn)\s+"((?:[^"\\]|\\.)*)"`)

// TestMessageKeysExist fails when a template or script refers to a key
// en.json lacks - the page would show the bare key.
func TestMessageKeysExist(t *testing.T) {
	entries, _ := templatesFS.ReadDir("templates")
	for _, e := range entries {
		b, _ := templatesFS.ReadFile("templates/" + e.Name())
		for _, m := range tmplCallRE.FindAllStringSubmatch(string(b), -1) {
			if !i18n.Has(m[1]) {
				t.Errorf("%s: key %q is missing from en.json", e.Name(), m[1])
			}
		}
	}
	for _, k := range JSMessages {
		if !i18n.Has(k) {
			t.Errorf("JSMessages: key %q is missing from en.json", k)
		}
	}
}

var jsCallRE = regexp.MustCompile(`(?:^|[^\w.])t\(\s*"((?:[^"\\]|\\.)*)"`)
var inlineScriptRE = regexp.MustCompile(`(?s)<script nonce[^>]*>(.*?)</script>`)

// TestJSMessagesCoverScripts fails when a script calls t("...") with a
// string missing from JSMessages - it would silently stay English.
func TestJSMessagesCoverScripts(t *testing.T) {
	have := map[string]bool{}
	for _, m := range JSMessages {
		have[m] = true
	}
	var sources []string
	for _, name := range []string{"static/core.js", "static/app.js", "static/topology.js"} {
		b, err := staticFS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, string(b))
	}
	entries, _ := templatesFS.ReadDir("templates")
	for _, e := range entries {
		b, _ := templatesFS.ReadFile("templates/" + e.Name())
		for _, m := range inlineScriptRE.FindAllStringSubmatch(string(b), -1) {
			sources = append(sources, m[1])
		}
	}
	for _, src := range sources {
		for _, m := range jsCallRE.FindAllStringSubmatch(src, -1) {
			var key string
			if err := json.Unmarshal([]byte(`"`+m[1]+`"`), &key); err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(key, ".") {
				continue // a key completed at runtime, e.g. t("ws." + s)
			}
			if !have[key] {
				t.Errorf("t(%q) is used in a script but missing from JSMessages", key)
			}
		}
	}
}
