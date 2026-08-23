package store

import (
	"errors"
	"time"
)

var ErrNotFound = errors.New("not found")

type User struct {
	Username     string    `bson:"_id" json:"username"`
	PasswordHash string    `bson:"password_hash" json:"password_hash"`
	Role         string    `bson:"role" json:"role"` // "admin" or "viewer"
	CreatedAt    time.Time `bson:"created_at" json:"created_at"`
	// AuthSource is "local" (password login, the default/zero value for
	// users created before SSO existed) or "sso" (provisioned via an OIDC
	// login; PasswordHash is an unusable placeholder for these, so local
	// password login is never possible for them).
	AuthSource string `bson:"auth_source,omitempty" json:"auth_source,omitempty"`
	// MustChangePassword forces a redirect to /account/password on every
	// request until the user sets their own password - set on the
	// bootstrap admin account (its password came from an operator-chosen
	// env var or a generated one printed to logs, either way not really
	// "theirs") and on any account another admin resets the password of.
	MustChangePassword bool `bson:"must_change_password,omitempty" json:"must_change_password,omitempty"`
}

// LoginAttempt tracks recent failed local-password logins for one (username,
// client IP) pair, backing the lockout in handleLoginSubmit
// (internal/admin/auth.go). Key is that pair joined by loginAttemptKey, not
// username alone: a username-only key means one attacker anywhere can lock
// a known account (e.g. "admin") out from under its real owner with a
// handful of bad requests, which is worse than the credential-stuffing
// resistance a lockout buys - keying on the pair instead means a bad
// actor can only lock out their own (username, IP) combination, not
// everyone else guessing that username from a different address. This
// stays correct under Mongo HA the same way username-only did, since every
// replica still shares the same deployment. A TTL index on LastFailure
// prunes rows a day after the last failed attempt.
type LoginAttempt struct {
	Key         string    `bson:"_id" json:"key"`
	Username    string    `bson:"username" json:"username"`
	IP          string    `bson:"ip" json:"ip"`
	FailCount   int       `bson:"fail_count" json:"fail_count"`
	LastFailure time.Time `bson:"last_failure" json:"last_failure"`
	LockedUntil time.Time `bson:"locked_until,omitempty" json:"locked_until,omitempty"`
}

type Session struct {
	Token     string    `bson:"_id" json:"token"`
	Username  string    `bson:"username" json:"username"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	ExpiresAt time.Time `bson:"expires_at" json:"expires_at"`
}

type AuditEntry struct {
	ID       uint64    `bson:"seq" json:"id"`
	Time     time.Time `bson:"ts" json:"time"`
	Username string    `bson:"username" json:"username"`
	Action   string    `bson:"action" json:"action"`
	Target   string    `bson:"target" json:"target"`
	Detail   string    `bson:"detail,omitempty" json:"detail,omitempty"`
	Success  bool      `bson:"success" json:"success"`
	Error    string    `bson:"error,omitempty" json:"error,omitempty"`
}

// APIToken lets CI/CD or scripts call the admin API without a browser
// session. Only the SHA-256 hash of the token is stored; the plaintext
// token is shown to the user exactly once, at creation time.
type APIToken struct {
	ID         string    `bson:"_id" json:"id"`
	Name       string    `bson:"name" json:"name"`
	Hash       string    `bson:"hash" json:"hash"`
	Role       string    `bson:"role,omitempty" json:"role,omitempty"` // "admin" or "viewer"; "" on tokens issued before roles existed, treated as "admin"
	CreatedBy  string    `bson:"created_by" json:"created_by"`
	CreatedAt  time.Time `bson:"created_at" json:"created_at"`
	LastUsedAt time.Time `bson:"last_used_at,omitempty" json:"last_used_at,omitempty"`
}

// RegistryCredential holds login details for a private image registry.
// PasswordEnc is opaque ciphertext as far as this package is concerned -
// the admin package encrypts/decrypts it (keyed off the cluster secret)
// before it ever reaches here, so a stolen database alone isn't enough to
// recover registry passwords.
type RegistryCredential struct {
	Server      string    `bson:"_id" json:"server"`
	Username    string    `bson:"username" json:"username"`
	PasswordEnc []byte    `bson:"password_enc" json:"password_enc"`
	CreatedAt   time.Time `bson:"created_at" json:"created_at"`
}

// TaskEvent records a task state transition observed by admin's background
// poller (Docker doesn't push these, so admin polls and diffs) - kept
// around so "what happened at 3am" has an answer after the live SSE view
// has moved on.
type TaskEvent struct {
	ID          uint64    `bson:"seq" json:"id"`
	Time        time.Time `bson:"ts" json:"time"`
	ServiceName string    `bson:"service_name" json:"service_name"`
	TaskID      string    `bson:"task_id" json:"task_id"`
	Node        string    `bson:"node" json:"node"`
	State       string    `bson:"state" json:"state"`
	Message     string    `bson:"message,omitempty" json:"message,omitempty"`
}

// Webhook receives a JSON POST whenever the background poller notices a
// service degrade past the threshold, recover, or a node go down/come
// back. See internal/admin/poller.go for the events it actually fires.
type Webhook struct {
	ID        string    `bson:"_id" json:"id"`
	Name      string    `bson:"name" json:"name"`
	URL       string    `bson:"url" json:"url"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}

