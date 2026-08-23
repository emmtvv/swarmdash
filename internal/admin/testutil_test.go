package admin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"

	"github.com/docker/docker/client"

	"swarmdash/internal/store"
	"swarmdash/internal/web"
)

// Shared test infrastructure for internal/admin handler tests.
//
// Handlers are exercised by calling the s.handleXxx method directly against
// an httptest.NewRecorder/Request pair, rather than through the full
// s.Handler() mux - this keeps tests focused on one handler's behavior
// instead of re-verifying the auth/CSRF/routing middleware every time (those
// have their own tests). Use withUser/withPathValue/withForm to shape the
// request the same way the real middleware chain would have.
//
// Two backends get faked:
//   - the swarm-wide Docker Engine API (s.docker, a *client.Client) via
//     newFakeDocker, which serves canned JSON off a real httptest.Server so
//     the SDK's HTTP plumbing runs unmodified;
//   - the per-node agent API (s.agentGet/agentPost, plain net/http) via
//     newFakeAgent, which points every node's advertised address at a second
//     httptest.Server.

var apiVersionPrefix = regexp.MustCompile(`^/v[0-9]+\.[0-9]+`)

// newFakeDocker starts a Docker Engine API stand-in backed by mux (register
// plain paths like "/nodes" - the real SDK's version prefix, e.g.
// "/v1.51/nodes", is stripped before dispatch) and returns a client wired to
// it.
func newFakeDocker(t *testing.T, mux *http.ServeMux) *client.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = apiVersionPrefix.ReplaceAllString(r.URL.Path, "")
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	cli, err := client.NewClientWithOpts(client.WithHost("tcp://" + mustHostPort(t, ts.URL)))
	if err != nil {
		t.Fatalf("newFakeDocker: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// newFakeAgent starts a per-node agent API stand-in backed by mux and
// returns the loopback address/port pair to use as a node's Status.Addr and
// the server's AgentPort so agentGet/agentPost/dialAgentWS reach it.
func newFakeAgent(t *testing.T, mux *http.ServeMux) (addr, port string) {
	t.Helper()
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return "127.0.0.1", mustPort(t, ts.URL)
}

func mustHostPort(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Host
}

func mustPort(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Port()
}

// jsonHandler replies with v JSON-encoded, for canned Docker/agent API
// responses that don't need to inspect the request.
func jsonHandler(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}

// newTestStore opens a throwaway sqlite-backed store.Interface, cleaned up
// automatically at the end of the test.
func newTestStore(t *testing.T) store.Interface {
	t.Helper()
	st, err := store.OpenSQLite(t.TempDir())
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// newTestServer builds a *Server suitable for calling handler methods
// directly. docker may be nil for handlers that never touch it. Callers
// needing a specific store, ClusterSecret, or AgentPort should build the
// *Server by hand instead - this covers the common case.
func newTestServer(t *testing.T, docker *client.Client) *Server {
	t.Helper()
	return &Server{
		cfg:      Config{AgentPort: "0"},
		docker:   docker,
		store:    newTestStore(t),
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		renderer: web.NewRenderer(),
	}
}

var (
	testAdmin  = &store.User{Username: "admin-user", Role: "admin"}
	testViewer = &store.User{Username: "viewer-user", Role: "viewer"}
)

// withUser attaches user to r's context the way requireAuth would have.
func withUser(r *http.Request, user *store.User) *http.Request {
	return r.WithContext(newUserContext(r, user))
}
