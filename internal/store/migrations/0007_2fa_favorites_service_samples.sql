-- Two-factor authentication (TOTP), favorites and per-user UI language on
-- users. totp_secret_enc is ciphertext (see store.User.TOTPSecretEnc);
-- recovery_code_hashes and favorites are JSON arrays of strings.
ALTER TABLE users ADD COLUMN totp_secret_enc BLOB;
ALTER TABLE users ADD COLUMN totp_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN recovery_code_hashes TEXT NOT NULL DEFAULT '[]';
ALTER TABLE users ADD COLUMN favorites TEXT NOT NULL DEFAULT '[]';
ALTER TABLE users ADD COLUMN language TEXT NOT NULL DEFAULT '';

ALTER TABLE app_settings ADD COLUMN require_2fa INTEGER NOT NULL DEFAULT 0;

-- Per-service real CPU/memory usage, sampled once a minute by the poller
-- (see store.ServiceSample) and kept for the same 14 days as
-- cluster_samples.
CREATE TABLE service_samples (
    ts            DATETIME NOT NULL,
    service_name  TEXT NOT NULL,
    cpu_cores     REAL NOT NULL,
    mem_bytes     INTEGER NOT NULL,
    tasks         INTEGER NOT NULL
);
CREATE INDEX idx_service_samples_service_ts ON service_samples (service_name, ts);
CREATE INDEX idx_service_samples_ts ON service_samples (ts);
