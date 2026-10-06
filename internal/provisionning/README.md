# Provisionning API — instance administration (management plane)

The Provisionning API lets an external system provision and reconcile a Xolo instance
without any human interaction: a control plane, a Kubernetes operator, a
Terraform provider, an Ansible playbook or a plain script.

It is deliberately **not** part of `/api/v1/`. Its security boundary is
different — instance-wide privileges, no user context — so it lives on its own
listener, on its own port, with its own TLS configuration, its own middleware
chain and its own authentication mechanism.

```
Xolo process
├── http.Server (internal/http)          Web UI, OIDC, /api/v1, LLM proxy
└── provisionning.Server (internal/provisionning)  dedicated listener + port + mutual TLS
        └── handler/v1                   transport only
                └── service.ProvisioningService   (internal/core/service)
                        └── port.TenantStore / OrgStore / UserStore / RoleStore
```

The resources are nested the way the domain is: a **tenant** owns
**organizations** and **users**, an organization owns **members** and **roles**.

Both servers share the root context of `cmd/server`, and the Provisionning API uses the
**same database** as the public server. Transactional writes use bound stores;
cache invalidation and local events are deferred until commit.

## Authentication

Mutual TLS, and nothing else. There is no OIDC, no session, no cookie and no
user API token on this port, and no Provisionning API endpoint is ever mounted on the
public HTTP port. There is no anonymous fallback.

The listener requires **TLS 1.3**, a client certificate verified against the configured
CA, and **exactly one URI SAN** matching `XOLO_PROVISIONNING_API_AUTHORIZED_URIS`
exactly. The Common Name never authorizes a client. These checks run during the
TLS handshake and in middleware around every route, including health, permissions,
unknown routes and refused methods. HTTP rejections use `403` and
`client_certificate_rejected` in the existing JSON error envelope.

An authorized URI administers the whole instance, with tenant and organization
parent checks. There are no permissions per certificate. Renewing a certificate
with the same authorized URI retains its identity and rate budget.

**Upgrade requirement:** installations already enabling this listener must reissue
client certificates with one URI SAN and configure the allowlist before restarting.
An empty list, duplicate or invalid URI prevents startup. Clients must support TLS 1.3.
Routes, request bodies and response schemas are unchanged.

TLS material is loaded at startup, before the listener opens: a missing or
inconsistent certificate, key or CA bundle is a startup failure, never a
first-request failure.

## Configuration

| Variable | Default | Description |
|---|---|---|
| `XOLO_PROVISIONNING_API_ENABLED` | `false` | Opens the administration listener |
| `XOLO_PROVISIONNING_API_ADDRESS` | `:3003` | Listen address |
| `XOLO_PROVISIONNING_API_TLS_CERT_FILE` | — | Server certificate (PEM), required when enabled |
| `XOLO_PROVISIONNING_API_TLS_KEY_FILE` | — | Server private key (PEM), required when enabled |
| `XOLO_PROVISIONNING_API_TLS_CLIENT_CA_FILE` | — | Authority verifying client certificates, required when enabled |
| `XOLO_PROVISIONNING_API_AUTHORIZED_URIS` | — | Required comma-separated list of distinct absolute client URIs |
| `XOLO_PROVISIONNING_API_RATE_LIMIT` | `10` | Requests/second per authorized URI, per process |
| `XOLO_PROVISIONNING_API_RATE_BURST` | `20` | Burst allowance per URI |
| `XOLO_PROVISIONNING_API_SHUTDOWN_TIMEOUT` | `10s` | Graceful shutdown budget |

Multi-tenancy is configured on the instance, not on this API:

| Variable | Default | Description |
|---|---|---|
| `XOLO_MULTITENANCY_ENABLED` | `false` | Allows more than one tenant |
| `XOLO_MULTITENANCY_HOST_PATTERN` | — | Hostname template, e.g. `{tenant}.xolo.example.com`, required when enabled |
| `XOLO_MULTITENANCY_DEFAULT_TENANT_SLUG` | `default` | The tenant served when multi-tenancy is disabled |

## Endpoints

