# Changelog

Notable changes to swarmdash are tracked here, following
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). This project
does not yet follow strict semantic versioning across releases — see the
README's "Known gaps" section for what's still evolving.

## [1.2.2] - 2026-08-23

### Security

- Deploy webhooks (`POST /hooks/deploy/{token}`) no longer accept an
  arbitrary `image` override in the request body. By design the token is
  the only credential and the endpoint stays unauthenticated, but an
  unvalidated `{"image": "..."}` field meant anyone who obtained the URL
  could redeploy the service running any image of their choosing -
  effective remote code execution in the target service's context. A hook
  now only accepts a same-repository tag/digest swap unless it was
  explicitly created with a new "allow full image override" option; a
  rejected override is recorded in the audit log.
- SSO login now re-authenticates by the OIDC `sub` claim instead of
  `preferred_username`/`email`, neither of which OIDC guarantees to be
  unique or immutable and which some identity providers let end users edit
  themselves. Previously, an attacker who could get their IdP to report a
  `preferred_username`/`email` matching an existing SSO-provisioned local
  account could sign into that account, including an admin's. Existing
  SSO accounts are transparently bound to their `sub` on next login; a
  later login attempt from a different `sub` claiming the same username is
  now rejected instead of silently succeeding. The allowed-domains check
  also now requires `email_verified` before trusting the email claim.
- The audit log previously covered every mutating admin action but not
  interactive container access: opening/closing a `/ws/exec` console
  session, browsing or downloading files out of a container, and local
  login/logout were all invisible in it. All five are now recorded
  (exec entries include the command run; file entries include the path),
  and every audit entry now records the client IP it was attributed to.
- Fixed a nil-pointer panic on every successful SSO login/auto-provision:
  the audit call for `user.sso_login`/`user.sso_provision` read the
  session user from request context, but the SSO callback runs before a
  session exists. Split the internal `audit` helper so pre-session flows
  (SSO callback, local login, logout) can attribute an audit entry to an
  explicit username instead.

## [1.2.1] - 2026-08-23

### Security

- Fixed a trust-model asymmetry introduced by the 1.2.0 rate limiting/lockout
  and `Secure`-cookie work: `clientIP` never trusted `X-Forwarded-For`, but
  `isSecureRequest` unconditionally trusted `X-Forwarded-Proto`. Behind a
  reverse proxy (Traefik, nginx, a cloud load balancer, ...) every request
  shares the proxy's address, which collapsed the per-IP global/login rate
  limiters and the (username, IP) lockout onto one bucket for every client -
  reopening the exact targeted-lockout gap migration `0002` closed, and
  making the global/login limiters into a self-inflicted denial of service.
  Both headers are now gated together on the same decision: off by default
  (today's safe behavior), or trusted from addresses listed in the new
  `--trusted-proxies` flag. See the Hardening doc.
- The global rate limiter no longer wraps `/static/`: a single page load
  pulls in several static assets alongside the dynamic request, so counting
  them against the same per-IP burst as the rest of the app could exhaust it
  before a real request was served.

## [1.2.0] - 2026-08-23

### Security

- RBAC is now an allow-list: `internal/admin/routes.go` declares every
  protected route's access level explicitly (`ViewerOK`), and `requireRole`
  resolves against that table instead of pattern-matching a hand-maintained
  deny-list. A route added without deciding who may reach it now defaults
  to admin-only instead of silently becoming viewer-readable. As part of
  this, `/files/{taskID}` and `/files/{taskID}/download` (arbitrary file
  read out of a container - including anything bind-mounted from
  `/run/secrets`) moved from viewer-readable to admin-only.
- Session cookies are hashed at rest (SHA-256, same treatment as API
  tokens) instead of the plaintext bearer value being the SQLite/MongoDB
  primary key - a copied database file or backup no longer hands out live
  sessions.
- Session, CSRF, and SSO-state cookies now set `Secure` whenever the
  request arrived over HTTPS (direct TLS or a reverse proxy's
  `X-Forwarded-Proto`).
- The agent's cluster-secret check is constant-time (previously a plain
  `!=` comparison, timing-leakable).
