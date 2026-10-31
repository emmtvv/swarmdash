package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"

	"swarmdash/internal/httpx"
)

// Volume backup and restore. Volumes are node-local (see volumes.go), so
// every backup lives on the node that holds the volume, as a .tar.gz in
// Config.BackupDir next to a small .json metadata file. Both directions
// run as background jobs - a volume can be many gigabytes, far longer
// than an HTTP request should stay open - and the admin UI polls their
// status.
//
// Reading and writing a volume needs no shell, tar binary or pulled image
// on the node: the agent imports an empty ("scratch") helper image once,
// creates - but never starts - a container from it with the volume mounted
// at /data, and moves the files with the Docker API's archive endpoints
// (CopyFromContainer / CopyToContainer, i.e. `docker cp`), which work on a
// created container and see its volume mounts.

const (
	helperImage      = "swarmdash-volume-helper:scratch"
	helperMountPoint = "/data"
	helperLabel      = "swarmdash.helper"

	// maxConcurrentVolumeJobs bounds disk and I/O pressure from backups
	// and restores on one node; further jobs wait their turn as "queued".
	maxConcurrentVolumeJobs = 2
)

type jobStatus string

const (
	jobQueued  jobStatus = "queued"
	jobRunning jobStatus = "running"
	jobDone    jobStatus = "done"
	jobFailed  jobStatus = "failed"
)

// BackupInfo is one stored (or in-progress) volume backup. It's what the
// .json sidecar holds and what the API returns.
type BackupInfo struct {
	ID         string    `json:"id"`
	Volume     string    `json:"volume"`
	Source     string    `json:"source"` // "backup" (taken here) or "upload"
	Status     jobStatus `json:"status"`
	Error      string    `json:"error,omitempty"`
	SizeBytes  int64     `json:"size_bytes"`
	CreatedAt  time.Time `json:"created_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// RestoreJob is one restore of a stored backup into a volume. Restores
// are kept in memory only: a restore interrupted by an agent restart is
// simply gone (and should be re-run), while the backup itself is durable.
type RestoreJob struct {
	ID         string    `json:"id"`
	BackupID   string    `json:"backup_id"`
	Volume     string    `json:"volume"`
	Status     jobStatus `json:"status"`
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

type volumeJobs struct {
	dir   string
	slots chan struct{}

	mu       sync.Mutex
	backups  map[string]*BackupInfo
	restores map[string]*RestoreJob
	// busy marks volumes with a job in flight, so a backup and a restore
	// of the same volume (or two restores) never interleave.
	busy map[string]bool
}

// backupIDRE is what IDs look like - checked on every ID that comes in a
// URL, since it's also a file name under dir.
var backupIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,200}$`)

func newVolumeJobs(dir string) *volumeJobs {
	return &volumeJobs{
		dir:      dir,
		slots:    make(chan struct{}, maxConcurrentVolumeJobs),
		backups:  map[string]*BackupInfo{},
		restores: map[string]*RestoreJob{},
		busy:     map[string]bool{},
	}
}

// load reads existing backups from dir. One still marked queued/running
// was interrupted by the agent stopping: its partial archive is removed and
// it's recorded as failed rather than silently dropped.
func (j *volumeJobs) load() error {
	if j.dir == "" {
		return nil
	}
	if err := os.MkdirAll(j.dir, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(j.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".partial") {
			_ = os.Remove(filepath.Join(j.dir, name))
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(j.dir, name))
		if err != nil {
			continue
		}
		var info BackupInfo
		if json.Unmarshal(b, &info) != nil || !backupIDRE.MatchString(info.ID) {
			continue
		}
		if info.Status == jobQueued || info.Status == jobRunning {
			info.Status = jobFailed
			info.Error = "interrupted: the agent stopped while this backup was running"
			info.FinishedAt = time.Now()
			_ = j.writeMeta(&info)
		}
		j.backups[info.ID] = &info
	}
	return nil
}

func (j *volumeJobs) archivePath(id string) string { return filepath.Join(j.dir, id+".tar.gz") }
func (j *volumeJobs) metaPath(id string) string    { return filepath.Join(j.dir, id+".json") }

func (j *volumeJobs) writeMeta(info *BackupInfo) error {
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	tmp := j.metaPath(info.ID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, j.metaPath(info.ID))
}

