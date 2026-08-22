# Contributing

## Development setup

```
go build -o bin/swarmdash ./cmd/swarmdash
```

You'll need a local Docker Swarm to exercise `admin`/`agent` against, and a
MongoDB instance for `admin`'s storage — see the "Running locally against a
test swarm" section in [README.md](README.md).

## Before opening a PR

```
go build ./...
go vet ./...
go test ./... -cover
```

CI runs the same three commands, plus a Docker image build, on every PR.

## Scope

swarmdash intentionally implements a pragmatic subset of Docker Compose and
Swarm's own API surface rather than every possible option — see "What's
implemented" and "Known gaps" in the README for what's deliberately left
out and why. If you're proposing a larger feature, especially one that adds
a new external dependency or a new persistent data type, open an issue
first so the design can be discussed before you invest time in an
implementation.

## Code style

Standard `gofmt`/`go vet` conventions. Comments should explain *why*, not
restate *what* the code already says — match the existing style in the
package you're editing.

## Reporting security issues

Do not open a public issue for a security vulnerability — see
[SECURITY.md](SECURITY.md).
