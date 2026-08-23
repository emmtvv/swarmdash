# Hardening (all opt-in)

- mTLS between admin and agent: `swarmdash tls init` (or `make tls`)
  generates a CA plus a shared agent server cert and admin client cert;
  point `--tls-cert/--tls-key/--tls-ca` (agent) and `--agent-tls-cert/
  --agent-tls-key/--agent-tls-ca` (admin) at them. Off by default — the
  cluster-secret bearer auth is enough on a trusted cluster network.
  Leaf certs are valid for 1y; renew them before they expire with
  `swarmdash tls renew` (or `make tls-renew`), which reissues agent/admin
  certs from the existing CA without touching the CA itself, so already-
  distributed `ca.crt` copies stay valid. Only re-run `tls init` for a new
  cluster — it mints a brand new CA and invalidates every certificate
  issued from the old one.
- HTTPS for the admin UI itself: `--ui-tls-cert`/`--ui-tls-key`.
- Storage: all admin state (users, sessions, audit log, tokens, registry
  credentials, gitops stacks, ...) persists through one of two backends,
  chosen with `--storage-driver`/`SWARMDASH_STORAGE_DRIVER`:
  - `local` (the default) stores everything in a SQLite database file
    (`swarmdash.db`, plus its `-wal`/`-shm` sidecar files while admin is
    running) under a directory set with `--data-dir`/`SWARMDASH_DATA_DIR`
    (defaults to `./data`) — no external database needed, and it's a single
    file to back up (`cp -r` the whole data dir, or `sqlite3 .backup` while
    it's running). The schema is applied automatically on first start and
    upgraded automatically by later swarmdash versions - see "Local storage
    schema migrations" below. The tradeoff: state lives on that one
    container/host's disk, so only a single admin replica may point at a
    given data directory at a time. [../deploy/stack.yml](../deploy/stack.yml)
    pins `SWARMDASH_STORAGE_DRIVER=mongo` explicitly rather than relying on
    this default, since it bundles its own `mongo` service.
  - `mongo` stores everything in MongoDB instead. Either set
    `--mongo-uri`/`SWARMDASH_MONGO_URI` directly (`_FILE` suffix also
    works, Docker-secret style), or leave it unset and configure the
    discrete parts it's built from instead — `--mongo-host`/
    `SWARMDASH_MONGO_HOST` (default `localhost`), `--mongo-port`/
    `SWARMDASH_MONGO_PORT` (default `27017`), `--mongo-username`/
    `SWARMDASH_MONGO_USERNAME`, `--mongo-password`/`SWARMDASH_MONGO_PASSWORD`
    (username/password both support the `_FILE` convention, so each can be
    its own Docker secret), `--mongo-auth-source`/
    `SWARMDASH_MONGO_AUTH_SOURCE` (defaults to `admin` once a username is
    set), and `--mongo-params`/`SWARMDASH_MONGO_PARAMS` for anything else
    (e.g. `replicaSet=rs0`). `--mongo-database`/`SWARMDASH_MONGO_DATABASE`
    (defaults to `swarmdash`) selects the working database either way. See
    [../deploy/stack.yml](../deploy/stack.yml) for the discrete-parts form in
    practice. Because state isn't pinned to a single admin container's
    local disk, running more than one admin replica for HA is just a
    matter of raising `deploy.replicas` — every replica talks to the same
    MongoDB deployment (and the reason to reach for `mongo` at all). Point
    all replicas at a MongoDB replica set (not a single standalone node) if
    you actually want MongoDB itself to be HA too.

## Local storage schema migrations

`--storage-driver local` applies its schema with plain, forward-only SQL
migration files embedded in the binary
([../internal/store/migrations](../internal/store/migrations)) — no separate
migration tool or manual step. Every time `admin` starts against a data
directory, it runs whichever `NNNN_description.sql` files haven't been
recorded yet, in order, each in its own transaction, and records them in a
`schema_migrations` table so they're never re-applied. Concretely, this
means:

- Upgrading swarmdash (new binary, same `--data-dir`) picks up any new
  migrations that version shipped automatically on next start - there's no
  separate `swarmdash migrate` command to remember to run.
- There are no down migrations. Rolling an upgrade back means restoring the
  data directory from a backup taken before the upgrade, not running the
  new binary's migrations in reverse - the usual tradeoff for a
  forward-only migration story, same as most embedded-SQLite apps.
- This only matters for `local`; MongoDB's collections are schemaless, so
  `--storage-driver mongo` has no equivalent migration step to run.
