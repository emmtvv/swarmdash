package agent

import (
	"archive/tar"
	"io"
	"net/http"
	"path"
	"strings"

	"swarmdash/internal/httpx"
)

type fileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
	Mode  string `json:"mode"`
}

// handleListFiles lists the immediate children of a directory inside a
// container by asking Docker to copy that path out as a tar stream (the
// only way to browse a container's filesystem without an exec session) and
// keeping just the entries one level deep.
func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dir := r.URL.Query().Get("path")
	if dir == "" {
		dir = "/"
	}
	dir = path.Clean(dir)

	rc, _, err := s.docker.CopyFromContainer(r.Context(), id, dir)
	if err != nil {
		http.Error(w, "copy from container: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer rc.Close()

	tr := tar.NewReader(rc)
	var entries []fileEntry
	rootPrefix := ""
	first := true
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "read archive: "+err.Error(), http.StatusBadGateway)
			return
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		if first {
			rootPrefix = name
			first = false
			continue
		}
		rel := strings.TrimPrefix(name, rootPrefix+"/")
		if rel == name || rel == "" || strings.Contains(rel, "/") {
			continue
		}
		entries = append(entries, fileEntry{
			Name:  rel,
			Path:  path.Join(dir, rel),
			IsDir: hdr.Typeflag == tar.TypeDir,
			Size:  hdr.Size,
			Mode:  hdr.FileInfo().Mode().String(),
		})
	}
	httpx.WriteJSON(w, entries)
}

// handleDownloadFile streams a single file out of a container.
func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}

	rc, _, err := s.docker.CopyFromContainer(r.Context(), id, filePath)
	if err != nil {
		http.Error(w, "copy from container: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer rc.Close()

	tr := tar.NewReader(rc)
	hdr, err := tr.Next()
	if err != nil {
		http.Error(w, "empty archive: "+err.Error(), http.StatusBadGateway)
		return
	}
	if hdr.Typeflag == tar.TypeDir {
		http.Error(w, "path is a directory", http.StatusBadRequest)
		return
	}
	httpx.SetAttachment(w, path.Base(hdr.Name))
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = io.Copy(w, tr)
}
