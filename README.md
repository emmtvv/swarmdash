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
  The admin panel Docker Swarm doesn't ship with — stacks, services, nodes,<br>
  a web shell into any container, live rollouts, and RBAC/SSO, in one Go binary.
</p>

<p align="center">
  <img src="assets/screenshots/dashboard.png" width="49%" alt="Dashboard">
  <img src="assets/screenshots/console.png" width="49%" alt="Web-based console">
</p>

`swarmdash` is a single binary with two modes: `admin` runs on a manager
node and serves the UI; `agent` runs on every node and gives `admin` a way
to reach that node's Docker daemon for exec/logs/stats. Point it at your
swarm, get a dashboard, in under five minutes.

```
git clone https://github.com/emmtvv/swarmdash.git
cd swarmdash
make up
```

See [docs/quickstart.md](docs/quickstart.md) for building from source,
running locally, and deploying on a real cluster.

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
