// SQLiteStore is the dependency-free alternative to MongoStore: it persists
// the exact same admin state in a local SQLite database file instead of a
// MongoDB deployment, for installs that don't want to run MongoDB just for
// the admin UI. The schema lives in internal/store/migrations (applied by
// migrate.go, see its doc comment for how schema changes are versioned
// going forward) rather than being created ad hoc here, so upgrading
// swarmdash across releases that change the schema is just starting the new
// binary against the existing data directory.
//
// The tradeoff is the one MongoStore's docs call out in reverse: state is
// pinned to one container/host's local disk (a single *.db file, plus
// SQLite's -wal/-shm sidecar files while the process is running), so only a
// single admin replica may point at a given data directory at a time. Fine
// for the common case of one admin instance; use MongoStore instead if you
// want more than one replica for HA.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const sqliteBusyTimeout = 5 * time.Second

// loginAttemptTTL mirrors MongoStore's TTL index on login_attempts (24h
// after the last failure) - see ensureIndexes in mongo.go.
const loginAttemptTTL = 24 * time.Hour

const pruneInterval = 5 * time.Minute

type SQLiteStore struct {
	db *sql.DB

	stop chan struct{}
	wg   sync.WaitGroup
}

// OpenSQLite opens (creating and migrating if necessary) a SQLite database
// at dir/swarmdash.db.
func OpenSQLite(dir string) (*SQLiteStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)",
		filepath.Join(dir, "swarmdash.db"), sqliteBusyTimeout.Milliseconds())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	// Concurrent writers under WAL still serialize (SQLite allows one
	// writer at a time) and would otherwise burn through connections from
	// the pool retrying on SQLITE_BUSY; pinning to a single connection
	// sidesteps that entirely; this admin UI never needs write throughput
	// beyond what one connection can serialize.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate sqlite database: %w", err)
	}

	s := &SQLiteStore{db: db, stop: make(chan struct{})}
	s.pruneExpired()
	s.wg.Add(1)
	go s.pruneLoop()
	return s, nil
}

// pruneLoop periodically deletes expired sessions and stale login attempts
// - the SQLite equivalent of the TTL indexes MongoStore relies on
// (ensureIndexes in mongo.go). GetSession already filters out expired rows
// on read, so this is pure disk hygiene rather than something correctness
// depends on.
func (s *SQLiteStore) pruneLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(pruneInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.pruneExpired()
		}
	}
}

func (s *SQLiteStore) pruneExpired() {
	now := time.Now()
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, now)
	_, _ = s.db.Exec(`DELETE FROM login_attempts WHERE last_failure <= ?`, now.Add(-loginAttemptTTL))
}

func (s *SQLiteStore) Close() error {
	close(s.stop)
	s.wg.Wait()
	return s.db.Close()
}

func (s *SQLiteStore) Ping() error {
	return s.db.Ping()
}

