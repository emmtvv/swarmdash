package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/volume"

	"swarmdash/internal/dockerutil"
)

func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if strings.HasSuffix(n, "/") {
			if err := tw.WriteHeader(&tar.Header{Name: n, Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		body := files[n]
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readTarGz(t *testing.T, p string) map[string]string {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeDir {
			out[strings.TrimSuffix(hdr.Name, "/")+"/"] = ""
			continue
		}
		b, _ := io.ReadAll(tr)
		out[hdr.Name] = string(b)
	}
	return out
}

func TestRebaseTarStripsMountPointPrefix(t *testing.T) {
	var in bytes.Buffer
	tw := tar.NewWriter(&in)
	for _, h := range []*tar.Header{
		{Name: "data/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "data/a.txt", Typeflag: tar.TypeReg, Size: 1, Mode: 0o644},
		{Name: "data/b", Typeflag: tar.TypeLink, Linkname: "data/a.txt"},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			_, _ = tw.Write([]byte("x"))
		}
	}
	_ = tw.Close()

	var out bytes.Buffer
	if err := rebaseTar(tar.NewReader(&in), tar.NewWriter(&out), "data"); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&out)
	var got []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, h.Name+">"+h.Linkname)
	}
	if strings.Join(got, ",") != "a.txt>,b>a.txt" {
		t.Fatalf("rebased entries = %v", got)
	}
}

func TestVolumeBackupUploadRejectsBadArchives(t *testing.T) {
	s := &Server{jobs: newVolumeJobs(t.TempDir()), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := s.jobs.load(); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"not gzip":  []byte("hello"),
		"escape":    makeTarGz(t, map[string]string{"../etc/passwd": "x"}),
		"absolute":  makeTarGz(t, map[string]string{"/etc/passwd": "x"}),
		"truncated": makeTarGz(t, map[string]string{"a": strings.Repeat("y", 4096)})[:40],
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/volume-backups/upload?volume=v", bytes.NewReader(body))
		s.handleVolumeBackupUpload(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (%s)", name, w.Code, w.Body.String())
		}
	}
	entries, _ := os.ReadDir(s.jobs.dir)
	if len(entries) != 0 {
		t.Errorf("rejected uploads left files behind: %v", entries)
	}

	good := makeTarGz(t, map[string]string{"dir/": "", "dir/f.txt": "hi"})
	w := httptest.NewRecorder()
	s.handleVolumeBackupUpload(w, httptest.NewRequest(http.MethodPost, "/v1/volume-backups/upload?volume=v", bytes.NewReader(good)))
	if w.Code != http.StatusOK || len(s.jobs.backups) != 1 {
		t.Fatalf("good upload: status %d, backups %d: %s", w.Code, len(s.jobs.backups), w.Body.String())
	}
	for id := range s.jobs.backups {
		b, _ := os.ReadFile(s.jobs.archivePath(id))
		if !bytes.Equal(b, good) {
			t.Fatal("stored archive differs from the upload")
		}
	}
}

func TestVolumeJobsLoadMarksInterruptedBackupsFailed(t *testing.T) {
	dir := t.TempDir()
	j := newVolumeJobs(dir)
	info := &BackupInfo{ID: "v-1", Volume: "v", Status: jobRunning, CreatedAt: time.Now()}
	if err := j.writeMeta(info); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "v-1.tar.gz.partial"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	j2 := newVolumeJobs(dir)
	if err := j2.load(); err != nil {
		t.Fatal(err)
	}
	if got := j2.backups["v-1"]; got == nil || got.Status != jobFailed || got.Error == "" {
		t.Fatalf("loaded backup = %+v, want failed", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "v-1.tar.gz.partial")); !os.IsNotExist(err) {
		t.Fatal("partial archive was not cleaned up")
	}
}

// TestVolumeBackupRoundTripDocker runs a real backup and restore against
// the local Docker daemon. Opt-in, since it creates (and removes) volumes:
//
//	SWARMDASH_DOCKER_TESTS=1 go test ./internal/agent/ -run Docker
func TestVolumeBackupRoundTripDocker(t *testing.T) {
	if os.Getenv("SWARMDASH_DOCKER_TESTS") == "" {
		t.Skip("set SWARMDASH_DOCKER_TESTS=1 to run against the local Docker daemon")
	}
	docker, err := dockerutil.New("")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s := &Server{docker: docker, jobs: newVolumeJobs(t.TempDir()), log: slog.New(slog.NewTextHandler(io.Discard, nil)), ctx: ctx}

	suffix := time.Now().Format("150405.000")
	src, dst := "sdtest-src-"+suffix, "sdtest-dst-"+suffix
	for _, v := range []string{src, dst} {
		v := v
		t.Cleanup(func() { _ = docker.VolumeRemove(context.Background(), v, true) })
	}
	if _, err := docker.VolumeCreate(ctx, volume.CreateOptions{Name: src}); err != nil {
		t.Fatal(err)
	}

	files := map[string]string{"top.txt": "hello", "nested/": "", "nested/deep.txt": strings.Repeat("z", 100_000)}
	seed := filepath.Join(t.TempDir(), "seed.tar.gz")
	if err := os.WriteFile(seed, makeTarGz(t, files), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.restoreVolume(ctx, seed, src); err != nil {
		t.Fatalf("seed volume: %v", err)
	}

	backup := filepath.Join(s.jobs.dir, "b.tar.gz")
	if _, err := s.backupVolume(ctx, src, backup); err != nil {
		t.Fatalf("backup: %v", err)
	}
	got := readTarGz(t, backup)
	for name, body := range files {
		if got[name] != body {
			t.Errorf("backup entry %q = %d bytes, want %d", name, len(got[name]), len(body))
		}
	}

	// Restoring into a volume that doesn't exist yet creates it.
	if err := s.restoreVolume(ctx, backup, dst); err != nil {
		t.Fatalf("restore: %v", err)
	}
	again := filepath.Join(s.jobs.dir, "c.tar.gz")
	if _, err := s.backupVolume(ctx, dst, again); err != nil {
		t.Fatalf("backup of restored volume: %v", err)
	}
	if g := readTarGz(t, again); g["nested/deep.txt"] != files["nested/deep.txt"] || g["top.txt"] != "hello" {
		t.Fatalf("restored volume contents = %v", g)
	}
}
