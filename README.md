<p align="center">
  <img src="assets/banner.svg" alt="swarmdash" width="720">
</p>

<p align="center">
  <a href="https://github.com/emmtvv/swarmdash/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/emmtvv/swarmdash/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://hub.docker.com/r/mathwave/swarmdash"><img alt="Docker Pulls" src="https://img.shields.io/docker/pulls/mathwave/swarmdash"></a>
  <a href="go.mod"><img alt="Go version" src="https://img.shields.io/badge/go-1.25-00ADD8?logo=go&logoColor=white"></a>
  <a href="LICENSE"><img alt="License" src="https://img.shields.io/github/license/emmtvv/swarmdash"></a>
</p>

<p align="center">
  <b><a href="https://swarmdash.app">swarmdash.app</a></b> &nbsp;·&nbsp;
  <b><a href="https://hub.docker.com/r/mathwave/swarmdash">Docker Hub</a></b> &nbsp;·&nbsp;
  <b><a href="docs/quickstart.md">Quickstart</a></b> &nbsp;·&nbsp;
  <b><a href="docs/features.md">Features</a></b> &nbsp;·&nbsp;
  <b><a href="docs">Docs</a></b>
</p>

<p align="center">
  One Go binary, no external database — the admin panel Docker Swarm doesn't<br>
  ship with: stacks, services, nodes, a web shell into any container, live<br>
  rollouts, and RBAC/SSO.
</p>

<p align="center">
  <img src="assets/screenshots/dashboard.png" width="49%" alt="Dashboard">
  <img src="assets/screenshots/console.png" width="49%" alt="Web-based console">
</p>

`swarmdash` is a single binary with two modes: `admin` runs on a manager
node and serves the UI; `agent` runs on every node and gives `admin` a way
to reach that node's Docker daemon for exec/logs/stats. State (users,
sessions, audit log, tokens, ...) lives in an embedded SQLite database by
default — no MongoDB or other external service to stand up first (swap in
MongoDB later with `--storage-driver mongo` if you need more than one
`admin` replica). Point it at your swarm, get a dashboard, in under five
minutes:

```
docker swarm init   # skip if this node is already in swarm mode

export SWARMDASH_CLUSTER_SECRET=$(openssl rand -hex 32)

docker run -d --name swarmdash-agent --network host \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -e SWARMDASH_CLUSTER_SECRET \
  mathwave/swarmdash agent

docker run -d --name swarmdash-admin -p 8870:8870 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -e SWARMDASH_CLUSTER_SECRET \
  mathwave/swarmdash admin

docker logs swarmdash-admin   # grab the generated bootstrap admin password
```

Open http://localhost:8870. That's the published image on a single node
for a quick look; see [docs/quickstart.md](docs/quickstart.md) for a real
multi-node deploy with persistent storage (`make deploy`), building from
source, and running locally against a test swarm.

## swarmdash vs. Portainer CE vs. Swarmpit

|  | swarmdash | Portainer CE | Swarmpit |
|---|---|---|---|
| License / cost | Apache-2.0, free | free CE / paid BE | open source, free |
| RBAC (admin/viewer roles) | ✅ free | 🔒 Business Edition only | basic |
| SSO (OIDC) | ✅ free | 🔒 Business Edition only | ❌ |
| GitOps auto-sync (poll/webhook) | ✅ free | 🔒 Business Edition only | ❌ |

See [docs/comparison.md](docs/comparison.md) for the full table and caveats.

## Documentation

- [Quickstart](docs/quickstart.md) — build, run locally, deploy on a cluster
- [Features](docs/features.md) — full feature list
- [Architecture](docs/architecture.md) — why the agent/admin split
- [Hardening](docs/hardening.md) — mTLS, HTTPS, storage backends, migrations
- [Comparison](docs/comparison.md) — swarmdash vs. Portainer CE vs. Swarmpit
- [Known gaps / next steps](docs/known-gaps.md)
- [Testing](docs/testing.md)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Bug reports and feature requests:
[open an issue](https://github.com/emmtvv/swarmdash/issues). This project
follows the [Code of Conduct](CODE_OF_CONDUCT.md).

## Security

See [SECURITY.md](SECURITY.md) for how to report a vulnerability privately.

## License

[Apache License 2.0](LICENSE). See [NOTICE](NOTICE) for bundled
third-party JS attributions.