func notFoundSQL(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (s *SQLiteStore) PutUser(u User) error {
	_, err := s.db.Exec(`
		INSERT INTO users (username, password_hash, role, created_at, auth_source, must_change_password, sso_subject)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(username) DO UPDATE SET
			password_hash = excluded.password_hash,
			role = excluded.role,
			created_at = excluded.created_at,
			auth_source = excluded.auth_source,
			must_change_password = excluded.must_change_password,
			sso_subject = excluded.sso_subject`,
		u.Username, u.PasswordHash, u.Role, u.CreatedAt, u.AuthSource, u.MustChangePassword, u.SSOSubject)
	return err
}

func (s *SQLiteStore) GetUser(username string) (User, error) {
	var u User
	err := s.db.QueryRow(`SELECT username, password_hash, role, created_at, auth_source, must_change_password, sso_subject FROM users WHERE username = ?`, username).
		Scan(&u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt, &u.AuthSource, &u.MustChangePassword, &u.SSOSubject)
	return u, notFoundSQL(err)
}

// GetUserBySSOSubject looks up the user record pinned to an OIDC `sub`
// claim (see User.SSOSubject's doc comment). ErrNotFound if no user has
// bound that subject yet - either because they haven't logged in since
// migration 0004 backfilled the column, or because this is their first
// login ever.
func (s *SQLiteStore) GetUserBySSOSubject(subject string) (User, error) {
	var u User
	err := s.db.QueryRow(`SELECT username, password_hash, role, created_at, auth_source, must_change_password, sso_subject FROM users WHERE sso_subject = ? AND sso_subject != ''`, subject).
		Scan(&u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt, &u.AuthSource, &u.MustChangePassword, &u.SSOSubject)
	return u, notFoundSQL(err)
}

func (s *SQLiteStore) HasAnyUser() (bool, error) {
	var exists bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM users)`).Scan(&exists)
	return exists, err
}

func (s *SQLiteStore) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT username, password_hash, role, created_at, auth_source, must_change_password, sso_subject FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt, &u.AuthSource, &u.MustChangePassword, &u.SSOSubject); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) DeleteUser(username string) error {
	_, err := s.db.Exec(`DELETE FROM users WHERE username = ?`, username)
	return err
}

func (s *SQLiteStore) PutSession(sess Session) error {
	_, err := s.db.Exec(`
		INSERT INTO sessions (token, username, created_at, expires_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(token) DO UPDATE SET
			username = excluded.username,
			created_at = excluded.created_at,
			expires_at = excluded.expires_at`,
		sess.Token, sess.Username, sess.CreatedAt, sess.ExpiresAt)
	return err
}

func (s *SQLiteStore) GetSession(token string) (Session, error) {
	var sess Session
	err := s.db.QueryRow(`SELECT token, username, created_at, expires_at FROM sessions WHERE token = ? AND expires_at > ?`, token, time.Now()).
		Scan(&sess.Token, &sess.Username, &sess.CreatedAt, &sess.ExpiresAt)
	return sess, notFoundSQL(err)
}

func (s *SQLiteStore) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

func (s *SQLiteStore) PutLoginAttempt(a LoginAttempt) error {
	_, err := s.db.Exec(`
		INSERT INTO login_attempts (key, username, ip, fail_count, last_failure, locked_until)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			fail_count = excluded.fail_count,
			last_failure = excluded.last_failure,
			locked_until = excluded.locked_until`,
		a.Key, a.Username, a.IP, a.FailCount, a.LastFailure, a.LockedUntil)
	return err
}

func (s *SQLiteStore) GetLoginAttempt(key string) (LoginAttempt, error) {
	var a LoginAttempt
	err := s.db.QueryRow(`SELECT key, username, ip, fail_count, last_failure, locked_until FROM login_attempts WHERE key = ?`, key).
		Scan(&a.Key, &a.Username, &a.IP, &a.FailCount, &a.LastFailure, &a.LockedUntil)
	return a, notFoundSQL(err)
}

func (s *SQLiteStore) DeleteLoginAttempt(key string) error {
	_, err := s.db.Exec(`DELETE FROM login_attempts WHERE key = ?`, key)
	return err
}

func (s *SQLiteStore) AppendAudit(e AuditEntry) error {
	_, err := s.db.Exec(`INSERT INTO audit_log (ts, username, action, target, detail, success, error, ip) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		time.Now(), e.Username, e.Action, e.Target, e.Detail, e.Success, e.Error, e.IP)
	return err
}

func (s *SQLiteStore) ListAudit(skip, limit int) ([]AuditEntry, error) {
	rows, err := s.db.Query(`SELECT seq, ts, username, action, target, detail, success, error, ip FROM audit_log ORDER BY seq DESC LIMIT ? OFFSET ?`, limit, skip)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Time, &e.Username, &e.Action, &e.Target, &e.Detail, &e.Success, &e.Error, &e.IP); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) CountAudit() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&n)
	return n, err
}

func (s *SQLiteStore) PutAPIToken(t APIToken) error {
	_, err := s.db.Exec(`
		INSERT INTO api_tokens (id, name, hash, role, created_by, created_at, last_used_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			hash = excluded.hash,
			role = excluded.role,
			created_by = excluded.created_by,
			created_at = excluded.created_at,
			last_used_at = excluded.last_used_at`,
		t.ID, t.Name, t.Hash, t.Role, t.CreatedBy, t.CreatedAt, t.LastUsedAt)
	return err
}

func (s *SQLiteStore) ListAPITokens() ([]APIToken, error) {
	rows, err := s.db.Query(`SELECT id, name, hash, role, created_by, created_at, last_used_at FROM api_tokens ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.Hash, &t.Role, &t.CreatedBy, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) FindAPITokenByHash(hash string) (APIToken, error) {
	var t APIToken
	err := s.db.QueryRow(`SELECT id, name, hash, role, created_by, created_at, last_used_at FROM api_tokens WHERE hash = ?`, hash).
		Scan(&t.ID, &t.Name, &t.Hash, &t.Role, &t.CreatedBy, &t.CreatedAt, &t.LastUsedAt)
	return t, notFoundSQL(err)
}

func (s *SQLiteStore) TouchAPIToken(id string) error {
	_, err := s.db.Exec(`UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, time.Now(), id)
	return err
}

