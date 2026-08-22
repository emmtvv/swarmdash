package secretenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolve(t *testing.T) {
	t.Run("plain env var", func(t *testing.T) {
		t.Setenv("FOO", "bar")
		v, err := Resolve("FOO")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v != "bar" {
			t.Fatalf("got %q, want %q", v, "bar")
		}
	})

	t.Run("file fallback", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "secret")
		writeFile(t, path, "s3cr3t")
		t.Setenv("FOO_FILE", path)
		v, err := Resolve("FOO")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v != "s3cr3t" {
			t.Fatalf("got %q, want %q", v, "s3cr3t")
		}
	})

	t.Run("plain var takes precedence over file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "secret")
		writeFile(t, path, "from-file")
		t.Setenv("FOO", "from-env")
		t.Setenv("FOO_FILE", path)
		v, err := Resolve("FOO")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v != "from-env" {
			t.Fatalf("got %q, want %q", v, "from-env")
		}
	})

	t.Run("file value trimmed", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "secret")
		writeFile(t, path, "  padded\n\t")
		t.Setenv("FOO_FILE", path)
		v, err := Resolve("FOO")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v != "padded" {
			t.Fatalf("got %q, want %q", v, "padded")
		}
	})

	t.Run("neither set returns empty string", func(t *testing.T) {
		v, err := Resolve("FOO")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v != "" {
			t.Fatalf("got %q, want empty", v)
		}
	})

	t.Run("nonexistent file path errors", func(t *testing.T) {
		t.Setenv("FOO_FILE", filepath.Join(t.TempDir(), "does-not-exist"))
		_, err := Resolve("FOO")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
}
