# Changelog

Notable changes to swarmdash are tracked here, following
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). This project
does not yet follow strict semantic versioning across releases — see the
README's "Known gaps" section for what's still evolving.

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
