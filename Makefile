VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X swarmdash/internal/version.Version=$(VERSION) -X swarmdash/internal/version.Commit=$(COMMIT)

# Overridable so `make up` can force `sudo docker` for the rest of a run
# right after installing Docker, before group membership picks up - see
# scripts/up.sh.
DOCKER ?= docker

.PHONY: build build-linux docker test test-cover vet env cluster-secret tls tls-renew deploy down up

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/swarmdash ./cmd/swarmdash

build-linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/swarmdash-linux-amd64 ./cmd/swarmdash

docker:
	$(DOCKER) build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t swarmdash:$(VERSION) .

vet:
	go vet ./...

test:
	go test ./...

# Coverage for the unit-tested packages (pure logic - compose parsing/diff/
# export, crypto, pagination, pki, secretenv, gitfetch, store). Excludes
# internal/admin's HTTP handlers, internal/agent, and cmd/swarmdash, which
# need a real Docker daemon (and, for --storage-driver=mongo, MongoDB) and
# are exercised by hand / in CI's integration step instead - see README's
# Testing section.
test-cover:
	go test ./... -cover

# Generates .env with a random cluster secret, in the plain-environment-
# variable form deploy/stack.yml expects (SWARMDASH_CLUSTER_SECRET - see
# deploy/stack.yml's top comment for why it's an env var rather than a
# Docker secret). Storage defaults to a SQLite database on a Docker volume,
# no credentials needed; see deploy/stack.yml's "Optional: HA admin"
# comment if you want to switch to an external MongoDB deployment instead.
# Safe to re-run: leaves an existing .env untouched. To rotate a value, edit
# .env by hand and redeploy - unlike Docker secrets these aren't immutable.
env:
	@if [ -f .env ]; then \
		echo ".env already exists - edit it by hand to rotate a value, then redeploy:"; \
		echo "  \$$EDITOR .env && make deploy"; \
	else \
		{ \
			echo "SWARMDASH_CLUSTER_SECRET=$$(openssl rand -hex 32)"; \
			echo "SWARMDASH_ADMIN_PASSWORD="; \
		} > .env; \
		echo "generated .env"; \
	fi

# Loads the cluster secret as a Docker secret instead of the plain env var
# `make env`/deploy/stack.yml use by default (see the top-of-file comment
# in deploy/stack.yml for that default's tradeoff). Reuses the value
# already in .env if `make env` has been run, so switching delivery
# mechanisms doesn't also rotate the secret and break already-deployed
# agents; generates a fresh one otherwise. A no-op if the Docker secret
# already exists - Docker secrets are immutable, so to rotate, create a new
# secret under a new name, update deploy/stack.yml's reference, redeploy,
# then remove the old secret (run `swarmdash rotate-cluster-secret` first -
# see docs/hardening.md - so encrypted registry/GitOps/SSO credentials
# aren't left undecryptable).
cluster-secret:
	@if [ -f .env ] && grep -q '^SWARMDASH_CLUSTER_SECRET=' .env; then \
		value="$$(grep '^SWARMDASH_CLUSTER_SECRET=' .env | cut -d= -f2-)"; \
	else \
		value="$$(openssl rand -hex 32)"; \
	fi; \
	if $(DOCKER) secret inspect swarmdash_cluster_secret >/dev/null 2>&1; then \
		echo "swarmdash_cluster_secret already exists, skipping"; \
	else \
		printf '%s' "$$value" | $(DOCKER) secret create swarmdash_cluster_secret - >/dev/null; \
		echo "created docker secret: swarmdash_cluster_secret"; \
	fi
	@echo ""
	@echo "enable it by uncommenting the cluster-secret sections in deploy/stack.yml, then redeploy"

