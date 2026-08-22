// Package admin implements the swarmdash "admin" mode: the web UI and API
// server that runs on a manager node. It talks to the swarm-wide Docker API
// directly for stacks/services/tasks/nodes, and proxies node-local
// operations (exec, logs, stats) to the per-node agent processes.
package admin

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/docker/docker/client"

	"swarmdash/internal/store"
	"swarmdash/internal/web"
)

type Config struct {
	ListenAddr    string
	DockerHost    string
	ClusterSecret string
	AgentPort     string

	BootstrapUsername string
	BootstrapPassword string

	// AgentTLS enables mTLS when dialing agents; see `swarmdash tls init`.
	// All three must be set together. When unset, admin talks to agents
	// over plain HTTP (fine on a trusted cluster network, the default).
	AgentTLSCertFile string
	AgentTLSKeyFile  string
	AgentTLSCAFile   string

	// UITLSCertFile/UITLSKeyFile enable HTTPS on the admin web UI/API
	// itself. Independent of AgentTLS above - this secures browser<->admin,
	// that secures admin<->agent.
	UITLSCertFile string
	UITLSKeyFile  string
}

type Server struct {
	cfg      Config
	docker   *client.Client
	store    store.Interface
	log      *slog.Logger
	renderer *web.Renderer
	agentTLS *tls.Config

	// memUsage caches the last cluster-wide real memory usage sampled by
	// the poller (internal/admin/poller.go), so the dashboard can render
	// instantly instead of fanning out to every node's agent on every page
	// load.
	memUsageMu    sync.RWMutex
	memUsageBytes int64
}

func New(cfg Config, docker *client.Client, st store.Interface) (*Server, error) {
	agentTLS, err := buildAgentTLSConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("agent tls config: %w", err)
	}
	return &Server{
		cfg:      cfg,
		docker:   docker,
		store:    st,
		log:      slog.New(slog.NewTextHandler(os.Stdout, nil)).With("component", "admin"),
		renderer: web.NewRenderer(),
		agentTLS: agentTLS,
	}, nil
}

