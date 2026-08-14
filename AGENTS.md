# AGENTS.md

This file provides guidance to LLM agents when working with code in this repository.

## Commands

```bash
# Build
make build                    # builds bin/server
make generate                 # runs templ + tailwind (required before build if .templ files changed)

# Run (with .env)
make watch                    # hot-reload dev server via modd
make CMD='bin/server' run-with-env

# Test
go test ./...                  # unit tests, SQLite only, no Docker needed
go test ./internal/adapter/memory/...  # single package
make test-integration         # store suite on BOTH backends (PostgreSQL via testcontainers, needs Docker; run by the `integration` CI job)
make seed                     # generates e2e.sqlite, a deterministic E2E fixture (see cmd/seed/README.md)

# Release
make goreleaser               # snapshot release
```

**Config env prefix:** `XOLO_` (e.g. `XOLO_HTTP_ADDRESS=:3002`, `XOLO_STORAGE_DATABASE_DSN=data.sqlite`)

## Architecture

Xolo is an enterprise LLM gateway. It wraps the `github.com/bornholm/genai/proxy` OpenAI-compatible proxy with authentication, user management, and a web admin UI.

### Hexagonal structure

```
internal/
  core/
    model/      — domain types: Tenant, Organization, User, AuthToken, Task
    port/       — interfaces: TenantStore, UserStore, TaskRunner, error sentinels
    service/    — orchestration across stores (QuotaService, ProvisioningService)
  provisionning/ — instance provisionning API: own listener, own port, mutual TLS
    handler/v1/ — versioned M2M REST API (tenants > organizations > members/roles, tenant users)
  adapter/
    gorm/       — GORM implementation of the stores (SQLite or PostgreSQL, see internal/adapter/gorm/dialect.go)
                  store tests go through eachBackend() so every behaviour is asserted on both backends
    cache/      — LRU cache wrappers for UserStore
    memory/     — in-memory TaskRunner implementation
  http/
    server.go   — HTTP server (CORS, slog middleware, context injection)
    options.go  — mount points configuration
    context/    — request-scoped values (BaseURL, CurrentURL, Tenant, User)
    middleware/
      tenant/   — resolves the request tenant (subdomain in multi-tenant mode); runs before everything else
      authn/    — authentication: OIDC (goth) + token-based
      authz/    — role assertions (user/admin/active)
      bridge/   — populates/creates User from authn identity
      ratelimit/— per-IP rate limiting
    handler/
      metrics/  — Prometheus metrics endpoint
      webui/
        common/ — shared assets handler + error pages + templ layout components
        admin/  — user management pages (admin-only)
        profile/— user profile + API token management
        templui/ — shadcn-style UI component library (templ)
  setup/        — wires config → concrete implementations (createFromConfigOnce pattern)
  config/       — env-based config structs (XOLO_ prefix)
  metrics/      — Prometheus metric definitions (namespace: "xolo")
```

### Key wiring

- `internal/setup/http_server.go` — assembles the full HTTP server from config; this is the composition root
- `internal/setup/provisionning_api_server.go` — assembles the Provisionning API server; returns nil when disabled
- `internal/setup/helper.go` — `createFromConfigOnce` pattern: each dependency is created at most once per config
- The genai proxy is mounted at `/v1/` and sits behind auth middleware
- `cmd/server` runs the public HTTP server and, when enabled, the Provisionning API server on the same root context; the first fatal error stops both

### Multi-tenancy

A **Tenant** owns organizations and users; it is the outermost isolation
boundary. Slugs and identities are unique per tenant, never instance-wide:
`GetOrgBySlug`, `GetUserByIdentity` and `FindOrCreateUser` all take a
`model.TenantID`, and `ListOrgsOptions`/`QueryUsersOptions` carry a `TenantID`
filter. Xolo IDs stay globally unique, so lookups by ID keep their signature —
the tenant is then asserted by the service layer.

Disabled by default: the schema migration creates a single `default` tenant,
attaches every pre-existing row to it, and the tenant is never surfaced (no
subdomain, no change of URL). `XOLO_MULTITENANCY_ENABLED=true` plus
`XOLO_MULTITENANCY_HOST_PATTERN={tenant}.example.com` switches the resolution to
the request host; a host matching no active tenant answers 404.
See `internal/http/middleware/tenant/`.

### Provisionning API

Machine-to-machine provisioning of tenants, the organizations they own, their
members, roles and users, on a dedicated listener authenticated by mutual TLS
only (`XOLO_PROVISIONNING_API_*`, disabled by default). It never grants
platform-wide privileges and shares the same store instances as the public
server. Creating a second tenant is refused while multi-tenancy is disabled.
See `internal/provisionning/README.md`.

### UI / templating

- Uses [`templ`](https://templ.guide/) for HTML templates (`*.templ` → `*_templ.go`)
- UI components are in `internal/http/handler/webui/templui/` (shadcn-inspired, via templui)
- Tailwind CSS is generated from `misc/tailwind/templui.css` → `internal/http/handler/webui/common/assets/templui.css`
- Run `make generate` after editing any `.templ` file or CSS
- NEVER edit generated templ components Go files. ALWAYS edit .templ files instead THEN generate the Go code.

**IMPORTANT — composants UI :** toute nouvelle interface doit **obligatoirement** utiliser les composants templui disponibles sous `internal/http/handler/webui/templui/component/` (input, button, checkbox, label, card, badge, alert, etc.). Ne jamais utiliser de balises HTML brutes (`<input>`, `<button>`, `<select>`) là où un composant templui équivalent existe.

### Adding a new proxy hook

Mount hooks on the `proxy.Server` in `internal/setup/http_server.go` using `proxy.WithHook(...)`. The hook system is defined in `github.com/bornholm/genai/proxy` — see `proxy/hook.go` for the interface.
