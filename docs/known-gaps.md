# Known gaps / next steps

- **Multi-cluster** — one admin instance manages exactly one swarm; running
  against several clusters means running several admin instances.
- The degraded-service/node-down poller state (and its 2-minute alert
  threshold) lives in memory and resets on admin restart - a restart
  during an ongoing incident means one missed/delayed alert, not a false
  one (task event history in MongoDB is unaffected).
- GitOps auto-poll last-checked timestamps are in-memory too (same
  reasoning) — a restart just delays the next check by up to the poller's
  30s tick, it doesn't skip or repeat a deploy.
- Deploy webhooks are unauthenticated by design (the token in the URL is
  the credential, matching GitHub/GitLab/Portainer's model) — anyone who
  obtains the URL can force a redeploy of that one service. Treat it like
  any other CI secret.
- SSO is OIDC-only — no SAML. Covers every major IdP (Okta, Authentik,
  Keycloak, Google Workspace, Azure AD, Auth0, ...) since they all speak
  OIDC, but a shop standardized on SAML-only (e.g. some ADFS setups) can't
  point it at swarmdash directly.
- "Require SSO" (Settings → SSO) has no built-in break-glass: once it's
  on, local password login is rejected server-side with no override flag
  or recovery user. If the identity provider is unreachable or
  misconfigured afterwards, every account is locked out until an operator
  flips `enforce_sso` back to `false` directly in the `sso_config`
  table/collection (SQLite or MongoDB, whichever `--storage-driver` is
  configured). Verify SSO sign-in actually works before turning this on.
- `/health` checks the store (SQLite or MongoDB) and the local Docker
  daemon, not connectivity to any particular agent — an admin instance can
  report healthy while one node's agent is unreachable (that surfaces in
  the UI as a failed exec/logs/stats proxy to that node, not as a failed
  health probe).
- Backup/restore covers configuration only, not audit log or metrics
  history, and restore is additive (upsert) rather than a full
  point-in-time rollback — it can't undo a deletion made after the backup
  was taken.
- The "viewer" role is read-only, not secrets-blind: it can see every
  service/stack's full spec, including environment variable values, both
  in the service detail page and via `GET /services/{name}/export.yml` /
  `GET /stacks/{name}/export.yml` — real deployments routinely carry
  DB passwords or API keys in `environment:`. This is deliberate and
  consistent across both surfaces (redacting only the export while the
  detail page shows the same values would be a false sense of security,
  not an actual fix), unlike the interactive console and file
  browser/download (`/exec/*`, `/files/*`), which stay admin-only because
  they can reach arbitrary files inside a container - e.g. bind-mounted
  Docker secrets - well beyond what's declared in the service spec
  itself. Only grant "viewer" to people who are trusted with those
  values, the same as anyone with shell access to a node.
