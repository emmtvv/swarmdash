-- Initial schema: one table per store.Interface collection, mirroring the
-- documents MongoStore keeps in the equivalently-named Mongo collection
-- (mongo.go) - same primary keys, same set of indexes.

CREATE TABLE users (
    username              TEXT PRIMARY KEY,
    password_hash         TEXT NOT NULL,
    role                  TEXT NOT NULL,
    created_at            DATETIME NOT NULL,
    auth_source           TEXT NOT NULL DEFAULT '',
    must_change_password  INTEGER NOT NULL DEFAULT 0
);

-- token is the session cookie's SHA-256 hash (see hashToken in
-- internal/admin/auth.go), not the plaintext bearer value - same
-- treatment as api_tokens.hash below, so a copy of this file doesn't hand
-- out live sessions.
CREATE TABLE sessions (
    token       TEXT PRIMARY KEY,
    username    TEXT NOT NULL,
    created_at  DATETIME NOT NULL,
    expires_at  DATETIME NOT NULL
);
CREATE INDEX idx_sessions_expires_at ON sessions (expires_at);

CREATE TABLE login_attempts (
    username      TEXT PRIMARY KEY,
    fail_count    INTEGER NOT NULL DEFAULT 0,
    last_failure  DATETIME NOT NULL,
    locked_until  DATETIME NOT NULL
);
CREATE INDEX idx_login_attempts_last_failure ON login_attempts (last_failure);

CREATE TABLE api_tokens (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    hash           TEXT NOT NULL,
    role           TEXT NOT NULL DEFAULT '',
    created_by     TEXT NOT NULL,
    created_at     DATETIME NOT NULL,
    last_used_at   DATETIME NOT NULL
);
CREATE UNIQUE INDEX idx_api_tokens_hash ON api_tokens (hash);

CREATE TABLE registry_credentials (
    server        TEXT PRIMARY KEY,
    username      TEXT NOT NULL,
    -- Nullable despite RegistryCredential.PasswordEnc having no bson
    -- omitempty: database/sql binds a nil []byte as SQL NULL rather than a
    -- zero-length blob, so NOT NULL would reject a legitimately-empty
    -- ciphertext.
    password_enc  BLOB,
    created_at    DATETIME NOT NULL
);

CREATE TABLE webhooks (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    url         TEXT NOT NULL,
    created_at  DATETIME NOT NULL
);

CREATE TABLE gitops_stacks (
    id                TEXT PRIMARY KEY,
    stack_name        TEXT NOT NULL,
    repo_url          TEXT NOT NULL,
    ref               TEXT NOT NULL,
    compose_path      TEXT NOT NULL,
    auth_token_enc    BLOB,
    poll_seconds      INTEGER NOT NULL DEFAULT 0,
    last_commit       TEXT NOT NULL DEFAULT '',
    last_deployed_at  DATETIME NOT NULL,
    last_error        TEXT NOT NULL DEFAULT '',
    created_by        TEXT NOT NULL,
    created_at        DATETIME NOT NULL
);

CREATE TABLE deploy_hooks (
    id             TEXT PRIMARY KEY,
    hash           TEXT NOT NULL,
    service_name   TEXT NOT NULL,
    created_by     TEXT NOT NULL,
    created_at     DATETIME NOT NULL,
    last_used_at   DATETIME NOT NULL
);
CREATE UNIQUE INDEX idx_deploy_hooks_hash ON deploy_hooks (hash);
CREATE INDEX idx_deploy_hooks_service_name ON deploy_hooks (service_name);

-- seq is an INTEGER PRIMARY KEY (a rowid alias), so SQLite assigns each
-- insert one more than the current max automatically - equivalent to
-- MongoStore's hand-rolled nextSeq counter, without needing one.
CREATE TABLE audit_log (
    seq       INTEGER PRIMARY KEY,
    ts        DATETIME NOT NULL,
    username  TEXT NOT NULL,
    action    TEXT NOT NULL,
    target    TEXT NOT NULL,
    detail    TEXT NOT NULL DEFAULT '',
    success   INTEGER NOT NULL,
    error     TEXT NOT NULL DEFAULT ''
);

CREATE TABLE task_events (
    seq           INTEGER PRIMARY KEY,
    ts            DATETIME NOT NULL,
    service_name  TEXT NOT NULL,
    task_id       TEXT NOT NULL,
    node          TEXT NOT NULL,
    state         TEXT NOT NULL,
    message       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_task_events_service_name ON task_events (service_name);

CREATE TABLE cluster_samples (
    seq                INTEGER PRIMARY KEY,
    ts                 DATETIME NOT NULL,
    node_count         INTEGER NOT NULL,
    ready_nodes        INTEGER NOT NULL,
    service_count      INTEGER NOT NULL,
    degraded_services  INTEGER NOT NULL,
    running_tasks      INTEGER NOT NULL,
    desired_tasks      INTEGER NOT NULL,
    failed_tasks       INTEGER NOT NULL,
    cpu_reserved_pct   REAL NOT NULL,
    mem_used_pct       REAL NOT NULL
);
CREATE INDEX idx_cluster_samples_ts ON cluster_samples (ts);

-- Singleton row, same convention as MongoStore: always keyed by
-- store.SSOConfigID ("sso").
CREATE TABLE sso_config (
    id                  TEXT PRIMARY KEY,
    enabled             INTEGER NOT NULL DEFAULT 0,
    label               TEXT NOT NULL DEFAULT '',
    issuer              TEXT NOT NULL DEFAULT '',
    client_id           TEXT NOT NULL DEFAULT '',
    client_secret_enc   BLOB,
    scopes              TEXT NOT NULL DEFAULT '',
    auto_create_users   INTEGER NOT NULL DEFAULT 0,
    enforce_sso         INTEGER NOT NULL DEFAULT 0,
    default_role        TEXT NOT NULL DEFAULT '',
    allowed_domains     TEXT NOT NULL DEFAULT '',
    redirect_base_url   TEXT NOT NULL DEFAULT '',
    updated_at          DATETIME NOT NULL
);
