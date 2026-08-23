# Features

<p align="center">
  <img src="../assets/screenshots/services.png" width="32%" alt="Services">
  <img src="../assets/screenshots/topology.png" width="32%" alt="Cluster topology">
  <img src="../assets/screenshots/stacks.png" width="32%" alt="Stacks">
</p>
<p align="center">
  <img src="../assets/screenshots/service-detail.png" width="32%" alt="Service detail">
  <img src="../assets/screenshots/nodes.png" width="32%" alt="Nodes">
</p>

- **Stacks, services, tasks, nodes** — list/detail views, scale, update
  image, rollback, force restart, bulk actions, placement constraints.
- **Deploy from compose**, with a dry-run preview, a template gallery, and
  GitOps (point a stack at a git repo/branch and it stays in sync).
- **Web-based console** per task — exec shell (full TTY), live logs, live
  `docker stats`, a file browser, all proxied to the right node.
- **Live rollout view**, cluster topology graph, dashboard history, and a
  public unauthenticated `/status` page for uptime monitors.
- **Users & roles, SSO (OIDC)**, API tokens, audit log of every mutating
  action, CI/CD deploy webhooks.
- **Hardening, all opt-in**: mTLS between admin and agent, HTTPS for the
  UI, encrypted registry credentials, backup/restore — see
  [hardening.md](hardening.md).

## Full feature list

### Core
- Stacks / services / tasks / nodes: list + detail views.
- Service actions: scale, update image, force restart, rollback (Docker's
  native `Rollback: "previous"`), delete, plus an "advanced settings" editor
  for env vars, labels, mounts, placement constraints, CPU/memory
  limits/reservations, restart policy, and rolling-update policy
  (parallelism/delay/order/failure-action).
- Stack delete (removes every service with that stack label — Docker has
  no atomic stack-delete API) and stack-wide force-restart.