| Method | Route | Notes |
|---|---|---|
| `GET` | `/v1/healthz` | Behind mutual TLS as well |
| `GET` | `/v1/permissions` | The RBAC catalog: the only source of valid permission codes |
| `GET` | `/v1/tenants` | `?slug=` for an exact lookup, otherwise `?page=&limit=` |
| `POST` | `/v1/tenants` | **Refused with `409` on a single-tenant instance** |
| `GET` | `/v1/tenants/{tenantID}` | |
| `PATCH` | `/v1/tenants/{tenantID}` | `name`, `description`, `active`. The slug is immutable |
| `DELETE` | `/v1/tenants/{tenantID}` | Removes everything the tenant owns |
| `GET` | `/v1/tenants/{tenantID}/organizations` | `?slug=` for an exact lookup, otherwise `?page=&limit=` |
| `POST` | `/v1/tenants/{tenantID}/organizations` | Creates the organization, its builtin roles and, optionally, its initial owner |
| `GET` | `/v1/tenants/{tenantID}/organizations/{orgID}` | |
| `PATCH` | `/v1/tenants/{tenantID}/organizations/{orgID}` | `name`, `description`, `active`, `currency`, `shareQuotaEqually`. The slug is immutable |
| `DELETE` | `/v1/tenants/{tenantID}/organizations/{orgID}` | |
| `GET` | `/v1/tenants/{tenantID}/organizations/{orgID}/members` | Paginated |
| `POST` | `/v1/tenants/{tenantID}/organizations/{orgID}/members` | `userId` **or** `user{provider,subject,…}`, plus `roleIds[]` and/or `builtinRoles[]` |
| `GET` | `/v1/tenants/{tenantID}/organizations/{orgID}/members/{membershipID}` | |
| `PUT` | `/v1/tenants/{tenantID}/organizations/{orgID}/members/{membershipID}/roles` | Full replacement of the role set |
| `DELETE` | `/v1/tenants/{tenantID}/organizations/{orgID}/members/{membershipID}` | |
| `GET` | `/v1/tenants/{tenantID}/organizations/{orgID}/roles` | Builtin and custom roles |
| `POST` | `/v1/tenants/{tenantID}/organizations/{orgID}/roles` | Custom role |
| `GET` | `/v1/tenants/{tenantID}/organizations/{orgID}/roles/{roleID}` | |
| `PUT` | `/v1/tenants/{tenantID}/organizations/{orgID}/roles/{roleID}` | Custom roles only |
| `DELETE` | `/v1/tenants/{tenantID}/organizations/{orgID}/roles/{roleID}` | Custom roles only |
| `GET` | `/v1/tenants/{tenantID}/users` | `?provider=&subject=` for an exact lookup, otherwise `?search=&active=&page=&limit=` |
| `PUT` | `/v1/tenants/{tenantID}/users` | Idempotent upsert on `(provider, subject)`: `201` when created, `200` otherwise |
| `GET` | `/v1/tenants/{tenantID}/users/{userID}` | |
| `PATCH` | `/v1/tenants/{tenantID}/users/{userID}` | `email`, `displayName`, `active` |

Users hang from the tenant because `(provider, subject)` is only unique within
one: the same person signing in on two tenants owns two distinct accounts.

### Single-tenant instances

A default installation owns exactly one tenant, `default`, created by the schema
migration. It is not visible to end users — no subdomain, no change of URL — but
it is the `{tenantID}` every route above needs. A control plane discovers it
with `GET /v1/tenants?slug=default`, then uses that identifier throughout.

Creating a second tenant is refused with `409` while
`XOLO_MULTITENANCY_ENABLED` is false: no hostname would resolve to it, so its
organizations would be unreachable.

Payloads are JSON in camelCase, timestamps are RFC 3339, and collections are
returned as `{"items": […], "page": 1, "limit": 50, "total": 123}`. Unknown
fields are rejected so a misspelled field is reported instead of ignored.

### Errors

Every error uses the same envelope:

```json
{"error": {"code": "conflict", "message": "organization with slug \"acme\" already exists in this tenant (id: c9m2…)"}}
```

