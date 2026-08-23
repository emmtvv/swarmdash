# Quickstart

On any Linux machine (installs Docker if it's missing, initializes swarm
mode if this node isn't in it yet, builds the image, deploys the stack, and
prints the panel's URL plus the generated bootstrap admin credentials):

```
git clone https://github.com/emmtvv/swarmdash.git
cd swarmdash
make up
```

Already have a Docker Swarm cluster running? Skip straight to
[Deploying on a real cluster](#deploying-on-a-real-cluster) below (`make
deploy`) instead — `make up` is specifically the from-a-bare-machine path.

Want to pin the bootstrap admin password yourself instead of reading a
generated one out of the logs? Set `SWARMDASH_ADMIN_PASSWORD` in `.env`
before running `make up`/`make deploy` — see `.env.example`.

Prefer the published image over building locally? `docker pull
mathwave/swarmdash` — edit the `image:` lines in
[../deploy/stack.yml](../deploy/stack.yml) to `mathwave/swarmdash:latest` before
running `make deploy` (skips the local `docker build` step, useful on
multi-node clusters where you'd otherwise have to build on every node).

## Building

```
go build -o bin/swarmdash ./cmd/swarmdash
# or:
make build          # local binary
make build-linux     # linux/amd64 for the cluster
make docker          # swarmdash:dev image
```

## Running locally against a test swarm

```
docker swarm init   # if not already in swarm mode

export SWARMDASH_CLUSTER_SECRET=dev-secret
./bin/swarmdash agent --listen :8871 &
./bin/swarmdash admin --listen :8870 &
```

`admin` needs somewhere to persist its own state (users, sessions, audit
log, tokens, etc. - see Storage in [hardening.md](hardening.md)). By default
(`--storage-driver local`, no setup needed) that's a local SQLite database
under `./data`; pass `--storage-driver mongo` /
`SWARMDASH_STORAGE_DRIVER=mongo` instead to point at a MongoDB deployment
(`SWARMDASH_MONGO_URI=mongodb://localhost:27017`, with a single-node
`docker run -p 27017:27017 mongo:7` enough for local testing) - needed if
you want to run more than one admin replica.

The first admin start prints a generated bootstrap password to stdout
(unless `SWARMDASH_ADMIN_PASSWORD` / `--bootstrap-password` is set; the
username is `admin` unless `SWARMDASH_ADMIN_USERNAME` / `--bootstrap-username`
is set). Open http://localhost:8870. Either way, that first login forces you
to set your own password before anything else is reachable - see
"Change password" under the user menu once you're past that.

## Deploying on a real cluster

See [../deploy/stack.yml](../deploy/stack.yml): `agent` runs `mode: global`
attached to the pre-existing `host` network (so it's reachable at each
node's real address — plain `network_mode: host` is silently ignored by
`docker stack deploy`, it only works for docker-compose/docker run, so the
external-network attach is the way to get real host networking for a Swarm
service), and `admin` runs pinned to a manager with its state (users,
sessions, audit log, tokens, ...) in a local SQLite database on its own
Docker volume — swap `--storage-driver` to `mongo` and point it at an
external MongoDB deployment instead if you want more than one admin replica
(see the "Optional: HA admin" comment in `deploy/stack.yml`).
`admin`/`agent` mount `/var/run/docker.sock` and share the cluster secret
via a plain environment variable (`SWARMDASH_CLUSTER_SECRET`, interpolated
from a `.env` file — not a Docker secret, see the comment at the top of
`deploy/stack.yml` for the tradeoff).

From a manager node that's already in swarm mode, with this repo checked
out (use [`make up`](#quickstart) instead if this node isn't in swarm mode
yet, or doesn't have Docker installed at all):

```
make deploy
```

That builds the image, generates `.env` with a random cluster secret if it
doesn't already exist (`make env` on its own does just that step — see
`.env.example` for the format), and runs `docker stack deploy` with those
values exported into its environment (`docker stack deploy` doesn't read
`.env` itself the way `docker compose` does). Then:

```
docker service logs -f swarmdash_admin   # grab the generated admin password
```

(skip this if you already set `SWARMDASH_ADMIN_PASSWORD` in `.env` — then
that's the password, nothing gets generated).

To rotate a value, edit `.env` by hand and run `make deploy` again — unlike
Docker secrets these aren't immutable. Deploying without the Makefile,
export them into your shell first: `set -a; . ./.env; set +a; docker stack
deploy -c deploy/stack.yml swarmdash`.

Tear it down with `make down` (leaves `.env` and the SQLite data volume in
place, so state survives a re-deploy).
