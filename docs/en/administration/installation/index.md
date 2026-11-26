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

## RPM and Debian packages

Every release publishes on its [GitHub page](https://github.com/xolo-gateway/xolo/releases) `.rpm` and `.deb` packages, for `x86_64` and `arm64`:

| Package             | Contents                                                                                   |
| ------------------- | ------------------------------------------------------------------------------------------ |
| `xolo`              | `xolo-server`, `xolo-migrate`, `xolo-adoption`, `xolo-admin`, the systemd service `xolo`   |
| `xolo-plugin-<name>` | one official plugin, under `/usr/lib/xolo/plugins` (`xolo-plugin-pseudonymizer`, `xolo-plugin-mcp-bridge`…) |
| `xolo-plugin-all`   | no file: depends on every official plugin package                                          |

The binaries are statically linked: the packages depend on no library of the system. They are installed and checked on every change on Red Hat Enterprise Linux 10 and 9, Debian 13 and 12; the distributions derived from them (AlmaLinux, Rocky Linux, Ubuntu) suit too.

Download the `xolo` package and those of the plugins you want, or every package of the release with `xolo-plugin-all`, and install them together:

```bash
# Red Hat Enterprise Linux, AlmaLinux, Rocky Linux: everything
sudo dnf install ./xolo-*.x86_64.rpm ./xolo-plugin-all-*.noarch.rpm

# Debian, Ubuntu: the server and two plugins
sudo apt install ./xolo_*_amd64.deb ./xolo-plugin-pseudonymizer_*_amd64.deb ./xolo-plugin-mcp-bridge_*_amd64.deb
```

The server finds its plugins at startup only: installing, upgrading or removing plugin packages restarts the service when it runs, once for all the packages of a command. On Red Hat, the restart that follows a removal comes 5 seconds after the command.

The packages are not signed and no `dnf` or `apt` repository serves them: check the downloaded files against the `checksums.txt` of the release.

The installation creates the `xolo` system account and these files:

| Path                   | Role                                                                                         |
| ---------------------- | -------------------------------------------------------------------------------------------- |
| `/etc/xolo/xolo.env`   | Configuration: one `XOLO_*` variable per line. Kept on upgrades.                             |
| `/etc/xolo/secret.env` | `XOLO_SECRET_KEY` and `XOLO_HTTP_SESSION_KEYS`, generated on the first installation, never replaced. |
| `/var/lib/xolo`        | SQLite database (`data.sqlite`), created on the first start.                                 |
| `/var/cache/xolo`      | Models downloaded by the `pseudonymizer` plugin.                                             |

Back up `/etc/xolo/secret.env` with the database: without `XOLO_SECRET_KEY`, the provider API keys it holds cannot be read.

The package neither enables nor starts the service, which first needs an identity provider. Fill in `/etc/xolo/xolo.env` (see [Configuration](./configuration.md)), then:

```bash
sudo systemctl enable --now xolo
sudo journalctl -u xolo -f
```

An upgrade of the package restarts the service when it runs. Its removal stops it and leaves the configuration, `/etc/xolo/secret.env` and the database in place.

The offline administration commands `xolo-migrate` ([UUID migration](./uuid-migration.md)) and `xolo-adoption` read their configuration from the environment only. `xolo-admin` runs them with the configuration of the service (`/etc/xolo/xolo.env` and `/etc/xolo/secret.env`), under the `xolo` account: run as root, they would leave SQLite files the service could no longer open.

```bash
sudo systemctl stop xolo
sudo xolo-admin migrate diagnose
sudo xolo-admin migrate plan -out /var/lib/xolo/recovery.json
```

The files they write must be writable by the `xolo` account, under `/var/lib/xolo` for instance.

On Red Hat Enterprise Linux, open the port in the firewall when the instance is reached without a reverse proxy on the same machine: `sudo firewall-cmd --permanent --add-port=3002/tcp && sudo firewall-cmd --reload`.
