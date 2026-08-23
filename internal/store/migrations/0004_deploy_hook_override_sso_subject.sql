-- allow_image_override: deploy webhooks default (and every pre-existing
-- hook defaults, via DEFAULT 0) to only accepting a same-repository tag
-- swap from their {"image": ...} body, not an arbitrary image - see
-- sameImageRepo in internal/admin/handlers_deploy_hooks.go. Opting a hook
-- into full image override is a deliberate, separate choice made when the
-- hook is created.
ALTER TABLE deploy_hooks ADD COLUMN allow_image_override INTEGER NOT NULL DEFAULT 0;

-- sso_subject pins an SSO-provisioned user record to the identity
-- provider's `sub` claim - the only OIDC claim guaranteed unique and
-- immutable for a given account - instead of trusting `preferred_username`
-- or `email`, either of which can be self-service-editable at the IdP and
-- so previously let one SSO identity take over another's local username on
-- login. Empty for local accounts and for SSO accounts provisioned before
-- this migration (backfilled on their next successful login - see
-- handleSSOCallback). The partial unique index only applies to non-empty
-- values so any number of rows can share the '' default.
ALTER TABLE users ADD COLUMN sso_subject TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX idx_users_sso_subject ON users (sso_subject) WHERE sso_subject != '';

-- ip records the client address an audit entry is attributed to (see
-- audit/auditAs in internal/admin/audit.go), so an entry can be traced
-- back to where it came from - Username alone is self-reported (a session
-- cookie, a claimed SSO identity), not itself an authentication factor.
ALTER TABLE audit_log ADD COLUMN ip TEXT NOT NULL DEFAULT '';
