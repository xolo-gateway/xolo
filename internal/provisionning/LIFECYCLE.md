# Lifecycle and business resource extensions, version 1

These are Xolo extensions to the pinned common contract. They do not add DELETE
or business resources to the minimal manifest. Discover enabled capabilities at
`GET /v1/xolo/extensions` on the existing, strictly authorized mTLS listener.
Each authorized certificate is a provisioning principal, subject to the family
ownership policy; these routes do not grant public platform-admin privileges.

## Activation and migration

Stop old writers before upgrading. Migration `202610030002` installs deletion
metadata, receipt candidates, durable leaf versions, retired-login guards and
database freeze triggers. Migration `202610030003` backfills business projections
without publishing historical creation events. All replicas must use the same
binary, ownership and extension settings.

| Setting | Default | Meaning |
| --- | --- | --- |
| `XOLO_LIFECYCLE_ENABLED` | `false` | Enables canonical DELETE, export/receipt routes and purge worker |
| `XOLO_LIFECYCLE_RETENTION` | `720h` | Minimum 1 second, maximum 3650 days; copied into each new deletion |
| `XOLO_LIFECYCLE_POLL_INTERVAL` | `1m` | Worker poll, between 1 second and 1 hour |
| `XOLO_BUSINESS_RESOURCES_ENABLED` | `false` | Enables the five business families and their feed events |

Coordinate activation with every feed/webhook consumer. Consumers must understand
`deleted`, terminal absence and resynchronization after history loss. Disabling
lifecycle stops routes and worker, **not the database guards on existing frozen
scopes**. Local parent deletion refuses with `lifecycle_disabled` while disabled;
there is no fallback to an immediate cascade. The public UI does not confirm
exports or bypass retention. Enable the provisioning listener to operate receipts.

## Routes and states

Let `P` be one of:

- `/v1/tenants/{tenantID}`
- `/v1/tenants/{tenantID}/organizations/{orgID}`
- `/v1/tenants/{tenantID}/members/{memberID}`

| Operation | Result |
| --- | --- |
| `DELETE P` | 202 and deletion metadata; optional common `If-Match` |
| `GET P` / collection list | Frozen representation with `status: "deleted"` until purge |
| `GET P/deletion` | Metadata, including after physical purge |
| `GET P/deletion/export` | Private `xolo-deletion/1` archive, `Cache-Control: no-store` |
| `POST P/purge-confirmation` | 200; JSON `{"export_sha256":"…"}` and explicit deleted `If-Match` required |

No query parameters are accepted on these operations. DELETE/GET have no request
body. Confirmation accepts one strict JSON object (maximum 4096 bytes); `*` is
not an explicit deleted version. Repeating DELETE preserves the original ETag
and deadline. Repeating the same receipt is idempotent; a different hash cannot
replace it. The worker alone performs physical purge, after both confirmation
and `purge_after`. Parent UUIDs are permanently retired, not reusable.

Metadata includes `resource_type`, `tenant_id`, `resource_id`, `etag`, `deleted_at`,
`purge_after`, `export_sha256`, `confirmed_at`, `purged_at`, `attempts` and
`diagnostic`. Confirmation/progress do not change the deleted resource version.
A process restart retains all this state. Deadline changes in configuration
never shorten already scheduled retention.

A tenant supersedes every child deletion. A member purge waits while it has a
membership in a deleted organization; purging that organization removes the
membership, then the member can become eligible. Organization deletion preserves
ordinary tenant members and their other memberships; application shadow users
belong to the organization and are removed with it. Deleting a last active owner
is rejected. A composite deletion requires write authority for each affected
resource family, including controlled descendants and subscriptions.

Expected errors include 400 invalid representation/parameter, 404 absent or
foreign resource, 409 conflict/last owner/export mismatch, 410
`resource_deleted` on frozen writes, 412 `precondition_failed`, 428
`precondition_required` for a missing explicit receipt version, and 403
`ownership_denied`. Technical failures return a generic 500.

### Immediate leaves

`DELETE /v1/tenants/{tenantID}/domains/{hostname}` and
`DELETE /v1/tenants/{tenantID}/organizations/{orgID}/members/{memberID}` return
204. An absent leaf is idempotent only without a precondition. Parent scope,
member tenant and last owner are checked transactionally. Membership role joins
are removed, but neither the account nor organization business assets are deleted.

A recreated hostname or membership has a fresh ETag, even after feed retention.
An old conditional DELETE cannot delete that new incarnation. Events invalidate
a key: always re-read and accept either absence or the latest recreation.

## Locking, access and asynchronous work

The publication clock is the first lock in each control transaction, followed by
parents, children and facts. SQLite's writer transaction serializes connections.
On PostgreSQL a BEFORE STATEMENT trigger locks the publication-clock row before
row mutations; BEFORE ROW triggers check both old and new scope. Database
triggers cover all inventory tables, including workers and adapter writes that
do not publish common facts. They reject children arriving after freeze.
The purge bypass is set and cleared inside that same locked transaction and
never committed as enabled. A failure rolls it back with all deletions.

