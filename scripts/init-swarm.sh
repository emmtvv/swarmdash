#!/usr/bin/env bash
# Idempotent docker swarm init: no-op if this node is already in swarm mode,
# otherwise initializes it with a best-effort --advertise-addr. Shared by
# `make deploy` and `make up` (via scripts/up.sh) so both entry points work
# on a bare, non-swarm node.
set -euo pipefail

DOCKER="${DOCKER:-docker}"

detect_advertise_addr() {
	# Best-effort local IP for `docker swarm init --advertise-addr`. Falls
	# through silently on failure - `docker swarm init` still works without
	# it as long as the host has exactly one candidate interface, and prints
	# its own actionable error if it doesn't.
	if command -v ip >/dev/null 2>&1; then
		ip route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' | head -n1
	elif command -v ipconfig >/dev/null 2>&1; then
		ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null
	fi
}

state="$($DOCKER info --format '{{.Swarm.LocalNodeState}}' 2>/dev/null || true)"
if [ "$state" = "active" ]; then
	exit 0
fi

echo "not in swarm mode - running 'docker swarm init'"
addr="$(detect_advertise_addr || true)"
if [ -n "${addr:-}" ]; then
	$DOCKER swarm init --advertise-addr "$addr"
else
	$DOCKER swarm init
fi
