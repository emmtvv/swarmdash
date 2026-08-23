# Testing

```
go test ./... -cover
```

Unit tests cover the pure logic: compose file parsing/diff/export, the
registry-credential encryption, pagination, mTLS CA/cert generation, and
secret-env resolution. `internal/admin`'s HTTP handlers, `internal/agent`,
and `cmd/swarmdash` talk to a real Docker daemon and MongoDB, so those are
exercised by running the real thing (`make up`) rather than mocked.

CI ([../.github/workflows/ci.yml](../.github/workflows/ci.yml)) runs the above
plus `gofmt -l`, `go vet`, and
[golangci-lint](https://github.com/golangci/golangci-lint) on every push and
PR, and a non-blocking [`govulncheck`](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck)
scan of dependencies (see the workflow for why it's informational rather
than a hard gate right now). Dependabot keeps Go modules, GitHub Actions,
and the Dockerfile's base images on weekly update PRs.

## Docker Engine compatibility

None of the above touches swarm mode or a real Docker daemon at all, so it
can't catch a real Docker Engine upgrade breaking Swarm - which has
happened: Docker 29 shipped Swarm-specific regressions (internal DNS
resolution, legacy volume plugins) that a lot of existing clusters hit on
upgrade. The `swarm-engine-compat` CI job covers that gap: it pins the
runner to specific real engine versions (currently the last 28.x and the
latest 29.x - see the job for exactly which) via
[`docker/setup-docker-action`](https://github.com/docker/setup-docker-action),
initializes swarm mode, starts swarmdash's own `agent`/`admin` against it,
and confirms a plain `docker service create` actually reaches `Running`
through the scheduler. It's a smoke test, not full coverage - the versions
in its matrix need bumping by hand as new engine releases ship.
