package httpx

import (
	"mime"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, map[string]string{"hello": "world"})

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if got, want := strings.TrimSpace(rec.Body.String()), `{"hello":"world"}`; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestSetAttachment(t *testing.T) {
	t.Run("plain name", func(t *testing.T) {
		rec := httptest.NewRecorder()
		SetAttachment(rec, "backup.json")
		if got, want := rec.Header().Get("Content-Disposition"), `attachment; filename=backup.json`; got != want {
			t.Errorf("Content-Disposition = %q, want %q", got, want)
		}
	})

	t.Run("name with a quote can't break out of the parameter", func(t *testing.T) {
		rec := httptest.NewRecorder()
		name := `evil"; x=y.txt`
		SetAttachment(rec, name)
		got := rec.Header().Get("Content-Disposition")

		_, params, err := mime.ParseMediaType(got)
		if err != nil {
			t.Fatalf("Content-Disposition %q doesn't parse as a single valid media type: %v", got, err)
		}
		if _, hasX := params["x"]; hasX {
			t.Errorf("Content-Disposition %q smuggled in an extra %q parameter from the filename", got, "x")
		}
		if params["filename"] != name {
			t.Errorf("filename round-tripped as %q, want %q", params["filename"], name)
		}
	})
}

func TestReadJSON(t *testing.T) {
	t.Run("valid body", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"agent"}`))
		var v struct {
			Name string `json:"name"`
		}
		if err := ReadJSON(req, &v); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v.Name != "agent" {
			t.Fatalf("Name = %q, want %q", v.Name, "agent")
		}
	})

	t.Run("malformed body errors", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/", strings.NewReader(`{not json`))
		var v map[string]string
		if err := ReadJSON(req, &v); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}
