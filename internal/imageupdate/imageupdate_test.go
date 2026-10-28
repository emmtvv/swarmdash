package imageupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestNewest(t *testing.T) {
	tags := []string{
		"latest", "1.26.0", "1.27.1", "1.27.3", "1.29.0-rc1", "2.0.1", "2.1",
		"1.27.4-alpine", "1.28.0-alpine", "2.0.0-alpine", "v1.30.0",
	}
	cases := []struct {
		current string
		policy  Policy
		want    string
	}{
		{"1.27.1", PolicyPatch, "1.27.3"},
		{"1.26.0", PolicyPatch, ""},
		{"1.27.1-alpine", PolicyPatch, "1.27.4-alpine"},
		{"1.27.1", PolicyMinor, "1.27.3"},
		{"1.27.1", PolicyMajor, "2.0.1"},
		{"1.27.3", PolicyMinor, ""},
		{"1.27.1-alpine", PolicyMinor, "1.28.0-alpine"},
		{"1.27.1-alpine", PolicyMajor, "2.0.0-alpine"},
		{"2.0", PolicyMajor, "2.1"},
		{"v1.2.0", PolicyMinor, "v1.30.0"},
		{"1.27.1", PolicyOff, ""},
		{"latest", PolicyMajor, ""},
	}
	for _, c := range cases {
		if got := Newest(c.current, tags, c.policy); got != c.want {
			t.Errorf("Newest(%q, %s) = %q, want %q", c.current, c.policy, got, c.want)
		}
	}
}

func TestNewest_ComparesNumerically(t *testing.T) {
	if got := Newest("1.9.0", []string{"1.10.0", "1.9.9"}, PolicyMinor); got != "1.10.0" {
		t.Errorf("got %q, want 1.10.0", got)
	}
}

func TestParseRef(t *testing.T) {
	cases := []struct {
		image string
		want  Ref
	}{
		{"nginx:1.27.1@sha256:0123456789012345678901234567890123456789012345678901234567890123",
			Ref{Repository: "nginx", Domain: "docker.io", Path: "library/nginx", Tag: "1.27.1"}},
		{"ghcr.io/org/app:v2.0.0", Ref{Repository: "ghcr.io/org/app", Domain: "ghcr.io", Path: "org/app", Tag: "v2.0.0"}},
		{"localhost:5000/app:1.0", Ref{Repository: "localhost:5000/app", Domain: "localhost:5000", Path: "app", Tag: "1.0"}},
	}
	for _, c := range cases {
		got, err := ParseRef(c.image)
		if err != nil {
			t.Fatalf("ParseRef(%q): %v", c.image, err)
		}
		if got != c.want {
			t.Errorf("ParseRef(%q) = %+v, want %+v", c.image, got, c.want)
		}
	}
	if _, err := ParseRef("localhost:5000/app"); err == nil {
		t.Error("expected an error for an image without a tag")
	}
}

// TestListTags_TokenAuthAndPagination runs a fake registry that demands a
// bearer token and splits the tag list over two pages.
func TestListTags_TokenAuthAndPagination(t *testing.T) {
	var ts *httptest.Server
	ts = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			user, pass, _ := r.BasicAuth()
			if user != "bob" || pass != "secret" || r.URL.Query().Get("scope") != "repository:org/app:pull" {
				http.Error(w, "denied", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"token":"tok"}`))
		case r.Header.Get("Authorization") != "Bearer tok":
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+ts.URL+`/token",service="reg"`)
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Query().Get("last") == "":
			w.Header().Set("Link", `</v2/org/app/tags/list?n=1000&last=1.1>; rel="next"`)
			_, _ = w.Write([]byte(`{"tags":["1.0","1.1"]}`))
		default:
			_, _ = w.Write([]byte(`{"tags":["2.0"]}`))
		}
	}))
	defer ts.Close()

	ref, err := ParseRef(strings.TrimPrefix(ts.URL, "https://") + "/org/app:1.0")
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{HTTP: ts.Client()}
	tags, err := c.ListTags(context.Background(), ref, Credential{Username: "bob", Password: "secret"})
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if want := []string{"1.0", "1.1", "2.0"}; !reflect.DeepEqual(tags, want) {
		t.Errorf("tags = %v, want %v", tags, want)
	}
}
