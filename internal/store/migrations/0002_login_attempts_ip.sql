-- login_attempts moves from a per-username lockout key to per-(username,
-- client IP): a username-only key lets an unauthenticated attacker lock a
-- known account (e.g. "admin") out from under its real owner with a
-- handful of bad requests. Dropping and recreating is safe - this table is
-- purely transient rate-limiting state, not user data, so losing any
-- in-flight lockouts on upgrade just resets everyone's counters to zero.
DROP TABLE login_attempts;

CREATE TABLE login_attempts (
    key           TEXT PRIMARY KEY,
    username      TEXT NOT NULL,
    ip            TEXT NOT NULL,
    fail_count    INTEGER NOT NULL DEFAULT 0,
    last_failure  DATETIME NOT NULL,
    locked_until  DATETIME NOT NULL
);
CREATE INDEX idx_login_attempts_last_failure ON login_attempts (last_failure);
