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