// update applies f to a backup's record under the lock and persists it.
func (j *volumeJobs) update(id string, f func(*BackupInfo)) {
	j.mu.Lock()
	info, ok := j.backups[id]
	if ok {
		f(info)
	}
	var snapshot BackupInfo
	if ok {
		snapshot = *info
	}
	j.mu.Unlock()
	if ok {
		_ = j.writeMeta(&snapshot)
	}
}

// maxRestoreHistory bounds the in-memory restore list; the oldest finished
// jobs are dropped past it.
const maxRestoreHistory = 50

func (j *volumeJobs) pruneRestoresLocked() {
	if len(j.restores) <= maxRestoreHistory {
		return
	}
	finished := make([]*RestoreJob, 0, len(j.restores))
	for _, r := range j.restores {
		if r.Status == jobDone || r.Status == jobFailed {
			finished = append(finished, r)
		}
	}
	sort.Slice(finished, func(a, b int) bool { return finished[a].CreatedAt.Before(finished[b].CreatedAt) })
	for i := 0; i < len(finished) && len(j.restores) > maxRestoreHistory; i++ {
		delete(j.restores, finished[i].ID)
	}
}

func (j *volumeJobs) updateRestore(id string, f func(*RestoreJob)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if r, ok := j.restores[id]; ok {
		f(r)
	}
}

// claim marks volume busy, failing if a job already holds it.
func (j *volumeJobs) claim(vol string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.busy[vol] {
		return false
	}
	j.busy[vol] = true
	return true
}

func (j *volumeJobs) release(vol string) {
	j.mu.Lock()
	delete(j.busy, vol)
	j.mu.Unlock()
}

var unsafeIDCharsRE = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

func newJobID(volume string) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	safe := unsafeIDCharsRE.ReplaceAllString(volume, "_")
	if len(safe) > 100 {
		safe = safe[:100]
	}
	return fmt.Sprintf("%s-%s-%s", safe, time.Now().UTC().Format("20060102-150405"), hex.EncodeToString(b))
}

func (s *Server) requireBackupDir(w http.ResponseWriter) bool {
	if s.jobs.dir == "" {
		http.Error(w, "volume backups are disabled on this node: the agent was started without --backup-dir", http.StatusServiceUnavailable)
		return false
	}
	return true
}

// handleVolumeBackups lists this node's backups (newest first) and recent
// restore jobs.
func (s *Server) handleVolumeBackups(w http.ResponseWriter, r *http.Request) {
	s.jobs.mu.Lock()
	backups := make([]BackupInfo, 0, len(s.jobs.backups))
	for _, b := range s.jobs.backups {
		backups = append(backups, *b)
	}
	restores := make([]RestoreJob, 0, len(s.jobs.restores))
	for _, rj := range s.jobs.restores {
		restores = append(restores, *rj)
	}
	s.jobs.mu.Unlock()
	sort.Slice(backups, func(a, b int) bool { return backups[a].CreatedAt.After(backups[b].CreatedAt) })
	sort.Slice(restores, func(a, b int) bool { return restores[a].CreatedAt.After(restores[b].CreatedAt) })
	httpx.WriteJSON(w, map[string]any{
		"enabled":  s.jobs.dir != "",
		"dir":      s.jobs.dir,
		"backups":  backups,
		"restores": restores,
	})
}

// handleVolumeBackupStart queues a backup of a volume and returns at once.
func (s *Server) handleVolumeBackupStart(w http.ResponseWriter, r *http.Request) {
	if !s.requireBackupDir(w) {
		return
	}
	vol := r.PathValue("name")
	if _, err := s.docker.VolumeInspect(r.Context(), vol); err != nil {
		http.Error(w, "volume: "+err.Error(), http.StatusNotFound)
		return
	}
	if !s.jobs.claim(vol) {
		http.Error(w, "another backup or restore of this volume is already running", http.StatusConflict)
		return
	}
	info := &BackupInfo{ID: newJobID(vol), Volume: vol, Source: "backup", Status: jobQueued, CreatedAt: time.Now()}
	s.jobs.mu.Lock()
	s.jobs.backups[info.ID] = info
	s.jobs.mu.Unlock()
	if err := s.jobs.writeMeta(info); err != nil {
		s.jobs.release(vol)
		s.jobs.mu.Lock()
		delete(s.jobs.backups, info.ID)
		s.jobs.mu.Unlock()
		http.Error(w, "write backup metadata: "+err.Error(), http.StatusInternalServerError)
		return
	}

	go func() {
		defer s.jobs.release(vol)
		s.jobs.slots <- struct{}{}
		defer func() { <-s.jobs.slots }()
		s.jobs.update(info.ID, func(b *BackupInfo) { b.Status = jobRunning })

		size, err := s.backupVolume(s.ctx, vol, s.jobs.archivePath(info.ID))
		s.jobs.update(info.ID, func(b *BackupInfo) {
			b.FinishedAt = time.Now()
			if err != nil {
				b.Status, b.Error = jobFailed, err.Error()
				return
			}
			b.Status, b.SizeBytes = jobDone, size
		})
		if err != nil {
			s.log.Warn("volume backup failed", "volume", vol, "backup", info.ID, "err", err)
		} else {
			s.log.Info("volume backup done", "volume", vol, "backup", info.ID, "bytes", size)
		}
	}()

	httpx.WriteJSON(w, info)
}

