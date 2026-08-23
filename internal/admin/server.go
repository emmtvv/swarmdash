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
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/docker/docker/client"
	"golang.org/x/time/rate"

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

	// TrustedProxies lists the IPs/CIDRs of reverse proxies (e.g. a
	// Traefik/nginx sitting in front of admin) allowed to set
	// X-Forwarded-For/X-Forwarded-Proto. Empty (the default) means neither
	// header is trusted: clientIP falls back to RemoteAddr and
	// isSecureRequest falls back to r.TLS - safe, but means every proxied
	// request looks like it came from the proxy's own address (see
	// clientIP in auth.go). Set this to the proxy's address when running
	// behind one, so per-IP rate limiting, login lockout, and the Secure
	// cookie flag see the real client again.
	TrustedProxies []string
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

	// rbacCache/rbacOnce memoize the route -> role-requirement lookup built
	// from protectedRoutes (see routes.go and rbacTable in auth.go).
	rbacOnce  sync.Once
	rbacCache *rbac

	// loginLimiter/globalLimiter throttle by client IP (see ratelimit.go).
	// loginLimiter is deliberately tight - it backstops the (username, IP)
	// account lockout in auth.go against an attacker spraying many
	// different usernames from one address, which the per-account lockout
	// alone wouldn't catch. globalLimiter is a loose backstop against
	// naive flooding across the rest of the app.
	loginLimiter  *ipRateLimiter
	globalLimiter *ipRateLimiter

	// trustedProxies backs clientIP (auth.go) and isSecureRequest
	// (security.go); see Config.TrustedProxies. Empty unless
	// --trusted-proxies is set.
	trustedProxies []*net.IPNet

	// execSlots caps concurrent interactive /ws/exec sessions (see
	// handleExecProxy in wsproxy.go): a buffered channel used purely as a
	// counting semaphore, sized maxConcurrentExecSessions.
	execSlots chan struct{}

	// updateCheckDisabled caches store.AppSettings.UpdateCheckDisabled -
	// see isUpdateCheckDisabled/setUpdateCheckDisabled in
	// handlers_settings_general.go - so the security() middleware (which
	// wraps every request) and the dashboard don't each hit the store on
	// every request for a value that only changes when an admin flips it
	// in Settings -> General.
	updateCheckLoaded   sync.Once
	updateCheckDisabled atomic.Bool
}

func New(cfg Config, docker *client.Client, st store.Interface) (*Server, error) {
	agentTLS, err := buildAgentTLSConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("agent tls config: %w", err)
	}
	trustedProxies, err := parseTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		return nil, fmt.Errorf("trusted proxies: %w", err)
	}
	s := &Server{
		cfg:            cfg,
		docker:         docker,
		store:          st,
		log:            slog.New(slog.NewTextHandler(os.Stdout, nil)).With("component", "admin"),
		renderer:       web.NewRenderer(),
		agentTLS:       agentTLS,
		trustedProxies: trustedProxies,
		execSlots:      make(chan struct{}, maxConcurrentExecSessions),
	}
	// 5 attempts/minute/IP, burst 5: generous enough for a real user
	// mistyping a password, tight enough to make spraying many usernames
	// from one address slow going.
	s.loginLimiter = newIPRateLimiter(rate.Every(12*time.Second), 5, s.clientIP)
	// 20 req/s/IP, burst 40: a loose backstop against naive flooding, not
	// meant to shape legitimate dashboard/SSE traffic.
	s.globalLimiter = newIPRateLimiter(rate.Limit(20), 40, s.clientIP)
	return s, nil
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

	mux.HandleFunc("GET /status", s.handleStatusPage)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /hooks/deploy/{token}", s.handleDeployHookTrigger)

	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.Handle("POST /login", s.loginLimiter.middleware(http.HandlerFunc(s.handleLoginSubmit)))
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /sso/login", s.handleSSOLogin)
	mux.HandleFunc("GET /sso/callback", s.handleSSOCallback)

	protected := http.NewServeMux()
	for _, rt := range protectedRoutes(s) {
		protected.HandleFunc(rt.Pattern, rt.Handler)
	}

	mux.Handle("/", s.requireAuth(s.requireRole(protected)))

	// /static/ sits outside globalLimiter: a single page load pulls in
	// layout/app CSS, app.js, htmx, xterm, xterm-addon-fit, and the
	// favicon, so a couple of concurrent dashboard sessions (each also
	// holding an SSE stream and polling) can burn through the limiter's
	// burst on static assets alone before a single dynamic request is
	// served. It still gets security()'s response headers and logging(),
	// just not IP throttling meant for the dynamic app.
	top := http.NewServeMux()
	top.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(web.StaticFS)))
	top.Handle("/", s.globalLimiter.middleware(mux))

	return s.logging(s.security(top))
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
