#!/usr/bin/env bash
# One-shot bring-up: install Docker if it's missing, then build+deploy
# swarmdash (which initializes swarm mode itself if needed - see
# scripts/init-swarm.sh) and print the URL (and bootstrap admin credentials)
# once the panel is reachable. Invoked via `make up` - see the Makefile for
# the other, more surgical targets this wraps (`env`, `deploy`).
set -euo pipefail

# docker commands below run through $DOCKER so a fresh install (whose group
# membership hasn't been picked up by this shell yet) still works without
# forcing a logout/login before the rest of the script can proceed.
DOCKER="docker"

log() { printf '\n==> %s\n' "$1"; }

install_docker() {
	if command -v docker >/dev/null 2>&1; then
		log "docker already installed ($(docker --version))"
		return
	fi

	case "$(uname -s)" in
	Linux)
		log "docker not found, installing via get.docker.com"
		curl -fsSL https://get.docker.com | sh
		if ! docker info >/dev/null 2>&1; then
			if [ "$(id -u)" -ne 0 ]; then
				sudo usermod -aG docker "$(id -un)" || true
			fi
			if ! docker info >/dev/null 2>&1; then
				# Group membership above only takes effect in a new login
				# session. Use sudo for the rest of *this* run so a single
				# `make up` still finishes end to end; the printed banner at
				# the end tells the user to re-login for passwordless
				# `docker` afterwards.
				DOCKER="sudo docker"
				echo "using 'sudo docker' for this run - log out and back in (or run 'newgrp docker') to use docker without sudo afterwards"
			fi
		fi
		;;
	Darwin)
		echo "docker not found. Install Docker Desktop for Mac, start it, then re-run 'make up':" >&2
		echo "  https://www.docker.com/products/docker-desktop" >&2
		exit 1
		;;
	*)
		echo "docker not found and this OS isn't supported for automatic install." >&2
		echo "Install Docker manually, then re-run 'make up': https://docs.docker.com/get-docker/" >&2
		exit 1
		;;
	esac
}

wait_for_bootstrap_credentials() {
	# The generated admin password is only ever printed once, to the admin
	# service's own logs (see internal/admin/auth.go) - never stored in
	# plaintext anywhere. Tail those logs briefly to surface it here instead
	# of making the user go find it themselves right after a fresh deploy.
	log "waiting for swarmdash_admin to report its bootstrap credentials"
	for _ in $(seq 1 30); do
		line="$($DOCKER service logs swarmdash_admin 2>/dev/null | grep 'generated bootstrap admin credentials' | tail -n1 || true)"
		if [ -n "$line" ]; then
			echo "$line"
			return
		fi
		sleep 1
	done
	echo "no bootstrap-credentials line seen yet - either SWARMDASH_ADMIN_PASSWORD is already set in .env, or the service is still starting (check: docker service logs swarmdash_admin)"
}

install_docker

log "building and deploying swarmdash"
DOCKER="$DOCKER" make deploy

wait_for_bootstrap_credentials

addr="$($DOCKER info --format '{{.Swarm.NodeAddr}}' 2>/dev/null || true)"
addr="${addr:-localhost}"
log "swarmdash is up: http://${addr}:8870"