// GitStack is a stack whose compose file lives in a git repo instead of
// being pasted by hand (Stacks -> Deploy stack). AuthTokenEnc is opaque
// ciphertext here, same convention as RegistryCredential.PasswordEnc - the
// admin package encrypts/decrypts it (keyed off the cluster secret).
type GitStack struct {
	ID             string    `bson:"_id" json:"id"`
	StackName      string    `bson:"stack_name" json:"stack_name"`
	RepoURL        string    `bson:"repo_url" json:"repo_url"`
	Ref            string    `bson:"ref" json:"ref"`
	ComposePath    string    `bson:"compose_path" json:"compose_path"`
	AuthTokenEnc   []byte    `bson:"auth_token_enc,omitempty" json:"auth_token_enc,omitempty"`
	PollSeconds    int       `bson:"poll_seconds" json:"poll_seconds"`
	LastCommit     string    `bson:"last_commit,omitempty" json:"last_commit,omitempty"`
	LastDeployedAt time.Time `bson:"last_deployed_at,omitempty" json:"last_deployed_at,omitempty"`
	LastError      string    `bson:"last_error,omitempty" json:"last_error,omitempty"`
	CreatedBy      string    `bson:"created_by" json:"created_by"`
	CreatedAt      time.Time `bson:"created_at" json:"created_at"`
}

// DeployHook lets a CI pipeline trigger a service redeploy over HTTP without
// a session/API token. Only the SHA-256 hash of the token is stored; the
// plaintext token (embedded in the webhook URL) is shown to the user exactly
// once, at creation time - same convention as APIToken.
type DeployHook struct {
	ID          string    `bson:"_id" json:"id"`
	Hash        string    `bson:"hash" json:"hash"`
	ServiceName string    `bson:"service_name" json:"service_name"`
	CreatedBy   string    `bson:"created_by" json:"created_by"`
	CreatedAt   time.Time `bson:"created_at" json:"created_at"`
	LastUsedAt  time.Time `bson:"last_used_at,omitempty" json:"last_used_at,omitempty"`
}

