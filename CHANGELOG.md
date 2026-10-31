# Changelog

Notable changes to swarmdash are tracked here, following
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). This project
does not yet follow strict semantic versioning across releases — see the
README's "Known gaps" section for what's still evolving.

## [1.4.0] - 2026-10-31

### Added

- **Two-factor authentication (TOTP)** for local-password accounts: set it
  up from the account page (the lock icon next to your name) with any
  authenticator app, get ten one-time recovery codes, and sign in with a
  code after your password. Settings → General can require it for every
  local account; admins can reset a user's two-factor from Settings →
  Users. SSO accounts and API tokens are unaffected.
- **Global search** in the command palette (Cmd/Ctrl+K, or `/`): services
  (by name or image), stacks, nodes (hostname, ID, address, labels), tasks
  (by ID), networks, secrets and configs, next to the page list.
- **Favorites**: star services, stacks and nodes; they show up with live
  status at the top of the dashboard and first in the command palette.
- **Per-service resource usage**: real CPU and memory per service and per
  task on the Services list and a service's page, with a 24h chart. It's
  collected once a minute from the same agent stats the dashboard uses.
- **Volume backups** (Volumes → Backups): back up a volume to a `.tar.gz`
  on the node that holds it, download it, upload one, and restore it into
  the same or a new volume - all as background jobs, so large volumes
  don't hold a request open. Agents keep backups in `--backup-dir`
  (default `/var/lib/swarmdash/backups`; `deploy/stack.yml` mounts a
  `swarmdash_backups` volume there).
- **Interface languages**: English, Russian, Ukrainian, German, Spanish,
  Portuguese, French, Chinese and Georgian, chosen from the selector in
  the sidebar, on the account page or on the login page (saved on the
  account). English is the default; the browser's language isn't used.

### Changed

- Rotating the cluster secret (`swarmdash rotate-cluster-secret`) and
  restoring a configuration backup now also cover two-factor secrets.

## [1.3.0] - 2026-10-28

### Added

- **Image updates.** A service's page shows the newest patch (same major
  and minor version), minor (same major version) and major version of its image tag available in the registry,
  with a button to update to each. Automatic updates run hourly, set per
  service on its page or with the `swarmdash.autoupdate` label
  (`off`/`patch`/`minor`/`major`), or for every service at once in Settings →
  General. Only tags of the same form as the deployed one are considered
  (`1.27.1-alpine` → `1.28.0-alpine`, never `-rc` builds or `latest`); a
  version Swarm rolled back from isn't retried. Updates are audited and
  sent to webhooks as `service_image_updated`.
- **Stack logs.** A stack's page gets a Logs button: every service's logs
  merged into one stream ordered by time, each line tagged with its
  service, with a per-service selector, a text filter, follow and
  download.

### Changed

- **Registry logins are checked when saved** (Settings → Registries), so a
  wrong password shows up right away instead of on the next deploy or
  automatic image update; "Save without checking" covers a registry the
  nodes can reach but admin can't. The address is normalized
  (`https://ghcr.io/` → `ghcr.io`, `index.docker.io` → `docker.io`) - it
  used to have to match the image's registry exactly, or the login was
  silently not used. Logins saved earlier in another form still match.

## [1.2.6] - 2026-10-18

### Fixed

- **Wrong version on the dashboard.** The version shown in the update
  banner was a hand-maintained constant that wasn't bumped for 1.2.4 and
  1.2.5, so those releases reported themselves as 1.2.3 and the banner
  offered an "update" to the version already running. The version is now
  read from the latest release entry in CHANGELOG.md, which is embedded
  into the binary, so it no longer needs to be bumped separately.

## [1.2.5] - 2026-10-18

### Fixed

- **Missing resource usage on the leader node.** Docker can report a
  manager's address as `0.0.0.0` (typically the leader, when the swarm was
  created without `--advertise-addr`), so admin couldn't reach that node's
  agent: CPU/memory/disk usage stayed empty and console/logs for tasks there
  failed. The node's address now falls back to its manager address
  (`ManagerStatus.Addr`), which also fixes the Address column on the Nodes
  and node pages and the `docker swarm join` hint on the Swarm page.

