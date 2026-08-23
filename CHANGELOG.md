# Changelog

Notable changes to swarmdash are tracked here, following
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). This project
does not yet follow strict semantic versioning across releases — see the
README's "Known gaps" section for what's still evolving.

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
