# Security Policy

swarmdash holds Docker socket access and swarm-wide credentials by design —
take vulnerability reports seriously here.

## Reporting a vulnerability

Please do **not** open a public GitHub issue for a security vulnerability.

Instead, use GitHub's private reporting flow:
[Security → Report a vulnerability](https://github.com/emmtvv/swarmdash/security/advisories/new)
on this repository. This opens a private advisory visible only to the
maintainer until a fix is ready.

Include, if known: the affected version/commit, an impact assessment, and
reproduction steps.

## Supported versions

swarmdash does not yet maintain parallel release branches — security fixes
land on `main` and the latest tagged release. Run the latest version to
get fixes.

## Scope

In scope: the `admin` and `agent` binaries, the deploy tooling in
`deploy/`/`Makefile`/`scripts/`, and the published `emmtvv/swarmdash`
Docker image.

Out of scope: vulnerabilities in third-party dependencies (report those
upstream — `go.mod` pins the versions in use here) and issues that require
an attacker to already have Docker socket or cluster-secret access (that
level of access is equivalent to full cluster control regardless of
swarmdash).
