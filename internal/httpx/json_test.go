package httpx

import (
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
