# Installation & deployment

Before upgrading an existing database, follow the [UUID migration procedure](./uuid-migration.md). All old replicas and workers must be stopped.

This page has not been translated yet. See the [French version](https://xolo-gateway.org/latest/) for the full content.

## Health probes

Every instance answers two paths without authentication, for a load balancer
or an orchestrator:

| Path       | Answers `200` when…                                      | Use             |
| ---------- | -------------------------------------------------------- | --------------- |
| `/healthz` | the process serves HTTP requests                         | liveness probe  |
| `/readyz`  | the instance also reaches its database (within 0.8 s)    | readiness probe |

`/readyz` answers `503` while the database is unreachable: the instance leaves
the load balancer without being restarted. Its result is shared for 0.8 s,
so a flood of probes costs the database one check. Both paths are answered
before tenant resolution, logging, HTTP metrics and the rate limiter: a probe
addresses the instance by its own address, which is no tenant's domain.

On PostgreSQL, `/readyz` goes through a connection of its own, outside the
application's pool: a pool saturated by traffic does not take every instance
out of the load balancer at once. The probe tells whether the database answers,
not whether the instance is busy. Count one more connection per instance.

```yaml
healthcheck:
  test: ["CMD", "wget", "-qO-", "http://localhost:3002/readyz"]
```