// backupVolume writes vol's contents to dst as a gzipped tar whose paths
// are relative to the volume root, returning the archive's size.
func (s *Server) backupVolume(ctx context.Context, vol, dst string) (int64, error) {
	cid, cleanup, err := s.helperContainer(ctx, vol, true)
	if err != nil {
		return 0, err
	}
	defer cleanup()

	rc, _, err := s.docker.CopyFromContainer(ctx, cid, helperMountPoint)
	if err != nil {
		return 0, fmt.Errorf("read volume: %w", err)
	}
	defer rc.Close()

	partial := dst + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, fmt.Errorf("create archive: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(partial)
		}
	}()

	gz := gzip.NewWriter(f)
	if err := rebaseTar(tar.NewReader(rc), tar.NewWriter(gz), path.Base(helperMountPoint)); err != nil {
		return 0, err
	}
	if err := gz.Close(); err != nil {
		return 0, fmt.Errorf("write archive: %w", err)
	}
	if err := f.Sync(); err != nil {
		return 0, fmt.Errorf("write archive: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, fmt.Errorf("write archive: %w", err)
	}
	if err := os.Rename(partial, dst); err != nil {
		return 0, fmt.Errorf("finish archive: %w", err)
	}
	ok = true
	return st.Size(), nil
}

// rebaseTar copies a tar stream, stripping the leading "<prefix>/"
// directory Docker's archive API puts in front of every path (the mount
// point's own name), so the backup's root is the volume's root - the same
// layout `tar -C /volume -czf ... .` would produce, which keeps archives
// interchangeable with hand-made ones on restore.
func rebaseTar(tr *tar.Reader, tw *tar.Writer, prefix string) error {
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read volume: %w", err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if name == prefix || name == prefix+"/" {
			continue
		}
		if !strings.HasPrefix(name, prefix+"/") {
			return fmt.Errorf("unexpected path %q in volume archive", hdr.Name)
		}
		hdr.Name = strings.TrimPrefix(name, prefix+"/")
		if hdr.Typeflag == tar.TypeLink {
			hdr.Linkname = strings.TrimPrefix(strings.TrimPrefix(hdr.Linkname, "./"), prefix+"/")
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write archive: %w", err)
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return fmt.Errorf("copy %s: %w", hdr.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}
	return nil
}

