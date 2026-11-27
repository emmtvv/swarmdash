# swarmdash

**The admin panel Docker Swarm doesn't ship with.** One Go binary, no external database: stacks, services, nodes, a web shell into any container, live rollouts, RBAC and SSO.

- 🌐 Website: https://swarmdash.app
- 📦 Source code: https://github.com/emmtvv/swarmdash
- 📖 Docs: https://github.com/emmtvv/swarmdash/tree/main/docs
- 🐛 Issues: https://github.com/emmtvv/swarmdash/issues
- 💬 Support chat (Telegram): https://t.me/swarmdash_support_bot
- ⚖️ License: Apache-2.0

![Dashboard](https://raw.githubusercontent.com/emmtvv/swarmdash/main/assets/screenshots/dashboard.png)

## What it is

The image runs in one of two modes:

- **`admin`** runs on a manager node and serves the web UI on port `8870`.
- **`agent`** runs on every node and gives `admin` access to that node's Docker daemon for exec, logs and stats (port `8871`, host network).

State (users, sessions, audit log, tokens) is kept in an embedded SQLite database, so there is nothing else to set up first. If you need more than one `admin` replica, you can switch to MongoDB later.

## Features

- **Stacks, services, tasks, nodes**: scale, update image, roll back, force restart, bulk actions, placement constraints
- **Deploy from compose** or from a form, with a dry-run preview, stack history and rollback, a template gallery and **GitOps** auto-sync
- **Web console** for any task: full-TTY shell, live logs, live `docker stats`, a file browser
- **Image updates**: one click to the newest patch, minor or major tag, or automatic updates
- **Secrets & configs**: see which services use each one, and rotate them in one step
- **Users & roles, SSO (OIDC), 2FA (TOTP)**, API tokens, an audit log, CI/CD deploy webhooks
- Live rollout view, cluster topology, volume backup/restore, a public `/status` page, and a UI in 9 languages
- Opt-in hardening: mTLS between admin and agent, HTTPS, encrypted registry credentials

## Quick start (single node)

```sh
docker swarm init   # skip if this node is already in swarm mode

export SWARMDASH_CLUSTER_SECRET=$(openssl rand -hex 32)

docker run -d --name swarmdash-agent --network host \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v swarmdash-backups:/var/lib/swarmdash/backups \
  -e SWARMDASH_CLUSTER_SECRET \
  emmtvv/swarmdash agent

docker run -d --name swarmdash-admin -p 8870:8870 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -e SWARMDASH_CLUSTER_SECRET \
  emmtvv/swarmdash admin

docker logs swarmdash-admin   # copy the generated admin password
```

Open http://localhost:8870 and log in as `admin` with the password from the logs. You'll be asked to set a new password the first time you log in.

This setup is only for a quick look. It doesn't store state on a volume. For a multi-node cluster, deploy it as a stack: `agent` as a global service and `admin` pinned to a manager with a data volume. The [Quickstart guide](https://github.com/emmtvv/swarmdash/blob/main/docs/quickstart.md) covers this, and a ready-made [`deploy/stack.yml`](https://github.com/emmtvv/swarmdash/blob/main/deploy/stack.yml) is in the repo.

## Configuration

| Variable | Used by | Description |
|---|---|---|
| `SWARMDASH_CLUSTER_SECRET` ¹ | admin, agent | **Required.** Shared secret between admin and agents. Must be the same on every node. |
| `SWARMDASH_DATA_DIR` | admin | Where the SQLite database is stored. Mount a volume here to keep state. |
| `SWARMDASH_ADMIN_USERNAME` ¹ | admin | Username of the first admin account (default `admin`). |
| `SWARMDASH_ADMIN_PASSWORD` ¹ | admin | Sets the first admin password instead of generating one. |
| `SWARMDASH_STORAGE_DRIVER` | admin | `local` (SQLite, default) or `mongo`. |

### MongoDB (`SWARMDASH_STORAGE_DRIVER=mongo`)

Use MongoDB when you want more than one `admin` replica: every replica connects to the same database. swarmdash doesn't bundle MongoDB, so point it at your own deployment. You can pass a full connection string, or set the host, port and credentials separately.

| Variable | Default | Description |
|---|---|---|
| `SWARMDASH_MONGO_URI` ¹ | | Full connection string, e.g. `mongodb://user:pass@mongo:27017/?replicaSet=rs0`. If set, the `HOST`, `PORT`, `USERNAME`, `PASSWORD`, `AUTH_SOURCE` and `PARAMS` variables below are ignored. |
| `SWARMDASH_MONGO_HOST` | `localhost` | MongoDB host. |
| `SWARMDASH_MONGO_PORT` | `27017` | MongoDB port. |
| `SWARMDASH_MONGO_USERNAME` ¹ | | Username. |
| `SWARMDASH_MONGO_PASSWORD` ¹ | | Password. |
| `SWARMDASH_MONGO_AUTH_SOURCE` | `admin` if a username is set | `authSource` database. |
| `SWARMDASH_MONGO_PARAMS` | | Extra connection string parameters, e.g. `replicaSet=rs0&tls=true`. |
| `SWARMDASH_MONGO_DATABASE` | `swarmdash` | Database name. Used with either form. |

¹ Also accepts a `_FILE` variant (for example `SWARMDASH_CLUSTER_SECRET_FILE=/run/secrets/cluster_secret`), so you can pass the value as a Docker secret.

For mTLS and HTTPS, see [Hardening](https://github.com/emmtvv/swarmdash/blob/main/docs/hardening.md).

## Tags

- `latest`: the newest release
- `X.Y.Z` (e.g. `1.4.1`): an exact version
- `X.Y` (e.g. `1.4`): the newest patch release in that minor version

Images are built for `linux/amd64` and `linux/arm64` from the [`Dockerfile`](https://github.com/emmtvv/swarmdash/blob/main/Dockerfile) in the repo by [GitHub Actions](https://github.com/emmtvv/swarmdash/actions) whenever a version tag is pushed. Release notes are in [CHANGELOG.md](https://github.com/emmtvv/swarmdash/blob/main/CHANGELOG.md).

## Security note

Both modes need `/var/run/docker.sock`, which gives full control over the Docker host. Only expose port `8870` on networks you trust, or put it behind HTTPS. Report vulnerabilities privately as described in [SECURITY.md](https://github.com/emmtvv/swarmdash/blob/main/SECURITY.md).
