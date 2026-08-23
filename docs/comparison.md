# Comparison

|  | swarmdash | Portainer CE | Swarmpit |
|---|---|---|---|
| Scope | Swarm only | Swarm + Kubernetes + standalone | Swarm only |
| License / cost | Apache-2.0, free | free CE / paid BE | open source, free |
| RBAC (admin/viewer roles) | ✅ free | 🔒 Business Edition only | basic |
| SSO (OIDC) | ✅ free | 🔒 Business Edition only | ❌ |
| GitOps auto-sync (poll/webhook) | ✅ free | 🔒 Business Edition only | ❌ |
| Web exec console | ✅ | ✅ | ✅ |
| Status as of writing | new, 0.x | mature, widely deployed | feature-complete, low activity |

Portainer/Swarmpit facts above are best-effort as of Aug 2026 — verify
against their own docs before deciding. swarmdash is the newest of the
three by a wide margin; weigh that against the free RBAC/SSO/GitOps.