Tenant access is disabled immediately; deleted organizations disappear from
public slug resolution, affected API token authentication is denied, and the
identity bridge refuses the deleted member even on a no-op login. Global
issuer/subject sessions shared with other tenants remain valid there. Purge
removes unshared sessions and stores tenant-bound HMAC revocation keys for
retired identity/email values, preventing automatic account recreation.
An explicit fresh provisioning grant is distinct from automatic login.
Global logout watermarks/replay guards remain instance-wide security metadata.

Already authorized requests and transmitted notifications cannot be recalled.
A late database write is refused, so an in-flight provider call may complete
without persisting its usage. Drain requests before deletion when exact final
accounting is required. Application setup uses fresh store reads for access
checks; there is no deletion-sensitive local identity cache to invalidate.

## Archive and durable receipt procedure

1. Schedule DELETE; save the returned deleted ETag and metadata.
2. Download `P/deletion/export` over the authorized mTLS connection into private
   storage. The archive is buffered in memory under the writer lock: provision
   memory and a maintenance window for large scopes.
3. Verify the format, source, resource family/IDs, deleted ETag, `complete: true`,
   table inventory and `record_count`. Verify the lowercase SHA-256 over the
   **exact UTF-8 bytes of the JSON payload value, including its braces**. Do not
   pretty-print or reserialize that value first. The envelope has `payload`
   and `sha256`; the digest excludes envelope whitespace.
4. Commit the validated archive to durable storage and verify it can be read
   back. Only then POST its digest with the deleted ETag. Generation and elapsed
   retention never constitute a receipt. A hash of a truncated, unknown or
   different-scope archive is refused.
5. Poll `P/deletion`. Before eligibility no purge occurs. After eligibility,
   success reports `purged_at` and `diagnostic: "purged"`. `purge_failed`
   increments attempts without committing a partial scope. Fix the underlying
   storage issue and leave the worker running; restarting is safe. Do not edit
   receipts or disable guards manually. Scope attempts are prioritized fairly;
   a failing scope does not prevent other eligible scopes from being attempted.

SHA-256 detects corruption, not malicious replacement; mTLS and file permissions
establish provenance. Xolo verifies a candidate it fully generated, but cannot
prove that a client really retained it durably. The authenticated receipt is the
client's attestation. The adoption export is **not** a deletion archive.

### Explicit data inventory

The implementation inventory is `internal/adapter/gorm/lifecycle_inventory.go`;
selection is materialized before removing any parent. Export and purge share it.

| Data | Scope, export and purge |
| --- | --- |
| Tenants, organizations, users, domains | Owned rows; tenant members survive organization deletion except application shadow users |
| Memberships, membership roles, user roles/preferences | Scoped joins; other memberships of surviving users remain |
| Builtin/custom roles, permissions and model grants, application roles | Organization-owned assignments; full permission/model linkage exported |
| Invitations | Organization-owned, created by a removed user, or addressed to its email within the same tenant |
| Auth tokens | Organization/application and member tokens; export contains stored hashes, not usable plaintext |
| Personal virtual models, plugin node secrets | Personal user IDs and `~:{userID}` secret namespace as well as organization scope |
| Applications, virtual models/pipeline graphs, middlewares | Complete stored configuration, including embedded graph references |
| Providers, LLM models | Organization scope, encrypted API keys and pricing/configuration included |
| Quotas, quota usages, usage records | Organization/application/user ownership; composite counter keys retained in export |
| Alerts, incidents, events, event settings | Organization footprint; personal alerts/incidents and user/member/actor-attributed facts within the tenant follow member deletion; creator of a retained organization alert is detached |
| Common projections and durable leaf versions | Keys, validators and projections exported; removed for the erased footprint |
| Mutation audit | Scope facts exported then erased; remaining audit actor references to erased members are anonymized |
| Publications and webhook deliveries | Select by immutable scope keys independently, including delivery copies whose source event expired |
| Webhook subscriptions | Tenant deletion removes subscription configuration, encrypted signing secrets and queue |
| Identity sessions | Only identities without another surviving account; shared sessions remain |
| Resource deletions | Pending descendant state included; only minimal retired UUID/progress metadata remains after purge |

Member erasure removes its user-scoped counters; non-identifying organization
aggregate usage counters remain as historical accounting. Historical audit facts
are selected even when their original resource has already been deleted.

Rows are sorted, include composite keys, and the payload records the feed source
and `c0`. This is a versioned storage inventory for controlled restoration, not
an automatic import API. Back up the encryption key separately: encrypted
provider/plugin/webhook secrets are otherwise unusable. Restore business parents
and dependencies in an isolated compatible database; validate foreign scopes,
roles, projections, and counts before service. Never replay archived sessions,
token hashes, revoked identities, pending deliveries or old cursors into a live
instance. Preserve the instance revocation registry. Test a full database restore
as well as your archive consumer.

