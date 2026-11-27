# Rootless Docker

swarmdash talks to Docker only through the Engine API socket. It doesn't
read `/proc`, mount host paths or need privileges, so it runs fine against
a [rootless](https://docs.docker.com/engine/security/rootless/) daemon. The
limits come from rootless mode itself, not from swarmdash.

## What works and what doesn't

| | Rootless |
|---|---|
| Single-node swarm: stacks, services, console, logs, volume backups | ✅ |
| Container CPU/memory stats | ✅ on cgroup v2 with delegated controllers, otherwise reads 0 |
| Multi-node swarm | ❌ rootless Docker has no overlay networks |
| Ingress routing mesh (`ports:` published via `mode: ingress`) | ❌ same reason |
| Agent on `hostnet` reachable from other nodes | ⚠️ only if its port is forwarded to the real host |

In rootless mode, "host" networking is the RootlessKit network namespace,
not the real host. On a single node, admin and agent still reach each
other there. Across nodes, admin dials `<node advertise address>:8871`,
which reaches the agent only if that port is forwarded out of RootlessKit.

Both `admin` and `agent` detect a rootless daemon at startup (`name=rootless`
in `docker info`'s security options) and log a warning. The dashboard also
shows a banner when admin's own daemon is rootless. The agent's `/v1/info`
reports `rootless` and `cgroup_version`.

## Running it

The rootless socket lives at `$XDG_RUNTIME_DIR/docker.sock` (usually
`/run/user/<uid>/docker.sock`). Mount it at the standard path inside the
container:

```
docker run -d --name swarmdash-agent --network host \
  -v $XDG_RUNTIME_DIR/docker.sock:/var/run/docker.sock \
  -v swarmdash-backups:/var/lib/swarmdash/backups \
  -e SWARMDASH_CLUSTER_SECRET \
  emmtvv/swarmdash agent

docker run -d --name swarmdash-admin -p 8870:8870 \
  -v $XDG_RUNTIME_DIR/docker.sock:/var/run/docker.sock \
  -e SWARMDASH_CLUSTER_SECRET \
  emmtvv/swarmdash admin
```

With `deploy/stack.yml` (`make deploy`), set the socket path in `.env`:

```
SWARMDASH_DOCKER_SOCK=/run/user/1000/docker.sock
```

Running the binary directly instead of in a container works too:
`swarmdash admin --docker-host unix://$XDG_RUNTIME_DIR/docker.sock`, or
just export `DOCKER_HOST`.

## CPU/memory stats

A rootless daemon only gets resource stats on cgroup v2, and only for the
controllers systemd delegates to the user. Check with:

```
docker info --format '{{.CgroupVersion}}'          # want: 2
cat /sys/fs/cgroup/user.slice/user-$(id -u).slice/user@$(id -u).service/cgroup.controllers
```

If `cpu` or `memory` is missing from the second line, delegate them:

```
sudo mkdir -p /etc/systemd/system/user@.service.d
printf '[Service]\nDelegate=cpu cpuset io memory pids\n' | \
  sudo tee /etc/systemd/system/user@.service.d/delegate.conf
sudo systemctl daemon-reload
```

Then log out and back in, or reboot. Without this, swarmdash still works,
but the dashboard and node pages show 0% usage.