func (s *SQLiteStore) DeleteAPIToken(id string) error {
	_, err := s.db.Exec(`DELETE FROM api_tokens WHERE id = ?`, id)
	return err
}

func (s *SQLiteStore) PutRegistryCredential(c RegistryCredential) error {
	_, err := s.db.Exec(`
		INSERT INTO registry_credentials (server, username, password_enc, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(server) DO UPDATE SET
			username = excluded.username,
			password_enc = excluded.password_enc,
			created_at = excluded.created_at`,
		c.Server, c.Username, c.PasswordEnc, c.CreatedAt)
	return err
}

func (s *SQLiteStore) GetRegistryCredential(server string) (RegistryCredential, error) {
	var c RegistryCredential
	err := s.db.QueryRow(`SELECT server, username, password_enc, created_at FROM registry_credentials WHERE server = ?`, server).
		Scan(&c.Server, &c.Username, &c.PasswordEnc, &c.CreatedAt)
	return c, notFoundSQL(err)
}

func (s *SQLiteStore) ListRegistryCredentials() ([]RegistryCredential, error) {
	rows, err := s.db.Query(`SELECT server, username, password_enc, created_at FROM registry_credentials ORDER BY server`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RegistryCredential
	for rows.Next() {
		var c RegistryCredential
		if err := rows.Scan(&c.Server, &c.Username, &c.PasswordEnc, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) DeleteRegistryCredential(server string) error {
	_, err := s.db.Exec(`DELETE FROM registry_credentials WHERE server = ?`, server)
	return err
}

func (s *SQLiteStore) AppendTaskEvent(e TaskEvent) error {
	_, err := s.db.Exec(`INSERT INTO task_events (ts, service_name, task_id, node, state, message) VALUES (?, ?, ?, ?, ?, ?)`,
		time.Now(), e.ServiceName, e.TaskID, e.Node, e.State, e.Message)
	return err
}

func (s *SQLiteStore) ListTaskEvents(skip, limit int, serviceName string) ([]TaskEvent, error) {
	query := `SELECT seq, ts, service_name, task_id, node, state, message FROM task_events`
	args := []any{}
	if serviceName != "" {
		query += ` WHERE service_name = ?`
		args = append(args, serviceName)
	}
	query += ` ORDER BY seq DESC LIMIT ? OFFSET ?`
	args = append(args, limit, skip)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []TaskEvent
	for rows.Next() {
		var e TaskEvent
		if err := rows.Scan(&e.ID, &e.Time, &e.ServiceName, &e.TaskID, &e.Node, &e.State, &e.Message); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) CountTaskEvents(serviceName string) (int, error) {
	var n int
	var err error
	if serviceName == "" {
		err = s.db.QueryRow(`SELECT COUNT(*) FROM task_events`).Scan(&n)
	} else {
		err = s.db.QueryRow(`SELECT COUNT(*) FROM task_events WHERE service_name = ?`, serviceName).Scan(&n)
	}
	return n, err
}

func (s *SQLiteStore) PutWebhook(w Webhook) error {
	_, err := s.db.Exec(`
		INSERT INTO webhooks (id, name, url, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, url = excluded.url, created_at = excluded.created_at`,
		w.ID, w.Name, w.URL, w.CreatedAt)
	return err
}

func (s *SQLiteStore) ListWebhooks() ([]Webhook, error) {
	rows, err := s.db.Query(`SELECT id, name, url, created_at FROM webhooks ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Webhook
	for rows.Next() {
		var w Webhook
		if err := rows.Scan(&w.ID, &w.Name, &w.URL, &w.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) DeleteWebhook(id string) error {
	_, err := s.db.Exec(`DELETE FROM webhooks WHERE id = ?`, id)
	return err
}

func (s *SQLiteStore) PutGitStack(g GitStack) error {
	_, err := s.db.Exec(`
		INSERT INTO gitops_stacks (id, stack_name, repo_url, ref, compose_path, auth_token_enc, poll_seconds, last_commit, last_deployed_at, last_error, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			stack_name = excluded.stack_name,
			repo_url = excluded.repo_url,
			ref = excluded.ref,
			compose_path = excluded.compose_path,
			auth_token_enc = excluded.auth_token_enc,
			poll_seconds = excluded.poll_seconds,
			last_commit = excluded.last_commit,
			last_deployed_at = excluded.last_deployed_at,
			last_error = excluded.last_error,
			created_by = excluded.created_by,
			created_at = excluded.created_at`,
		g.ID, g.StackName, g.RepoURL, g.Ref, g.ComposePath, g.AuthTokenEnc, g.PollSeconds, g.LastCommit, g.LastDeployedAt, g.LastError, g.CreatedBy, g.CreatedAt)
	return err
}

func (s *SQLiteStore) ListGitStacks() ([]GitStack, error) {
	rows, err := s.db.Query(`SELECT id, stack_name, repo_url, ref, compose_path, auth_token_enc, poll_seconds, last_commit, last_deployed_at, last_error, created_by, created_at FROM gitops_stacks ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []GitStack
	for rows.Next() {
		var g GitStack
		if err := rows.Scan(&g.ID, &g.StackName, &g.RepoURL, &g.Ref, &g.ComposePath, &g.AuthTokenEnc, &g.PollSeconds, &g.LastCommit, &g.LastDeployedAt, &g.LastError, &g.CreatedBy, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) GetGitStack(id string) (GitStack, error) {
	var g GitStack
	err := s.db.QueryRow(`SELECT id, stack_name, repo_url, ref, compose_path, auth_token_enc, poll_seconds, last_commit, last_deployed_at, last_error, created_by, created_at FROM gitops_stacks WHERE id = ?`, id).
		Scan(&g.ID, &g.StackName, &g.RepoURL, &g.Ref, &g.ComposePath, &g.AuthTokenEnc, &g.PollSeconds, &g.LastCommit, &g.LastDeployedAt, &g.LastError, &g.CreatedBy, &g.CreatedAt)
	return g, notFoundSQL(err)
}

func (s *SQLiteStore) DeleteGitStack(id string) error {
	_, err := s.db.Exec(`DELETE FROM gitops_stacks WHERE id = ?`, id)
	return err
}

func (s *SQLiteStore) PutDeployHook(h DeployHook) error {
	_, err := s.db.Exec(`
		INSERT INTO deploy_hooks (id, hash, service_name, created_by, created_at, last_used_at, allow_image_override)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			hash = excluded.hash,
			service_name = excluded.service_name,
			created_by = excluded.created_by,
			created_at = excluded.created_at,
			last_used_at = excluded.last_used_at,
			allow_image_override = excluded.allow_image_override`,
		h.ID, h.Hash, h.ServiceName, h.CreatedBy, h.CreatedAt, h.LastUsedAt, h.AllowImageOverride)
	return err
}

func (s *SQLiteStore) ListDeployHooks() ([]DeployHook, error) {
	rows, err := s.db.Query(`SELECT id, hash, service_name, created_by, created_at, last_used_at, allow_image_override FROM deploy_hooks ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []DeployHook
	for rows.Next() {
		var h DeployHook
		if err := rows.Scan(&h.ID, &h.Hash, &h.ServiceName, &h.CreatedBy, &h.CreatedAt, &h.LastUsedAt, &h.AllowImageOverride); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListDeployHooksForService(serviceName string) ([]DeployHook, error) {
	rows, err := s.db.Query(`SELECT id, hash, service_name, created_by, created_at, last_used_at, allow_image_override FROM deploy_hooks WHERE service_name = ? ORDER BY created_at`, serviceName)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []DeployHook
	for rows.Next() {
		var h DeployHook
		if err := rows.Scan(&h.ID, &h.Hash, &h.ServiceName, &h.CreatedBy, &h.CreatedAt, &h.LastUsedAt, &h.AllowImageOverride); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) FindDeployHookByHash(hash string) (DeployHook, error) {
	var h DeployHook
	err := s.db.QueryRow(`SELECT id, hash, service_name, created_by, created_at, last_used_at, allow_image_override FROM deploy_hooks WHERE hash = ?`, hash).
		Scan(&h.ID, &h.Hash, &h.ServiceName, &h.CreatedBy, &h.CreatedAt, &h.LastUsedAt, &h.AllowImageOverride)
	return h, notFoundSQL(err)
}

func (s *SQLiteStore) TouchDeployHook(id string) error {
	_, err := s.db.Exec(`UPDATE deploy_hooks SET last_used_at = ? WHERE id = ?`, time.Now(), id)
	return err
}

func (s *SQLiteStore) DeleteDeployHook(id string) error {
	_, err := s.db.Exec(`DELETE FROM deploy_hooks WHERE id = ?`, id)
	return err
}

func (s *SQLiteStore) AppendClusterSample(cs ClusterSample) error {
	_, err := s.db.Exec(`
		INSERT INTO cluster_samples (ts, node_count, ready_nodes, service_count, degraded_services, running_tasks, desired_tasks, failed_tasks, cpu_reserved_pct, mem_used_pct)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		time.Now(), cs.NodeCount, cs.ReadyNodes, cs.ServiceCount, cs.DegradedServices, cs.RunningTasks, cs.DesiredTasks, cs.FailedTasks, cs.CPUReservedPct, cs.MemUsedPct)
	return err
}

func (s *SQLiteStore) ListClusterSamples(since time.Time) ([]ClusterSample, error) {
	rows, err := s.db.Query(`
		SELECT seq, ts, node_count, ready_nodes, service_count, degraded_services, running_tasks, desired_tasks, failed_tasks, cpu_reserved_pct, mem_used_pct
		FROM cluster_samples WHERE ts >= ? ORDER BY seq`, since)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ClusterSample
	for rows.Next() {
		var cs ClusterSample
		if err := rows.Scan(&cs.ID, &cs.Time, &cs.NodeCount, &cs.ReadyNodes, &cs.ServiceCount, &cs.DegradedServices, &cs.RunningTasks, &cs.DesiredTasks, &cs.FailedTasks, &cs.CPUReservedPct, &cs.MemUsedPct); err != nil {
			return nil, err
		}
		out = append(out, cs)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) PruneClusterSamples(olderThan time.Time) error {
	_, err := s.db.Exec(`DELETE FROM cluster_samples WHERE ts < ?`, olderThan)
	return err
}

func (s *SQLiteStore) GetSSOConfig() (SSOConfig, error) {
	var c SSOConfig
	err := s.db.QueryRow(`
		SELECT id, enabled, label, issuer, client_id, client_secret_enc, scopes, auto_create_users, enforce_sso, default_role, allowed_domains, redirect_base_url, updated_at
		FROM sso_config WHERE id = ?`, SSOConfigID).
		Scan(&c.ID, &c.Enabled, &c.Label, &c.Issuer, &c.ClientID, &c.ClientSecretEnc, &c.Scopes, &c.AutoCreateUsers, &c.EnforceSSO, &c.DefaultRole, &c.AllowedDomains, &c.RedirectBaseURL, &c.UpdatedAt)
	return c, notFoundSQL(err)
}

func (s *SQLiteStore) PutSSOConfig(c SSOConfig) error {
	c.ID = SSOConfigID
	_, err := s.db.Exec(`
		INSERT INTO sso_config (id, enabled, label, issuer, client_id, client_secret_enc, scopes, auto_create_users, enforce_sso, default_role, allowed_domains, redirect_base_url, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			enabled = excluded.enabled,
			label = excluded.label,
			issuer = excluded.issuer,
			client_id = excluded.client_id,
			client_secret_enc = excluded.client_secret_enc,
			scopes = excluded.scopes,
			auto_create_users = excluded.auto_create_users,
			enforce_sso = excluded.enforce_sso,
			default_role = excluded.default_role,
			allowed_domains = excluded.allowed_domains,
			redirect_base_url = excluded.redirect_base_url,
			updated_at = excluded.updated_at`,
		c.ID, c.Enabled, c.Label, c.Issuer, c.ClientID, c.ClientSecretEnc, c.Scopes, c.AutoCreateUsers, c.EnforceSSO, c.DefaultRole, c.AllowedDomains, c.RedirectBaseURL, c.UpdatedAt)
	return err
}

func (s *SQLiteStore) GetAppSettings() (AppSettings, error) {
	var a AppSettings
	err := s.db.QueryRow(`SELECT id, update_check_disabled FROM app_settings WHERE id = ?`, AppSettingsID).
		Scan(&a.ID, &a.UpdateCheckDisabled)
	return a, notFoundSQL(err)
}

func (s *SQLiteStore) PutAppSettings(a AppSettings) error {
	a.ID = AppSettingsID
	_, err := s.db.Exec(`
		INSERT INTO app_settings (id, update_check_disabled)
		VALUES (?, ?)
		ON CONFLICT(id) DO UPDATE SET update_check_disabled = excluded.update_check_disabled`,
		a.ID, a.UpdateCheckDisabled)
	return err
}