- Local-login lockout is now keyed by (username, client IP) instead of
  username alone - a username-only key let an unauthenticated attacker
  lock a known account (e.g. the bootstrap `admin`) out from under its real
  owner with a handful of bad requests, from anywhere.
- An unknown username on `/login` now burns a dummy bcrypt comparison
  before responding, closing a timing side-channel that let it be
  distinguished from a known username with a wrong password.
- Added IP-based rate limiting: a tight limiter on `POST /login`
  (backstops the per-account lockout against spraying many usernames from
  one address) and a loose global limiter across the rest of the app -
  there was none at all before.
- `/ws/exec` sessions are capped at 50 concurrent (`maxConcurrentExecSessions`)
  to bound the resource cost of abandoned or malicious interactive shells.
- The admin↔agent websocket proxy now sends periodic pings and enforces a
  read deadline on both legs, so an idle session or a dead peer is
  detected instead of leaving relay goroutines blocked forever.
- `Content-Disposition` headers for file/log/backup downloads are now built
  with `mime.FormatMediaType` (new `internal/httpx.SetAttachment`) instead
  of hand-spliced string concatenation, which let a filename containing a
  `"` break out of the quoted parameter.
- Added `swarmdash rotate-cluster-secret`: registry passwords, GitOps auth
  tokens, and the SSO client secret are encrypted at rest with a key
  derived from the cluster secret, which previously had no rotation path -
  changing `SWARMDASH_CLUSTER_SECRET` and redeploying made all three
  permanently undecryptable. See the Hardening doc.
- Added a Settings -> General toggle to disable the dashboard's
  update-available check entirely, including the CSP's `connect-src`
  exception for `api.github.com` - previously that request always fired
  with no way to opt out short of blocking it at the network layer.

## [1.1.1] - 2026-08-23

### Changed

- The dashboard's update-available check now runs client-side: the browser
  compares the running version against the latest GitHub release
  (github.com/emmtvv/swarmdash) directly on page load, instead of the admin
  server polling GitHub every 15 minutes and caching the result. This drops
  `internal/updatecheck`'s `Checker`/`Run` down to just the `CurrentVersion`
  constant it was already the source of truth for - still bumped by hand
  alongside each new entry here. The CSP's `connect-src` now allows
  `https://api.github.com` for this.

## [1.1.0] - 2026-08-23

### Added

- Local storage backend: `--storage-driver local` persists admin state
  (users, sessions, audit log, tokens, ...) in a local SQLite database
  under `--data-dir`/`SWARMDASH_DATA_DIR` instead of MongoDB, for
  deployments that don't want to run a MongoDB service just for the admin
  UI. Schema changes ship as forward-only SQL migrations
  (`internal/store/migrations`) applied automatically on startup. See
  `internal/store/sqlite.go` and the README's Storage section.

### Changed

- `--storage-driver` now defaults to `local` instead of `mongo` - running
  `swarmdash admin` with no storage flags no longer requires a MongoDB
  instance to be reachable. `mongo` is still available (and still required
  for running more than one admin replica).
- [deploy/stack.yml](deploy/stack.yml) dropped the bundled `mongo` service
  and its `swarmdash_mongo_data` volume. `admin` now stores its SQLite
  database on a `swarmdash_data` volume instead (`SWARMDASH_DATA_DIR=/data`
  in the stack). Deployments that want MongoDB (for HA, multiple admin
  replicas) point `--storage-driver=mongo` at an external deployment - see
  the "Optional: HA admin" comment in `deploy/stack.yml`.

## [1.0.2] - 2026-08-22

### Added

- Update check: the admin UI now compares the running version against the
  latest GitHub release (github.com/emmtvv/swarmdash) every 15 minutes and
  shows a dismissible banner when a newer version is available. The current
  version is a constant in `internal/updatecheck` - bump it by hand
  alongside each new entry here, since not every deployment is built via
  `make build`/`make docker`.

## [1.0.1] - 2026-08-22

### Fixed

- Node picker on the Volumes and Images pages, and the Reset password
  action on the Users settings page, were using inline event handlers
  (`onchange=`/`onsubmit=`) that violate the app's Content-Security-Policy
  and get blocked by browsers. Moved this behavior into `app.js`.

## [1.0.0] - 2026-08-22

- First public release.