| Code | HTTP | Cause |
|---|---|---|
| `invalid_request` | 400 | Malformed body, unknown field, invalid query parameter |
| `client_certificate_rejected` | 403 | Client certificate or URI is not authorized |
| `rate_limited` | 429 | Per-URI budget exceeded; retry after the `Retry-After` seconds |
| `not_found` | 404 | Unknown resource, or a resource belonging to another tenant or organization |
| `method_not_allowed` | 405 | Known resource, wrong method |
| `conflict` | 409 | Existing resource, or a business invariant that refuses the change |
| `unprocessable` | 422 | Well-formed value refused by the domain |
| `internal_error` | 500 | Unexpected failure |

Messages are always built explicitly. Stack traces, SQL errors, file paths, TLS
details and secrets never reach the client: the full detail is logged
server-side.

## Identity model

A user is identified by its `provider` + `subject` tuple **within its tenant**,
the same key interactive authentication uses, so a provisioned user can log in
afterwards. The API deliberately offers no email-based identity: `email` is a
profile field, never an identifier.

### Provisioning ahead of the first sign-in

`POST /v1/tenants/{tenantID}/organizations` creates its owner before that person
ever signs in, so the account already exists when they do. That requires the caller to know their
`subject` — the identifier the identity provider assigns them — in advance. It
works when the control plane also owns the identity provider, or derives the
subject deterministically.

When the subject cannot be known ahead of time, do not disable
`XOLO_HTTP_AUTHN_AUTO_CREATE_USERS`: it would lock those people out. Use
`XOLO_HTTP_AUTHN_ACTIVE_BY_DEFAULT=false` instead. The account is then created
on first sign-in but stays inactive and grants nothing. The control plane picks
it up with `GET /v1/tenants/{tenantID}/users?active=false`, attaches it to an
organization with `POST /v1/tenants/{tenantID}/organizations/{orgID}/members`,
and enables it with
`PATCH /v1/tenants/{tenantID}/users/{userID} {"active": true}`.

## Invariants

- Provisioning an organization administrator **never** grants platform-wide privileges.
  A user created through this API receives exactly the `user` platform role, and
  the platform roles of an existing user are never modified.
- The addresses listed in `XOLO_HTTP_AUTHN_DEFAULT_ADMINS` are reserved: writing
  one of them on a user is refused with `422`. The authentication bridge grants
  the platform admin role to whoever signs in with such an address, so accepting
  it here would be an indirect privilege escalation.
- An organization always keeps at least one owner: removing or downgrading its
  last one is refused with `409`.
- A role can only be assigned to a membership of the organization it belongs to.
  Anything else is `422`, and no role is modified.
- A membership or role belonging to another organization is reported as `404`,
  and so is an organization or a user belonging to another tenant.
- A user is always resolved inside the organization's own tenant, which is what
  makes cross-tenant membership impossible.
- The `default` tenant can neither be deleted nor deactivated: it is the tenant
  every single-tenant instance resolves to.
- Builtin roles cannot be modified nor deleted.
- Only permission codes present in the RBAC catalog are accepted.

## Reconciliation

Identifiers are stable, `PUT /v1/tenants/{tenantID}/users` is idempotent,
creating a tenant or an organization on an existing slug answers `409` while
including the existing identifier, and the lookup endpoints allow the current
state to be read back in full. The single side effect of
`POST /v1/tenants/{tenantID}/organizations` is documented: it creates the
builtin roles of the organization.

## Transactions, audit and correlation

Every provisioning mutation validates parents and performs its writes in one
transaction. A failure rolls back the whole operation, including changes to an
existing user, role assignments and the audit itself. PostgreSQL uses `SERIALIZABLE`;
SQLite writer conflicts and PostgreSQL serialization failures replay the whole
operation with bounded, cancelable backoff. There is no instance-wide publication lock.

`mutation_audits` records one before/after pair per resource actually changed
(tenants, organizations, users, memberships and roles), including role associations
and cascading deletion. Repeated changes to a resource coalesce within the
transaction; a no-op produces no audit. States exclude secrets and technical
timestamps. Audits retain the tenant/organization scope after resource deletion.
UUID audit identifiers do not imply commit order. This audit covers provisioning
mutations only; reads and ordinary web UI operations do not write it.