// SSOConfig is the single (singleton, _id "sso") document holding this
// cluster's OIDC single sign-on settings, configured from Settings -> SSO.
// ClientSecretEnc follows the same opaque-ciphertext convention as
// RegistryCredential.PasswordEnc / GitStack.AuthTokenEnc - encrypted at rest
// keyed off the cluster secret, decrypted by the admin package only when
// actually dialing the identity provider.
type SSOConfig struct {
	ID              string `bson:"_id" json:"id"`
	Enabled         bool   `bson:"enabled" json:"enabled"`
	Label           string `bson:"label" json:"label"` // shown on the login button, e.g. "Okta"
	Issuer          string `bson:"issuer" json:"issuer"`
	ClientID        string `bson:"client_id" json:"client_id"`
	ClientSecretEnc []byte `bson:"client_secret_enc,omitempty" json:"client_secret_enc,omitempty"`
	Scopes          string `bson:"scopes" json:"scopes"` // space-separated, e.g. "openid profile email"
	AutoCreateUsers bool   `bson:"auto_create_users" json:"auto_create_users"`
	// EnforceSSO, when set alongside Enabled, turns off local
	// username/password login entirely (both the form on /login and
	// POST /login itself reject it) - only the "Sign in with ..." button
	// works. A misconfigured/unreachable identity provider then locks
	// every local account out; see the warning on Settings -> SSO and
	// the README's Known gaps for the recovery path (flip it back off
	// directly in the sso_config collection).
	EnforceSSO      bool      `bson:"enforce_sso" json:"enforce_sso"`
	DefaultRole     string    `bson:"default_role" json:"default_role"`                               // role assigned to auto-created users
	AllowedDomains  string    `bson:"allowed_domains,omitempty" json:"allowed_domains,omitempty"`     // comma-separated email domains; empty = no restriction
	RedirectBaseURL string    `bson:"redirect_base_url,omitempty" json:"redirect_base_url,omitempty"` // e.g. "https://swarmdash.example.com"; derived from the request when empty
	UpdatedAt       time.Time `bson:"updated_at" json:"updated_at"`
}

// SSOConfigID is the fixed document ID for the singleton SSOConfig.
const SSOConfigID = "sso"

// AppSettings is a singleton (_id/id AppSettingsID) document holding
// instance-wide settings that aren't tied to any one feature area.
type AppSettings struct {
	ID string `bson:"_id" json:"id"`
	// UpdateCheckDisabled turns off the dashboard's update-available
	// banner (see internal/web/static/app.js), which otherwise has the
	// browser fetch api.github.com directly on every dashboard load - off
	// by default so that request only ever happens for operators who
	// haven't opted out, not because the panel is closed-network by
	// default. Read once and cached (see Server.isUpdateCheckDisabled in
	// internal/admin/handlers_settings_general.go) rather than looked up
	// on every request.
	UpdateCheckDisabled bool `bson:"update_check_disabled" json:"update_check_disabled"`
}

// AppSettingsID is the fixed document ID for the singleton AppSettings.
const AppSettingsID = "app"

// ClusterSample is a periodic snapshot of cluster-wide health numbers,
// recorded by admin's background poller (internal/admin/poller.go) so the
// dashboard can show trends, not just a live snapshot. CPU% reflects the
// scheduler's reservation spec (no cheap aggregate cgroup CPU usage is
// available); Mem% reflects real, live non-cache memory usage fanned out
// from every node's agent, since most services never set a memory
// reservation and that field would otherwise sit at 0 regardless of actual
// usage.
type ClusterSample struct {
	ID               uint64    `bson:"seq" json:"id"`
	Time             time.Time `bson:"ts" json:"time"`
	NodeCount        int       `bson:"node_count" json:"node_count"`
	ReadyNodes       int       `bson:"ready_nodes" json:"ready_nodes"`
	ServiceCount     int       `bson:"service_count" json:"service_count"`
	DegradedServices int       `bson:"degraded_services" json:"degraded_services"`
	RunningTasks     int       `bson:"running_tasks" json:"running_tasks"`
	DesiredTasks     int       `bson:"desired_tasks" json:"desired_tasks"`
	FailedTasks      int       `bson:"failed_tasks" json:"failed_tasks"`
	CPUReservedPct   float64   `bson:"cpu_reserved_pct" json:"cpu_reserved_pct"`
	MemUsedPct       float64   `bson:"mem_used_pct" json:"mem_used_pct"`
}
