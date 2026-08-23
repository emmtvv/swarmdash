# Architecture

`swarmdash` is a single binary with two modes: `admin` runs on a manager
node and serves the UI; `agent` runs on every node and gives `admin` a way
to reach that node's Docker daemon for exec/logs/stats.

## Why the agent/admin split

`docker exec` isn't swarm-aware — it only works against the Docker daemon
that's actually running the container. A manager can see every task
cluster-wide via the Swarm API, but to open a shell in a specific container
it has to reach the node that's running it. `agent` is a thin process on
each node that does exactly that on admin's behalf, authenticated with a
shared cluster secret.

Node addressing piggybacks on Swarm itself: `admin` resolves a task's node
via `NodeInspect` and reaches its agent at `<node.Status.Addr>:8871`, the
same advertise address Swarm already uses for its own control-plane
traffic. No overlay network or service discovery layer needed.

```
 browser  <-- HTTP/WS + cookie session -->  admin (manager node)
                                              |  Docker API (stacks/services/tasks/nodes)
                                              |  Bearer <cluster secret>
                                              v
                                            agent (every node)  <-->  local Docker daemon
```
