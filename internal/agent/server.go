// Package agent implements the swarmdash "agent" mode: a small per-node
// daemon that exposes node-local Docker operations (exec, logs, stats) over
// HTTP to the admin process. It is deliberately dumb: all cluster-wide
// decisions (which node to talk to, auth of end users, etc.) live in admin.
package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/docker/docker/client"

	"swarmdash/internal/httpx"
)

type Config struct {
	ListenAddr    string
	DockerHost    string
	ClusterSecret string

	// TLS is optional mTLS hardening on top of the cluster-secret bearer
	// auth above; see `swarmdash tls init`. When unset, the agent serves
	// plain HTTP (fine on a trusted cluster network, the default).
	TLSCertFile string
	TLSKeyFile  string
	TLSCAFile   string
}

type Server struct {
	cfg    Config
	docker *client.Client
	log    *slog.Logger
}

func New(cfg Config, docker *client.Client) *Server {
	return &Server{
		cfg:    cfg,
		docker: docker,
		log:    slog.New(slog.NewTextHandler(os.Stdout, nil)).With("component", "agent"),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.Handle("GET /v1/info", s.auth(http.HandlerFunc(s.handleInfo)))
	mux.Handle("GET /v1/containers/{id}/exec", s.auth(http.HandlerFunc(s.handleExec)))
	mux.Handle("GET /v1/containers/{id}/logs", s.auth(http.HandlerFunc(s.handleLogs)))
	mux.Handle("GET /v1/containers/{id}/logs/download", s.auth(http.HandlerFunc(s.handleLogsDownload)))
	mux.Handle("GET /v1/images", s.auth(http.HandlerFunc(s.handleImages)))
	mux.Handle("POST /v1/images/prune", s.auth(http.HandlerFunc(s.handleImagesPrune)))
	mux.Handle("GET /v1/volumes", s.auth(http.HandlerFunc(s.handleVolumes)))
	mux.Handle("POST /v1/volumes", s.auth(http.HandlerFunc(s.handleVolumeCreate)))
	mux.Handle("POST /v1/volumes/{name}/delete", s.auth(http.HandlerFunc(s.handleVolumeDelete)))
	mux.Handle("POST /v1/system/prune", s.auth(http.HandlerFunc(s.handleSystemPrune)))
	mux.Handle("POST /v1/containers/{id}/remove", s.auth(http.HandlerFunc(s.handleContainerRemove)))
	mux.Handle("GET /v1/containers/{id}/files", s.auth(http.HandlerFunc(s.handleListFiles)))
	mux.Handle("GET /v1/containers/{id}/files/download", s.auth(http.HandlerFunc(s.handleDownloadFile)))
	mux.Handle("GET /v1/containers/{id}/stats", s.auth(http.HandlerFunc(s.handleStats)))
	mux.Handle("GET /v1/stats/summary", s.auth(http.HandlerFunc(s.handleStatsSummary)))

	return s.logging(mux)
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.ListenAddr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	useTLS := s.cfg.TLSCertFile != "" && s.cfg.TLSKeyFile != "" && s.cfg.TLSCAFile != ""
	if useTLS {
		caPEM, err := os.ReadFile(s.cfg.TLSCAFile)
		if err != nil {
			return fmt.Errorf("read tls-ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return fmt.Errorf("no certificates found in %s", s.cfg.TLSCAFile)
		}
		srv.TLSConfig = &tls.Config{
			ClientAuth: tls.RequireAndVerifyClientCert,
			ClientCAs:  pool,
			MinVersion: tls.VersionTLS12,
		}
	}

	errCh := make(chan error, 1)
	go func() {
		s.log.Info("agent listening", "addr", s.cfg.ListenAddr, "tls", useTLS)
		if useTLS {
			errCh <- srv.ListenAndServeTLS(s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
		} else {
			errCh <- srv.ListenAndServe()
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if s.cfg.ClusterSecret == "" || token != s.cfg.ClusterSecret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.log.Debug("request", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start))
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && h[:len(prefix)] == prefix {
		return h[len(prefix):]
	}
	return ""
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	info, err := s.docker.Info(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	httpx.WriteJSON(w, map[string]any{
		"name":            info.Name,
		"id":              info.ID,
		"server_version":  info.ServerVersion,
		"n_containers":    info.Containers,
		"n_containers_up": info.ContainersRunning,
	})
}