// buildAgentTLSConfig loads the admin client certificate and CA pool used
// to dial agents with mTLS, or returns nil if AgentTLS* wasn't configured
// (plain HTTP to agents, the default).
func buildAgentTLSConfig(cfg Config) (*tls.Config, error) {
	if cfg.AgentTLSCertFile == "" && cfg.AgentTLSKeyFile == "" && cfg.AgentTLSCAFile == "" {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(cfg.AgentTLSCertFile, cfg.AgentTLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load admin client certificate: %w", err)
	}
	caPEM, err := os.ReadFile(cfg.AgentTLSCAFile)
	if err != nil {
		return nil, fmt.Errorf("read agent CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("no certificates found in %s", cfg.AgentTLSCAFile)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   "swarmdash-agent",
		MinVersion:   tls.VersionTLS12,
	}, nil
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	s.renderer.Render(w, name, data, csrfTokenFromContext(r), cspNonceFromContext(r))
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(web.StaticFS)))

	mux.HandleFunc("GET /status", s.handleStatusPage)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /hooks/deploy/{token}", s.handleDeployHookTrigger)

	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /login", s.handleLoginSubmit)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /sso/login", s.handleSSOLogin)
	mux.HandleFunc("GET /sso/callback", s.handleSSOCallback)

	protected := http.NewServeMux()
	protected.HandleFunc("GET /{$}", s.handleDashboard)

	protected.HandleFunc("GET /account/password", s.handleAccountPasswordPage)
	protected.HandleFunc("POST /account/password", s.handleAccountPasswordChange)

	protected.HandleFunc("GET /nodes", s.handleNodesPage)
	protected.HandleFunc("GET /nodes/stats", s.handleNodesStatsJSON)
	protected.HandleFunc("GET /nodes/{id}", s.handleNodeDetail)
	protected.HandleFunc("GET /nodes/{id}/stats", s.handleNodeStatsJSON)
	protected.HandleFunc("POST /nodes/{id}/availability", s.handleNodeAvailability)
	protected.HandleFunc("POST /nodes/{id}/promote", s.handleNodePromote)
	protected.HandleFunc("POST /nodes/{id}/demote", s.handleNodeDemote)
	protected.HandleFunc("POST /nodes/{id}/labels", s.handleNodeLabelAdd)
	protected.HandleFunc("POST /nodes/{id}/labels/{key}/delete", s.handleNodeLabelDelete)

	protected.HandleFunc("GET /stacks", s.handleStacksPage)
	protected.HandleFunc("GET /stacks/templates", s.handleTemplatesPage)
	protected.HandleFunc("GET /stacks/deploy", s.handleStackDeployPage)
	protected.HandleFunc("POST /stacks/deploy/preview", s.handleStackDeployPreview)
	protected.HandleFunc("POST /stacks/deploy", s.handleStackDeploySubmit)
	protected.HandleFunc("GET /stacks/gitops", s.handleGitOpsPage)
	protected.HandleFunc("POST /stacks/gitops", s.handleGitOpsCreate)
	protected.HandleFunc("POST /stacks/gitops/{id}/sync", s.handleGitOpsSync)
	protected.HandleFunc("POST /stacks/gitops/{id}/delete", s.handleGitOpsDelete)
	protected.HandleFunc("GET /stacks/{name}", s.handleStackDetail)
	protected.HandleFunc("GET /stacks/{name}/export.yml", s.handleStackExport)
	protected.HandleFunc("POST /stacks/{name}/restart", s.handleStackRestart)
	protected.HandleFunc("POST /stacks/{name}/delete", s.handleStackDelete)

	protected.HandleFunc("GET /services", s.handleServicesPage)
	protected.HandleFunc("GET /services/{name}", s.handleServiceDetail)
	protected.HandleFunc("GET /services/{name}/events", s.handleServiceEvents)
	protected.HandleFunc("GET /services/{name}/export.yml", s.handleServiceExport)
	protected.HandleFunc("POST /services/{name}/scale", s.handleServiceScale)
	protected.HandleFunc("POST /services/{name}/image", s.handleServiceUpdateImage)
	protected.HandleFunc("POST /services/{name}/spec", s.handleServiceUpdateSpec)
	protected.HandleFunc("POST /services/{name}/rollback", s.handleServiceRollback)
	protected.HandleFunc("POST /services/{name}/restart", s.handleServiceRestart)
	protected.HandleFunc("POST /services/{name}/update-latest", s.handleServiceUpdateLatest)
	protected.HandleFunc("POST /services/{name}/delete", s.handleServiceDelete)
	protected.HandleFunc("POST /services/bulk", s.handleServicesBulk)
	protected.HandleFunc("POST /services/{name}/deploy-hooks", s.handleDeployHookCreate)
	protected.HandleFunc("POST /services/{name}/deploy-hooks/{id}/delete", s.handleDeployHookDelete)

	protected.HandleFunc("GET /topology", s.handleTopologyPage)

	protected.HandleFunc("GET /networks", s.handleNetworksPage)
	protected.HandleFunc("POST /networks", s.handleNetworkCreate)
	protected.HandleFunc("POST /networks/{id}/delete", s.handleNetworkDelete)

	protected.HandleFunc("GET /secrets", s.handleSecretsPage)
	protected.HandleFunc("POST /secrets", s.handleSecretCreate)
	protected.HandleFunc("POST /secrets/{id}/delete", s.handleSecretDelete)

	protected.HandleFunc("GET /configs", s.handleConfigsPage)
	protected.HandleFunc("POST /configs", s.handleConfigCreate)
	protected.HandleFunc("POST /configs/{id}/delete", s.handleConfigDelete)

	protected.HandleFunc("GET /images", s.handleImagesPage)
	protected.HandleFunc("POST /images/prune", s.handleImagesPrune)

	protected.HandleFunc("GET /volumes", s.handleVolumesPage)
	protected.HandleFunc("POST /volumes", s.handleVolumeCreate)
	protected.HandleFunc("POST /volumes/{name}/delete", s.handleVolumeDelete)

	protected.HandleFunc("GET /audit", s.handleAuditPage)
	protected.HandleFunc("GET /events", s.handleEventsPage)

	protected.HandleFunc("GET /settings/webhooks", s.handleWebhooksPage)
	protected.HandleFunc("POST /settings/webhooks", s.handleWebhookCreate)
	protected.HandleFunc("POST /settings/webhooks/{id}/delete", s.handleWebhookDelete)

	protected.HandleFunc("GET /settings/tokens", s.handleTokensPage)
	protected.HandleFunc("POST /settings/tokens", s.handleTokenCreate)
	protected.HandleFunc("POST /settings/tokens/{id}/delete", s.handleTokenDelete)

	protected.HandleFunc("GET /settings/users", s.handleUsersPage)
	protected.HandleFunc("POST /settings/users", s.handleUserCreate)
	protected.HandleFunc("POST /settings/users/{username}/delete", s.handleUserDelete)
	protected.HandleFunc("POST /settings/users/{username}/password", s.handleUserResetPassword)

	protected.HandleFunc("GET /settings/swarm", s.handleSwarmPage)
	protected.HandleFunc("POST /settings/swarm/spec", s.handleSwarmUpdateSpec)
	protected.HandleFunc("POST /settings/swarm/rotate-token/{role}", s.handleSwarmRotateToken)
	protected.HandleFunc("POST /settings/swarm/rotate-ca", s.handleSwarmForceCertRotate)
	protected.HandleFunc("POST /settings/swarm/rebalance", s.handleSwarmRebalance)
	protected.HandleFunc("POST /settings/swarm/prune-failed-tasks", s.handleSwarmPruneFailedTasks)
	protected.HandleFunc("POST /settings/swarm/prune-resources", s.handleSwarmPruneResources)

	protected.HandleFunc("GET /settings/registries", s.handleRegistriesPage)
	protected.HandleFunc("POST /settings/registries", s.handleRegistryCreate)
	protected.HandleFunc("POST /settings/registries/{server}/delete", s.handleRegistryDelete)

	protected.HandleFunc("GET /settings/sso", s.handleSSOSettingsPage)
	protected.HandleFunc("POST /settings/sso", s.handleSSOSettingsSave)
	protected.HandleFunc("POST /settings/sso/disable", s.handleSSOSettingsDisable)

	protected.HandleFunc("GET /settings/backup", s.handleBackupPage)
	protected.HandleFunc("GET /settings/backup/export", s.handleBackupExport)
	protected.HandleFunc("POST /settings/backup/restore", s.handleBackupRestore)

	protected.HandleFunc("GET /exec/{taskID}", s.handleExecPage)
	protected.HandleFunc("GET /ws/exec/{taskID}", s.handleExecProxy)
	protected.HandleFunc("GET /logs/{taskID}", s.handleTaskLogsPage)
	protected.HandleFunc("GET /ws/logs/{taskID}", s.handleLogsProxy)
	protected.HandleFunc("GET /ws/stats/{taskID}", s.handleStatsProxy)
	protected.HandleFunc("GET /logs/{taskID}/download", s.handleLogsDownload)
	protected.HandleFunc("GET /files/{taskID}", s.handleFilesList)
	protected.HandleFunc("GET /files/{taskID}/download", s.handleFileDownload)

	protected.HandleFunc("GET /services/{name}/logs", s.handleServiceLogsPage)
	protected.HandleFunc("GET /ws/services/{name}/logs", s.handleServiceLogsProxy)
	protected.HandleFunc("GET /services/{name}/logs/download", s.handleServiceLogsDownload)

	mux.Handle("/", s.requireAuth(s.requireRole(protected)))

	return s.logging(s.security(mux))
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	if err := s.ensureBootstrapUser(); err != nil {
		return err
	}

	go s.runPoller(ctx)
	go s.runGitOpsPoller(ctx)

	srv := &http.Server{
		Addr:              s.cfg.ListenAddr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	useTLS := s.cfg.UITLSCertFile != "" && s.cfg.UITLSKeyFile != ""

	errCh := make(chan error, 1)
	go func() {
		s.log.Info("admin listening", "addr", s.cfg.ListenAddr, "tls", useTLS)
		if useTLS {
			errCh <- srv.ListenAndServeTLS(s.cfg.UITLSCertFile, s.cfg.UITLSKeyFile)
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

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.log.Info("request", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start))
	})
}