Transport queues, audit receipts and global session/logout state can advance
after the business freeze; export captures their state at its own locked C0.
They are cleanup evidence, not a promise to replay notifications or sessions.
The immutable business scope cannot acquire new children. The purge independently
reselects cleanup records under the same lock, including late transport copies.

## Terminal facts, history and retention

Each successful purge atomically records a minimal local audit action and a
`{tenant|organization|member}.purged.v1` CloudEvent containing only keys, ETag,
source, sequence, time and request correlation. No deleted representation,
identity or credential is embedded. Freeze emits `*.deleted.v1`; receipt emits
`*.export_confirmed.v1`. Leaf deletion emits `tenant_domain.deleted.v1` or
`organization_membership.deleted.v1`.

Deleting publication positions raises the global retention floor. Cursors before
that floor return 410; lagging subscriptions become `history_lost` and require
explicit resnapshot/reset. A consumer must not treat holes as a complete feed.
A deleted tenant's subscriptions are gone, so its terminal event remains readable
from the instance feed rather than being promised to those former subscriptions.

Business rows remain at least until the fixed retention deadline and forever
without confirmation. Successful purge deletes receipt candidates. Minimal UUID
tombstones, HMAC login guards and logout revocation metadata remain indefinitely;
budget for them. Terminal feed facts follow the ordinary feed retention; local
terminal audit is retained indefinitely. Their purpose is idempotency
and preventing retired access/UUID resurrection, not retaining business content.

External exports, backups, logs, caches outside Xolo and received notifications
have separate lifetimes. The operator must set archive/backup/log retention and
access policies, register deletion deadlines with downstream recipients, rotate
or destroy archived encryption material as appropriate, and test expiration and
restoration. There is no database mechanism that erases these external copies.

## Business resource profile

All five families use the common transaction, ownership, weak ETag, `If-Match`,
strict full PUT, GET and cursor list mechanics. PUT returns 200 with the stored
public representation and ETag; a true no-op changes neither timestamp nor event.
Lists accept only common pagination parameters. IDs are canonical Xolo xid values
or canonical UUIDs chosen by the client. They are immutable and globally unique;
parent qualification is mandatory on every call.

| Family / collection | Parent route | PUT/GET fields |
| --- | --- | --- |
| `custom_role` / `roles` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}` | name, description, permissions, model_grants |
| `application` / `applications` | same organization parent | name, description, active, role_ids |
| `quota` / `quotas` | `/v1/xolo/tenants/{tenantID}` | scope, scope_id, currency, daily_budget, monthly_budget, yearly_budget |
| `alert` / `alerts` | organization parent | name, description, scope, owner_id, query, aggregation, window_seconds, comparator, threshold, for_seconds, enabled |
| `provider` / `providers` | organization parent | name, type, base_url, active, currency, cloud_tier, billing_mode, subscription_plan, retry_config, rate_limit_config |

Append `/{resourceID}` for GET/PUT. Lists contain common item envelopes with
`key.resource_id`, representation and ETag. Custom role routes use `{roleID}`;
builtin roles stay immutable and are excluded from this custom-role collection.
Legacy local role POST/DELETE remain subject to the same authority and freeze.

Names are trimmed, 1–200 UTF-8 bytes; descriptions at most 4000 bytes. Permission
codes must exist in `GET /v1/xolo/permissions`; model grants have `model_id` and
`kind` (`llm` or `virtual`), with same-organization validation including the
LLM's provider. Application roles must belong to its organization. Grants and
roles are normalized as sets.

Quota scope is `org`, `user` or `application`, qualified through the tenant.
Scope is immutable; there is one quota per scope/ID. Nullable budgets mean
unlimited and otherwise are nonnegative integer microcents. Currency must be
supported by Xolo. Alerts use `org` or `personal`, an immutable owner/scope,
a valid EventQL query, `count` aggregation, supported comparator, finite
nonnegative threshold, 1 second–31 days window and 0–31 days pending duration.
Evaluator state is excluded from configuration ETags.

Provider type must be supported, URL must be HTTP(S) without userinfo/query/
fragment, tier 0–2, billing `payg` or `subscription`. Retry/rate limits and
subscription constraints are validated using the existing provider structures.
Nullable provider settings must still be present in full PUT. Write-only
`api_key` is required on creation; omission preserves it on update, empty string
explicitly replaces it, and null is rejected. Parent checks precede decryption.
A credential change advances the ETag, but GET/list/discovery/events never contain
plaintext or ciphertext. The private deletion archive contains the ciphertext.

Ownership keys are the family names above, using
`XOLO_OWNERSHIP` like the existing families. The mTLS
provisioning principal requires the configured write authority. Public UI
authorization still uses the existing organization permission catalog and scoped
resolvers, including for platform admins; M2M error mapping remains separate.
Events are `{family}.created.v1`, `{family}.updated.v1` and, on local resource
removal, `{family}.deleted.v1`, with keys/ETag only. Parent purge removes all five
families through the inventory above. This profile adds no independent business
DELETE endpoint or second transaction orchestrator.