## [1.2.4] - 2026-10-14

### Added

- **Tasks on each node.** A node's page lists the tasks scheduled on it
  (like `docker node ps`): service and slot, state, image, container,
  last update and error message, with Console and Logs buttons for
  running tasks. The Nodes list gets a Tasks column with the running-task
  count for each node, linking to that list.

## [1.2.3] - 2026-10-06

### Added

- **New service form** (Services → New service) - create a service without
  writing compose: name, optional stack, image, mode/replicas and every
  field of the advanced editor. **Clone** on a service's page opens it
  pre-filled from that service.
- **Stack edit, history and rollback.** Every successful deploy (deploy
  form, GitOps sync, rollback) stores the compose file and its variables
  as a numbered version (last 50 per stack; variables encrypted at rest).
  A stack's page gets **Edit** - the deploy form on the latest version, or
  on a compose file generated from the running services for stacks with
  no stored file - and a History table to open, download or roll back to
  any version.
- "Remove services not in the file" on the deploy form (prune), and the
  preview now lists such services.
- Much broader compose support: `entrypoint`, `user`, `working_dir`,
  `hostname`, `stop_grace_period`, `stop_signal`, `init`, `read_only`,
  `tty`, `cap_add`/`cap_drop`, `sysctls`, `ulimits`, `dns`, `extra_hosts`,
  `healthcheck`, `logging`; long syntax for ports (incl. `mode: host`),
  volumes (tmpfs, bind propagation, nocopy) and secrets/configs
  (`target`/`uid`/`gid`/`mode`); port ranges; network aliases;
  `deploy.endpoint_mode`, `rollback_config`, `placement.max_replicas_per_node`,
  `resources.limits.pids`, restart policy `delay`/`max_attempts`/`window`;
  top-level volume `driver`/`driver_opts` (NFS/CIFS) and config `content:`.
- `${VAR}` interpolation (all compose forms, incl. defaults, required
  variables and `$$`) from a new Variables field on the deploy form.
- Unsupported or misspelled compose keys are listed as warnings in the
  preview instead of being silently ignored.
- Custom templates: "Save as template" on the deploy form; delete from the
  gallery.
- Secrets and Configs pages show which services use each one; new detail
  pages (`/secrets/{id}`, `/configs/{id}` - the latter admin-only, as it
  shows the content) list every attaching service and its mount path.
- **Secret/config rotation**: create a new version, switch every service
  using the old one to it (mount paths unchanged), optionally delete the
  old one.
- Backup/restore and `rotate-cluster-secret` cover stack history and saved
  templates.

### Changed

- Compose `command:` now sets the image's CMD (container args), and the new
  `entrypoint:` sets its ENTRYPOINT - previously `command:` replaced the
  ENTRYPOINT, which broke images whose entrypoint script wraps the command
  (postgres, redis, nginx, ...). Redeploying an existing compose file
  shows this as a change in the preview.
- A secret/config attached from compose is mounted at its compose key by
  default (as `docker stack deploy` does), not at the underlying object's
  `name:` - so pointing `name:` at a rotated version keeps the path.
- Compose-deployed environment variables are applied in sorted order, so
  redeploying an unchanged file no longer restarts tasks just because Go
  map iteration reordered them; services are also applied in a stable
  (alphabetical) order.
- Stack/service export produces a complete, redeployable file: top-level
  networks/secrets/configs/volumes are declared, networks by name instead
  of ID, and the new fields above round-trip.
- The preview diff covers entrypoint/command, labels, ports, networks,
  secrets/configs, healthcheck and constraints, and mounts by content
  rather than count.

### Fixed

- Secret, config and network lookups by name (service editor and compose
  deploy) matched by prefix - `db` could silently resolve to `db_password`.
  They now require an exact name.

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