Send `X-Request-ID` as a single value of exactly 32 lowercase hexadecimal characters.
Invalid, repeated or missing values are replaced. The retained value is returned in
the response and used in logs, audit and local events, unchanged across retries.
The actor URI comes only from the authorized certificate. Internal calls use
`urn:xolo:operator:local` and a generated correlation ID when no actor is supplied.

Existing local member and role events retain their types and messages, including
separate member-added and role-assigned events. They are emitted only after commit
through the existing asynchronous mechanism; they are not a durable outbox. The
audit is persisted atomically. Transactional reads bypass the shared user cache;
affected user entries, secondary keys and cascading token entries are invalidated
after commit.

Rate budgets are local to each process, allocated only for configured URIs and
shared by certificates bearing the same URI. Exceeding the budget returns `429`
with `Retry-After` (seconds), `rate_limited`, and the retained request ID.

Migration `202610060001` follows the UUID migration and adds the audit table on
upgrades and fresh installations. Its rollback refuses to erase audit history.
With `XOLO_STORAGE_AUTO_MIGRATE=false`, stop all writers, back up the database, then run
`bin/migrate apply -writers-stopped` before starting the server. The UUID migration and recovery artifacts remain unchanged.

## Development PKI

```bash
mkdir -p dev-pki && cd dev-pki

# Certificate authority
openssl req -x509 -newkey rsa:4096 -nodes -days 365 \
  -keyout ca.key -out ca.crt -subj "/CN=xolo-dev-ca"

# Server certificate
openssl req -newkey rsa:4096 -nodes -keyout server.key -out server.csr \
  -subj "/CN=localhost"
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out server.crt -days 365 \
  -extfile <(printf "subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth")

# Client certificate
openssl req -newkey rsa:4096 -nodes -keyout client.key -out client.csr \
  -subj "/CN=control-plane"
openssl x509 -req -in client.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out client.crt -days 365 \
  -extfile <(printf "subjectAltName=URI:urn:xolo:client:control-plane\nextendedKeyUsage=clientAuth")
```

```bash
XOLO_SECRET_KEY=$(openssl rand -hex 32) \
XOLO_PROVISIONNING_API_ENABLED=true \
XOLO_PROVISIONNING_API_AUTHORIZED_URIS=urn:xolo:client:control-plane \
XOLO_PROVISIONNING_API_TLS_CERT_FILE=dev-pki/server.crt \
XOLO_PROVISIONNING_API_TLS_KEY_FILE=dev-pki/server.key \
XOLO_PROVISIONNING_API_TLS_CLIENT_CA_FILE=dev-pki/ca.crt \
bin/server

# Refused: no client certificate
curl -sk https://localhost:3003/v1/permissions

# Accepted
curl -s --cacert dev-pki/ca.crt --cert dev-pki/client.crt --key dev-pki/client.key \
  "https://localhost:3003/v1/tenants?slug=default"

# Then, with the identifier it returned:
TENANT=... # the id read above

curl -s --cacert dev-pki/ca.crt --cert dev-pki/client.crt --key dev-pki/client.key \
  -X POST "https://localhost:3003/v1/tenants/$TENANT/organizations" \
  -d '{"slug":"acme","name":"Acme","owner":{"provider":"openid-connect","subject":"sub-123","email":"owner@acme.tld","displayName":"Owner"}}'
```

## Out of scope for now

- Providers, LLM models, virtual models, middlewares, applications and their
  tokens, quotas, alerts and event settings. They all already have a
  `port.*Store`: exposing them means adding a handler file and its DTOs, with no
  architectural change.
- Per-certificate scopes.
- Email pre-provisioning: the existing `InviteToken` mechanism remains the email
  path, through the Web UI.
- No generated OpenAPI specification.

## User documentation

A French user-facing version of this page lives in
[`docs/fr/administration/provisioning/provisioning.md`](../../docs/fr/administration/provisioning/provisioning.md).
