# Unreleased — LLM models and personal models in the provisioning API

The provisioning API exposes the models of the providers of an organization as
the read-only business family `llm_model`, under
`/v1/xolo/tenants/{tenantID}/organizations/{orgID}/llm-models`, with ETags and
events: `{"provider_id","proxy_name","kind","enabled"}`, disabled models
included. Their key is the ID a middleware target `{"kind":"llm","id"}` or a
team model grant designates, and `proxy_name` the name the gateway resolves,
without the organization slug nor a team segment. Models stay managed from the
web UI, whose writes now publish `llm_model.created.v1`, `.updated.v1` and
`.deleted.v1`; deleting a provider publishes the deletion of its models.

The personal virtual models of a member (`~/<name>`) are exposed the same way,
as the read-only family `personal_virtual_model` under
`/v1/xolo/tenants/{tenantID}/members/{memberID}/personal-models`:
`{"name","description","team_id","catalog_overrides","graph"}`, without the
secrets of their plugin nodes. Profile writes publish their events, and
deleting or purging a member publishes the deletion of its models.

Migration `202610230001` projects the existing models and personal models; it
requires every server to be stopped.

Details:
[English](docs/en/administration/provisioning/provisioning.md#llm-models),
[Français](docs/fr/administration/provisioning/provisioning.md#modeles-llm),
[Español](docs/es/administration/provisioning/provisioning.md#modelos-llm);
personal models:
[English](docs/en/administration/provisioning/provisioning.md#personal-models),
[Français](docs/fr/administration/provisioning/provisioning.md#modeles-personnels),
[Español](docs/es/administration/provisioning/provisioning.md#modelos-personales).

# Unreleased — virtual models and middlewares in the provisioning API

The provisioning API manages virtual models and middlewares as two business
families, `virtual_model` and `middleware`, under
`/v1/xolo/tenants/{tenantID}/organizations/{orgID}/virtual-models` and
`…/middlewares`, with ETags, conditional `PUT`s, events and write authority.
Their pipeline graph is written as a whole, or node by node through
`…/{key}/nodes/{nodeID}`, conditioned by the ETag of the pipeline. The secrets
of a plugin node are write-only: reads list their names only. Web UI writes
publish their events as well, and the web UI now refuses a graph with
duplicate IDs, an unknown node type, a plugin node naming no plugin or an edge
to a missing node (`422`). Migration `202610220003` projects the existing
virtual models and middlewares; it requires every server to be stopped.

Details:
[English](docs/en/administration/provisioning/provisioning.md#virtual-models-and-middlewares),
[Français](docs/fr/administration/provisioning/provisioning.md#modeles-virtuels-et-middlewares),
[Español](docs/es/administration/provisioning/provisioning.md#modelos-virtuales-y-middlewares).

# Unreleased — model catalogue declarations

Each model of `GET /api/v1/models` now carries a `xolo` object:
`capabilities_declared` is `false` for a virtual model whose capabilities
neither derive from its pipeline nor are declared in its catalogue, whose
text-only card therefore means "unknown"; `currency` is the currency of the
model's provider, missing when the price is unknown. A virtual model combining
candidates billed in different currencies no longer advertises a price. The
other fields are unchanged; `pricing` is in currency units per 1,000 tokens.
No migration.

Details:
[English](docs/en/administration/organisation/application/application.md#capabilities-and-pricing-of-the-listed-models),
[Français](docs/fr/administration/organisation/application/application.md#capacites-et-tarif-des-modeles-listes),
[Español](docs/es/administration/organisation/application/application.md#capacidades-y-precio-de-los-modelos-listados).

# Unreleased — user quota API

`GET /api/v1/quota?organization=<orgID>` returns the caller's own budget inside
one of their organizations: their personal quota, or their equal share of the
organization budget, with the spend of the current day, month and year and the
end of each window. It reads the counters the quota enforcer checks, so the
figures match a `429`. It accepts the proxy's authentications, a relayed OIDC ID
token included, needs no permission, and refuses applications. Organization,
team and member budgets are not reported. No migration.

Details:
[English](docs/en/concepts/budgets-et-quotas.md#reading-your-own-budget),
[Français](docs/fr/concepts/budgets-et-quotas.md#consulter-son-propre-budget),
[Español](docs/es/concepts/budgets-et-quotas.md#consultar-su-propio-presupuesto).

# Unreleased — durable OIDC sessions and back-channel logout

An interactive OIDC sign-in now opens a session registered in the database, and
its cookie only carries the session identifier. A logout closes the session for
good: a copied cookie no longer authenticates. Sessions survive restarts and
move between replicas sharing `XOLO_HTTP_SESSION_KEYS`. Identity providers can
revoke the sessions of a subject through OpenID Connect Back-Channel Logout, at
`/auth/oidc/providers/{provider}/backchannel-logout`. Every session of that
issuer and subject is closed, in every tenant, and a sign-in started before the
logout is refused. A replayed logout token changes nothing. Checking a session
is a single read that takes no lock. Expired sessions and stale replay entries
are swept every 10 minutes. Durable logout does not cover `oidctoken`,
`oauth2token` and `/auth/token/login` sessions, which keep their own expiry.

`XOLO_HTTP_SESSION_COOKIE_MAX_AGE` now defaults to `24h` as documented. A typo
left it unset, so cookies used to last as long as the browser stayed open. This
also applies to `/auth/token/login` sessions. A zero or negative value is now
refused at startup: a cookie that never expires would outlive its session.
A sign-in must start at Xolo: one initiated by the identity provider fails.

Migration `202610110001` adds the session registry. Its rollback drops it.
Cookies issued before the upgrade carry no session, so every OIDC user signs in
once more. Stop every old replica before migrating: an old server accepts
revoked sessions and issues cookies the new ones refuse.

Details:
[English](docs/en/administration/installation/configuration.md#oidc-sessions-and-back-channel-logout),
[Français](docs/fr/administration/installation/configuration.md#sessions-oidc-et-deconnexion-back-channel),
[Español](docs/es/administration/installation/configuration.md#sesiones-oidc-y-cierre-de-sesion-back-channel).

# Unreleased — declared member identities

A member `PUT` of the common provisioning contract now creates an unknown member
ahead of their first sign-in, and accepts an optional
`"identity": {"issuer", "subject"}`. An absent `identity` keeps the declaration
and the sign-in link, `null` removes both, an object declares it. At sign-in,
Xolo resolves the account by its link, then by its declared identity, then by
an email the identity provider asserts verified and that designates a single
unlinked account; nothing is ever merged. A platform administrator is never
modified by provisioning nor attached to a new identity. Requests of an already
linked account only read it. The manifest lists the `identity` capability.

Migration `202610100001` adds the declared identity columns and a unique index
per tenant; it changes no existing data, and its rollback is refused once an
identity is declared. Stop every old replica before migrating: an old server
ignores declared identities at sign-in.

Details:
[English](docs/en/administration/provisioning/provisioning.md#declared-identity),
[Français](docs/fr/administration/provisioning/provisioning.md#identite-declaree),
[Español](docs/es/administration/provisioning/provisioning.md#identidad-declarada).

# Unreleased — durable signed webhooks

The provisioning API delivers the events of `/v1/events` to HTTPS receivers,
per tenant: `/v1/xolo/tenants/{tenantID}/webhooks`. Deliveries are queued in the
database, leased, retried with backoff for up to 24 hours and signed following
Standard Webhooks, with two secrets during a rotation. Secrets are write-only
and encrypted with `XOLO_SECRET_KEY`. Destinations are restricted to
`XOLO_WEBHOOKS_ALLOWED_ORIGINS`, and every resolved address is checked before it
is dialled. A suspended tenant is paused. Webhooks are disabled by default
(`XOLO_WEBHOOKS_ENABLED`) and never take the lock of the event feed.

Migration `202610090001` adds the subscription and delivery tables and an index
on the feed; it changes no existing data and cannot be rolled back. Upgrade
every replica before enabling webhooks: an old replica deleting a tenant would
leave its subscriptions behind.

Details:
[English](docs/en/administration/provisioning/provisioning.md#webhooks),
[Français](docs/fr/administration/provisioning/provisioning.md#webhooks),
[Español](docs/es/administration/provisioning/provisioning.md#webhooks).

# Unreleased — provisioning reads, conditional writes and event feed

The provisioning API reads, lists and follows every resource of the common
contract. `PUT`s answer an `ETag` and honour `If-Match` within their
transaction; ETags are persisted revisions, never timestamps. Lists use signed
cursors, and `/v1/events` publishes one event per changed resource in commit
order, including changes made through the web UI, sign-ins and invitations.

**A stop-the-world upgrade is mandatory for migration `202610080001`. Stop ALL
old replicas, servers and other writers before migrating.** An old binary keeps
writing without publishing, so projections, ETags and the feed would silently
diverge. The migration gives every existing resource its own revision without
emitting events, and cannot be rolled back. Events are kept for
`XOLO_PROVISIONNING_API_EVENT_RETENTION` (default `720h`).

Details:
[English](docs/en/administration/provisioning/provisioning.md#reads-conditions-and-synchronization),
[Français](docs/fr/administration/provisioning/provisioning.md#lectures-conditions-et-synchronisation),
[Español](docs/es/administration/provisioning/provisioning.md#lecturas-condiciones-y-sincronizacion).

# Unreleased — UUID identity migration

Tenant, organization and user IDs become UUIDs in migration `202610020001`.
Existing UUIDs, relations, emails, provider identities and every role assignment
are preserved. Emails are not normalized and membership roles are not collapsed. External consumers retaining Xolo
IDs must update their references using the recovery mapping.

**A stop-the-world upgrade is mandatory. Stop ALL old replicas, servers, workers
and other writers before migration; rolling upgrades are unsupported.** Old
binaries can write retired IDs into usage and quota tables after migration,
creating orphaned rows and unaccounted spending. The migration lock cannot stop
those binaries. Back up the database and rehearse the upgrade first.

`xolo-migrate diagnose`, `plan` and `apply` provide an offline recovery path for
serialized references and other migration data issues. The migration
binary ships in release archives and the server Docker image. Set
`XOLO_STORAGE_AUTO_MIGRATE=false` after applying migrations manually; startup then
checks compatibility without changing the schema. Automatic migration remains
enabled by default. Downgrading after conversion requires the pre-upgrade backup.

Upgrade instructions:
[English](docs/en/administration/installation/uuid-migration.md),
[Français](docs/fr/administration/installation/uuid-migration.md),
[Español](docs/es/administration/installation/uuid-migration.md).

Recovery plans now use version 2: UUID mappings and explicit serialized corrections
only. Regenerate version 1 plans against the stopped pre-migration database and
review them; changing their version number is unsupported. Conversion uses an
indexed temporary mapping table, one SQL operation per relational reference and
1,000-row pages for serialized fields, in a single atomic transaction. Ordinary
writes retain their existing transactions and configured user cache. Publication
counters, domain management and new role/status concepts are deferred to a later
migration.
