# Changelog

Notable changes to swarmdash are tracked here, following
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). This project
does not yet follow strict semantic versioning across releases — see the
README's "Known gaps" section for what's still evolving.

## [Unreleased]

## [1.0.1] - 2026-08-22

### Fixed

- Node picker on the Volumes and Images pages, and the Reset password
  action on the Users settings page, were using inline event handlers
  (`onchange=`/`onsubmit=`) that violate the app's Content-Security-Policy
  and get blocked by browsers. Moved this behavior into `app.js`.

## [1.0.0] - 2026-08-22

- First public release.