- **Deploy a stack from a compose file** (Stacks → Deploy stack): a
  pragmatic subset of the compose format — image, command, environment,
  labels, ports, volumes (short syntax), networks, deploy.replicas/mode/
  placement/resources/update_config/restart_policy. No `build:` (Swarm
  can't build images anyway), no local bind-mount resolution. Secrets/
  configs must already exist and are referenced with `external: true` —
  swarmdash never lets compose file content create a secret's value.
- Export any service, or a whole stack, back to compose YAML.
- **Dry-run preview** for compose deploys (Stacks → Deploy stack →
  Preview): shows create/update/unchanged per service and what would
  change (image, env, replicas, mounts, resources) before anything is
  touched.
- **Template gallery** (Stacks → Templates): built-in compose templates
  for common self-hosted services (Postgres, MySQL, Redis, MongoDB, nginx,
  Adminer) — "Use template" pre-fills the deploy form, same preview/submit
  flow as pasting compose by hand.
- **GitOps stack deploy** (Stacks → GitOps deploy): point a stack at a git
  repo/branch/compose path instead of pasting YAML. "Sync now" or a poll
  interval (1–60 min) re-fetches the compose file and re-applies it
  whenever the commit at that ref changes. Uses a pure-Go git client (no
  system `git` binary), optionally with an HTTPS access token for private
  repos (encrypted at rest, same as registry passwords).
- **CI/CD deploy webhooks** (on a service's detail page): mint a
  per-service webhook URL a CI pipeline can `POST` to (optionally with
  `{"image": "repo/name:tag"}`) to force a redeploy — no session or API
  token needed, the token in the URL is the credential. Only its hash is
  stored; the URL is shown once at creation, same convention as API
  tokens.
- **Bulk operations**: select multiple services on the Services page and
  restart/scale/delete them in one action.
- **Placement preferences** (spread over a label, e.g.
  `node.labels.zone`) alongside placement constraints, in both the
  service editor and compose deploy.
- **Node labels**: add/remove custom labels on a node's detail page
  (`/nodes/{id}`) for use in placement constraints/preferences.
- **Volumes**: per-node list/create/delete, same pattern as images (both
  are local to each node's daemon, not swarm-wide).
- **Swarm settings** (Settings → Swarm): cluster info, cert expiry, and
  the worker/manager join tokens with one-click rotation.
- **Rebalance** (Settings → Swarm): force-restarts every replicated
  service so Swarm re-places their tasks across all current nodes — useful
  after adding/removing nodes, since Swarm only weighs node load for *new*
  task placement and never moves an already-running task on its own.
  Global-mode services are skipped (already one task per eligible node
  automatically).
- Live rollout view: service detail page streams task state over SSE,
  polling the Docker API every 1.5s (Docker doesn't push task-state
  changes, so this mirrors what `docker service ps --watch` effectively
  does).
- Web-based console: exec a shell in any running task's container
  (xterm.js, full TTY + resize), live log tail, live `docker stats`, a
  directory browser + file download, and a log-file download — all
  proxied through admin to the right node's agent, with the browser-facing
  websockets auto-reconnecting on drop.
- Networks / Secrets / Configs: list, create, delete.
- Node actions: activate / pause / drain, promote / demote.
- Images: per-node list + prune (dangling, or `all` for every unused
  image), since images live on each node's daemon rather than swarm-wide.
- Registry credentials: stored encrypted (AES-256-GCM, keyed off the
  cluster secret) and applied automatically to service create/update/
  compose-deploy when the image's registry matches one you've configured.
- Audit log of every mutating action (who, what, when, success/failure).
- API tokens (Settings → API tokens) for CI/CD: `Authorization: Bearer
  <token>` works on every endpoint alongside the browser session cookie.
- Sortable/filterable tables (client-side, no reload) on the services,
  nodes, and per-service task lists.
- Auth: user store in MongoDB (see below), bcrypt passwords, session
  cookies. First run bootstraps an `admin` user.
- **Users & roles** (Settings → Users): create additional accounts with
  role `admin` (full access) or `viewer` (read-only - no mutating
  actions, no console access, Settings pages hidden/blocked). API tokens
  (above) carry the same role; tokens created before roles existed have
  no stored role and are treated as `admin`. Enforced in one place
  (`requireRole` in `internal/admin/auth.go`, wrapped around every
  protected route) rather than per-handler, so no route can be missed.
  Guard rails: you can't delete your own account or the last remaining
  admin.
- **Single sign-on** (Settings → SSO): generic OIDC (Authorization Code +
  PKCE against any provider that publishes a
  `/.well-known/openid-configuration` document - Okta, Authentik, Keycloak,
  Google Workspace, Azure AD, Auth0, ...). Local username/password login
  stays available alongside it by default. Accounts can auto-provision on
  first login (with a configurable default role and an optional
  email-domain allowlist) or be pre-created from Settings → Users as an
  "SSO account" with no local password. An "Require SSO" switch on the
  same page turns local login off entirely - both the form on `/login`
  and `POST /login` itself, not just a hidden button - leaving the "Sign
  in with ..." button as the only way in; see [known-gaps.md](known-gaps.md)
  for the lockout risk that comes with it. Not SAML - see
  [known-gaps.md](known-gaps.md).
- **Backup & restore** (Settings → Backup): download users, roles, API
  tokens, webhooks, registry credentials, GitOps stacks and SSO config as
  one JSON file; restore merges it back in (creates/overwrites by ID,
  never deletes). Deliberately scoped to configuration, not history - the
  audit log and metrics samples aren't included. Encrypted fields
  (registry passwords, GitOps tokens, the SSO client secret) travel as
  ciphertext and only decrypt again on an admin instance using the same
  `--cluster-secret` the backup was taken with; restore flags anything it
  can't decrypt instead of silently importing it.

### Observability
- A background poller (every 10s - Docker doesn't push any of this, same
  reasoning as the SSE rollout view) watches for:
  - **Task state transitions** → durable history at Task events (`/events`,
    filterable by service), so "what happened at 3am" has an answer after
    the live view has moved on.
  - **Services stuck below their desired replica count for 2+ minutes**
    and **nodes going down/recovering** → fire configured **alert
    webhooks** (Settings → Webhooks: name + URL, POSTed a JSON
    `{event, target, message, time}` body) and record an entry in the
    audit log either way.
- **Metrics sparklines**: the console's Stats tab renders live CPU%/
  memory% line charts (plain `<canvas>`, no charting library) computed
  from the same stats stream already used for the raw JSON view.
- **Dashboard history**: the same poller samples cluster-wide task/service/
  node health and CPU/memory *reservation* (what the scheduler accounts
  against node capacity, not live per-container usage — that would need a
  cluster-wide stats collector, a bigger separate feature) roughly once a
  minute, kept for 14 days. The dashboard's "Last 24h" section renders it
  as plain-canvas line charts, same convention as the console sparklines.
- **Public status page** at `/status` — deliberately unauthenticated (for
  uptime monitors / status pages) and deliberately minimal: aggregate
  node/service counts only, nothing that identifies specific
  nodes/images/services by name.

### Always on
- **CSRF protection**: every mutating request from a browser session must
  echo back a per-browser token (double-submit cookie), checked in one
  place (`security` middleware in `internal/admin/security.go`) wrapped
  around the whole app, not per-handler. Plain `<form method="post">`
  elements get the token injected automatically by `app.js` from a
  `<meta>` tag - page templates don't wire it up individually. API tokens
  (bearer auth) and the CI deploy-hook endpoint are exempt: neither relies
  on a browser cookie, so neither can be forged cross-site the way this
  protects against.
- **Security headers** on every response: `X-Content-Type-Options: nosniff`,
  `X-Frame-Options: DENY`, `Referrer-Policy: same-origin`,
  `Permissions-Policy` (denies geolocation/camera/mic/etc, all unused),
  `Strict-Transport-Security` when serving over TLS, and a
  `Content-Security-Policy` scoped to the app's own same-origin assets
  (`default-src 'self'`) with per-response nonces allow-listing the
  handful of inline `<script>` blocks that render server-side chart/graph
  data, instead of blanket-allowing inline script.
- **`GET /health`**: unauthenticated liveness/readiness probe for a Docker
  `HEALTHCHECK`, a Swarm service healthcheck, or an external uptime
  check. Unlike `/status` it actually exercises MongoDB and the local
  Docker daemon (`200` + `{"status":"ok",...}`, or `503` naming whichever
  dependency failed) rather than just confirming the HTTP server accepts
  connections.
- **Login lockout**: 5 failed local-password attempts for a username within
  15 minutes locks it out for 15 minutes, tracked in MongoDB so it holds
  regardless of which admin replica a guess lands on. Applies to unknown
  usernames too, so it doesn't double as a way to enumerate valid accounts.

See [hardening.md](hardening.md) for the opt-in hardening features
(mTLS, HTTPS, storage backends, schema migrations).