// handleVolumeBackupUpload stores an uploaded .tar.gz as a backup of
// ?volume=, so it can be restored here - how a backup taken on one node
// gets to another. The archive is checked to be a readable gzipped tar as
// it streams in, so a corrupt upload fails now instead of at restore time.
func (s *Server) handleVolumeBackupUpload(w http.ResponseWriter, r *http.Request) {
	if !s.requireBackupDir(w) {
		return
	}
	vol := r.URL.Query().Get("volume")
	if vol == "" {
		http.Error(w, "volume is required", http.StatusBadRequest)
		return
	}
	info := &BackupInfo{ID: newJobID(vol), Volume: vol, Source: "upload", Status: jobRunning, CreatedAt: time.Now()}
	dst := s.jobs.archivePath(info.ID)
	partial := dst + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		http.Error(w, "create archive: "+err.Error(), http.StatusInternalServerError)
		return
	}
	fail := func(status int, msg string) {
		_ = f.Close()
		_ = os.Remove(partial)
		http.Error(w, msg, status)
	}

	// Tee the upload into the file while a gzip+tar reader walks the same
	// bytes, validating the archive without a second pass over it.
	gz, err := gzip.NewReader(io.TeeReader(r.Body, f))
	if err != nil {
		fail(http.StatusBadRequest, "not a gzip file: "+err.Error())
		return
	}
	tr := tar.NewReader(gz)
	entries := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fail(http.StatusBadRequest, "not a valid .tar.gz archive: "+err.Error())
			return
		}
		if path.IsAbs(hdr.Name) || strings.Contains("/"+hdr.Name+"/", "/../") {
			fail(http.StatusBadRequest, fmt.Sprintf("archive entry %q points outside the volume", hdr.Name))
			return
		}
		entries++
	}
	// Drain anything after the tar's end-of-archive marker so the stored
	// file is byte-for-byte what was uploaded.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		fail(http.StatusBadRequest, "not a valid .tar.gz archive: "+err.Error())
		return
	}
	if _, err := io.Copy(f, r.Body); err != nil {
		fail(http.StatusBadRequest, "read upload: "+err.Error())
		return
	}
	st, err := f.Stat()
	if err == nil {
		err = f.Close()
	}
	if err == nil {
		err = os.Rename(partial, dst)
	}
	if err != nil {
		fail(http.StatusInternalServerError, "store archive: "+err.Error())
		return
	}

	info.Status, info.SizeBytes, info.FinishedAt = jobDone, st.Size(), time.Now()
	if err := s.jobs.writeMeta(info); err != nil {
		_ = os.Remove(dst)
		http.Error(w, "write backup metadata: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.jobs.mu.Lock()
	s.jobs.backups[info.ID] = info
	s.jobs.mu.Unlock()
	s.log.Info("volume backup uploaded", "volume", vol, "backup", info.ID, "bytes", info.SizeBytes, "entries", entries)
	httpx.WriteJSON(w, info)
}

func (s *Server) lookupBackup(w http.ResponseWriter, r *http.Request) (BackupInfo, bool) {
	id := r.PathValue("id")
	if !backupIDRE.MatchString(id) {
		http.Error(w, "bad backup id", http.StatusBadRequest)
		return BackupInfo{}, false
	}
	s.jobs.mu.Lock()
	b, ok := s.jobs.backups[id]
	var info BackupInfo
	if ok {
		info = *b
	}
	s.jobs.mu.Unlock()
	if !ok {
		http.Error(w, "backup not found", http.StatusNotFound)
		return BackupInfo{}, false
	}
	return info, true
}

func (s *Server) handleVolumeBackupDownload(w http.ResponseWriter, r *http.Request) {
	info, ok := s.lookupBackup(w, r)
	if !ok {
		return
	}
	if info.Status != jobDone {
		http.Error(w, "backup is not finished", http.StatusConflict)
		return
	}
	f, err := os.Open(s.jobs.archivePath(info.ID))
	if err != nil {
		http.Error(w, "open archive: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", "application/gzip")
	httpx.SetAttachment(w, info.ID+".tar.gz")
	http.ServeContent(w, r, info.ID+".tar.gz", info.FinishedAt, f)
}

func (s *Server) handleVolumeBackupDelete(w http.ResponseWriter, r *http.Request) {
	info, ok := s.lookupBackup(w, r)
	if !ok {
		return
	}
	if info.Status == jobQueued || info.Status == jobRunning {
		http.Error(w, "backup is still running", http.StatusConflict)
		return
	}
	s.jobs.mu.Lock()
	for _, rj := range s.jobs.restores {
		if rj.BackupID == info.ID && (rj.Status == jobQueued || rj.Status == jobRunning) {
			s.jobs.mu.Unlock()
			http.Error(w, "a restore from this backup is still running", http.StatusConflict)
			return
		}
	}
	delete(s.jobs.backups, info.ID)
	s.jobs.mu.Unlock()
	for _, p := range []string{s.jobs.archivePath(info.ID), s.jobs.metaPath(info.ID)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			http.Error(w, "delete: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// handleVolumeBackupRestore queues restoring a backup into a volume (the
// backup's own volume unless another is named; created if missing).
// Files in the archive overwrite same-named files in the volume; files
// only in the volume are left alone - restore into a new, empty volume
// for an exact copy.
func (s *Server) handleVolumeBackupRestore(w http.ResponseWriter, r *http.Request) {
	info, ok := s.lookupBackup(w, r)
	if !ok {
		return
	}
	if info.Status != jobDone {
		http.Error(w, "backup is not finished", http.StatusConflict)
		return
	}
	var req struct {
		Volume string `json:"volume"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	vol := strings.TrimSpace(req.Volume)
	if vol == "" {
		vol = info.Volume
	}
	if !s.jobs.claim(vol) {
		http.Error(w, "another backup or restore of this volume is already running", http.StatusConflict)
		return
	}
	job := &RestoreJob{ID: newJobID(vol), BackupID: info.ID, Volume: vol, Status: jobQueued, CreatedAt: time.Now()}
	s.jobs.mu.Lock()
	s.jobs.restores[job.ID] = job
	s.jobs.pruneRestoresLocked()
	s.jobs.mu.Unlock()

	go func() {
		defer s.jobs.release(vol)
		s.jobs.slots <- struct{}{}
		defer func() { <-s.jobs.slots }()
		s.jobs.updateRestore(job.ID, func(j *RestoreJob) { j.Status = jobRunning })
		err := s.restoreVolume(s.ctx, s.jobs.archivePath(info.ID), vol)
		s.jobs.updateRestore(job.ID, func(j *RestoreJob) {
			j.FinishedAt = time.Now()
			if err != nil {
				j.Status, j.Error = jobFailed, err.Error()
			} else {
				j.Status = jobDone
			}
		})
		if err != nil {
			s.log.Warn("volume restore failed", "volume", vol, "backup", info.ID, "err", err)
		} else {
			s.log.Info("volume restore done", "volume", vol, "backup", info.ID)
		}
	}()

	httpx.WriteJSON(w, job)
}

func (s *Server) restoreVolume(ctx context.Context, archive, vol string) error {
	if _, err := s.docker.VolumeInspect(ctx, vol); err != nil {
		if !cerrdefs.IsNotFound(err) {
			return fmt.Errorf("inspect volume: %w", err)
		}
		if _, err := s.docker.VolumeCreate(ctx, volume.CreateOptions{Name: vol}); err != nil {
			return fmt.Errorf("create volume: %w", err)
		}
	}
	f, err := os.Open(archive)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("read archive: %w", err)
	}

	cid, cleanup, err := s.helperContainer(ctx, vol, false)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := s.docker.CopyToContainer(ctx, cid, helperMountPoint, gz, container.CopyToContainerOptions{AllowOverwriteDirWithFile: true}); err != nil {
		return fmt.Errorf("write volume: %w", err)
	}
	return nil
}

// helperContainer creates (without starting) a container with vol mounted
// at helperMountPoint, returning its ID and a cleanup func that removes it.
func (s *Server) helperContainer(ctx context.Context, vol string, readOnly bool) (string, func(), error) {
	if err := s.ensureHelperImage(ctx); err != nil {
		return "", nil, err
	}
	resp, err := s.docker.ContainerCreate(ctx,
		&container.Config{
			Image:  helperImage,
			Cmd:    []string{"/never-started"},
			Labels: map[string]string{helperLabel: "volume-backup"},
		},
		&container.HostConfig{
			Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: vol, Target: helperMountPoint, ReadOnly: readOnly}},
		},
		nil, nil, "")
	if err != nil {
		return "", nil, fmt.Errorf("create helper container: %w", err)
	}
	cleanup := func() {
		// Background context: the job's own context may be what got
		// cancelled, and the container must not be left behind either way.
		rmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.docker.ContainerRemove(rmCtx, resp.ID, container.RemoveOptions{Force: true}); err != nil {
			s.log.Warn("remove volume helper container", "id", resp.ID, "err", err)
		}
	}
	return resp.ID, cleanup, nil
}

// ensureHelperImage imports the empty helper image if this node's daemon
// doesn't have it yet - an image built from an empty tar, so there's
// nothing to pull and nothing that could ever run.
func (s *Server) ensureHelperImage(ctx context.Context) error {
	if _, err := s.docker.ImageInspect(ctx, helperImage); err == nil {
		return nil
	}
	var empty bytes.Buffer
	if err := tar.NewWriter(&empty).Close(); err != nil {
		return err
	}
	rc, err := s.docker.ImageImport(ctx, image.ImportSource{Source: &empty, SourceName: "-"}, helperImage, image.ImportOptions{})
	if err != nil {
		return fmt.Errorf("import helper image: %w", err)
	}
	defer rc.Close()
	_, _ = io.Copy(io.Discard, rc)
	if _, err := s.docker.ImageInspect(ctx, helperImage); err != nil {
		return fmt.Errorf("import helper image: %w", err)
	}
	return nil
}