# Generates the mTLS CA + agent/admin certificates (optional hardening on
# top of the cluster secret, see README) and loads them as Docker secrets -
# certs are files, so they stay Docker secrets even though the cluster
# secret/Mongo credentials above moved to .env. Re-running after the
# secrets already exist is a no-op per-secret; to rotate, remove the
# swarmdash_tls_* secrets first.
tls: docker
	@mkdir -p ./certs
	$(DOCKER) run --rm -v "$(CURDIR)/certs:/certs" swarmdash:latest tls init --out /certs
	@for f in ca.crt agent.crt agent.key admin.crt admin.key; do \
		name="swarmdash_tls_$$(echo $$f | tr '.' '_')"; \
		if $(DOCKER) secret inspect $$name >/dev/null 2>&1; then \
			echo "$$name already exists, skipping"; \
		else \
			$(DOCKER) secret create $$name ./certs/$$f >/dev/null; \
			echo "created docker secret: $$name"; \
		fi; \
	done
	@echo ""
	@echo "enable mTLS by uncommenting the tls_* sections in deploy/stack.yml, then redeploy"

# Reissues agent/admin certificates (fresh 1y validity) from the CA already
# in ./certs, without touching the CA itself - so unlike re-running `make
# tls`/`tls init`, this doesn't invalidate certs other nodes still trust.
# Docker secrets are immutable once created, so this only refreshes the
# local ./certs files; to actually roll the change out, create new secrets
# under new names (e.g. swarmdash_tls_agent_crt_v2), point the tls_* mounts
# in deploy/stack.yml at them, redeploy, then remove the old secrets.
tls-renew: docker
	$(DOCKER) run --rm -v "$(CURDIR)/certs:/certs" swarmdash:latest tls renew --out /certs
	@echo ""
	@echo "renewed ./certs/{agent,admin}.{crt,key} - CA unchanged."
	@echo "Docker secrets are immutable: create new ones from these files, update"
	@echo "deploy/stack.yml's tls_* secret references, redeploy, then remove the old secrets."

# Bring-up on a fresh Swarm cluster: build the image, generate .env with the
# cluster secret if it doesn't exist yet, initialize swarm mode if this node
# isn't in it yet, and deploy the full stack (admin on a manager, agent on
# every node). Run this on a node with Docker already installed - if Docker
# itself isn't installed yet, use `make up` instead, which wraps this after
# handling that step.
deploy: env
	DOCKER="$(DOCKER)" scripts/init-swarm.sh
	$(DOCKER) build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t swarmdash:latest .
	@# docker stack deploy doesn't read .env on its own (unlike docker
	@# compose) - export it into this recipe line's shell first.
	set -a; . ./.env; set +a; $(DOCKER) stack deploy -c deploy/stack.yml swarmdash
	@# Swarm only recreates tasks when it sees a new image digest, which it
	@# can't resolve for a locally-built, unpushed `latest` tag - so a
	@# rebuild-and-redeploy with the same tag would otherwise leave the old
	@# containers running. Force-updating makes redeploys actually take on
	@# a single-node/dev setup like this. On a real multi-node cluster, push
	@# the image to a registry all nodes can pull from instead.
	$(DOCKER) service update --force --quiet swarmdash_admin >/dev/null
	$(DOCKER) service update --force --quiet swarmdash_agent >/dev/null
	@echo ""
	@echo "deployed - waiting a moment before checking status..."
	@sleep 3
	@$(DOCKER) service ls --filter label=com.docker.stack.namespace=swarmdash
	@echo ""
	@echo "tail the admin logs to grab the generated bootstrap password:"
	@echo "  docker service logs -f swarmdash_admin"

# Tears the stack down. Leaves .env and the swarmdash_data volume in
# place, so a re-deploy picks up existing credentials/users/sessions/etc.
down:
	$(DOCKER) stack rm swarmdash

# The one-liner quickstart: installs Docker if it's missing, runs `docker
# swarm init` if this node isn't already in swarm mode, then `deploy` (above),
# and finally prints the URL the panel is reachable at plus the generated
# bootstrap admin credentials pulled from the admin service's own logs. See
# scripts/up.sh. Safe to re-run - every step it wraps already is.
up:
	@bash scripts/up.sh
