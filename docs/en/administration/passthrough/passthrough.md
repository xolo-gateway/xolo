# Credential passthrough

The passthrough lets a client that **already holds its own upstream credential** route its traffic through Xolo to have it metered, without handing the gateway a provider key.

It is the inverse of the usual LLM proxy:

| | Proxy `/api/v1/` | Passthrough `/passthrough/` |
|---|---|---|
| Upstream credential | held by Xolo (encrypted provider) | supplied by the client on every request |
| Model selection | routed by Xolo (virtual models, pipelines) | decided by the client |
| Request body | transformed by hooks | relayed verbatim |
| What Xolo adds | routing, transformation, quotas, metering | identity, authorization, metering |

The motivating case: an agentic CLI puts its credential in the `Authorization` header and offers no way to move it. Without the passthrough you have to give Xolo a separate API key; with it, the user keeps their credential and the traffic still shows up in the dashboards.

> **Disabled by default.** Enabling it means the instance forwards credentials it does not own to a third party. That is an explicit operator decision.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `XOLO_PASSTHROUGH_ENABLED` | `false` | Turns the surface on |
| `XOLO_PASSTHROUGH_MOUNT_PREFIX` | `/passthrough/` | Path served; this is the client's base URL |
| `XOLO_PASSTHROUGH_UPSTREAM_BASE_URL` | `https://api.anthropic.com` | Destination origin. `https` required outside loopback |
| `XOLO_PASSTHROUGH_CREDENTIAL_HEADER` | `X-Xolo-Key` | Header carrying the Xolo token |
| `XOLO_PASSTHROUGH_PROVIDER_ID` | — | Provider usage is attributed to. **Required** |
| `XOLO_PASSTHROUGH_ALLOWED_PATHS` | `/v1/messages` | Comma-separated upstream paths allowed |

`ALLOWED_PATHS` is an allowlist: enabling the relay for one endpoint does not open the rest of the upstream API.

The relay has no timeout knob of its own: it reuses `XOLO_PROXY_UPSTREAM_TIMEOUT`, which already bounds upstream calls on the proxied path. On this surface it caps the wait for the response headers, never the response itself — an agentic call streams for as long as the upstream keeps talking.

## The two credentials

A relayed request carries two, and they compete for one header:

- the **upstream credential**, in `Authorization`, which the client wants relayed;
- the **Xolo token**, which identifies the caller to the gateway.

Since the client cannot move its own, the Xolo token travels in `XOLO_PASSTHROUGH_CREDENTIAL_HEADER`. A middleware swaps them before the authentication chain — which therefore runs unmodified — and the upstream credential is put back just before the request leaves for the upstream. The Xolo token is stripped from the outbound request: it means nothing upstream, and forwarding it would hand a third party a credential valid on this instance.

A request without a Xolo token is rejected with 401 even when it carries a valid upstream credential: relaying an unidentified call makes no sense when nobody can be billed for it.

## Connectivity probe

The client probes `GET|HEAD <base>/api/hello` **with no credentials** before any request, and will not proceed until it succeeds. That route is therefore mounted outside the authentication chain and answers `200`, revealing nothing beyond the fact that a relay is mounted.

## Metering

Usage is extracted from the response as it streams, without buffering: nothing is delayed for the client. Both response shapes are handled — the SSE stream, and the single JSON message clients fall back to after a stream fails.

A usage record is written with the same fields as proxied traffic, so it lands in the same dashboards, quotas and exports. Cost is computed from the model's tariff **as registered under the configured provider**: if the upstream model is not declared there, the call is metered in tokens with a zero cost — visibly unpriced rather than silently wrong. Registering the model under the provider is what turns pricing on.

Failed calls (status ≥ 400) are not recorded: they bill nothing upstream, and counting them would inflate the ledger with usage that was never charged.

## Client configuration

```bash
export ANTHROPIC_BASE_URL="https://xolo.example.com/passthrough"
export ANTHROPIC_CUSTOM_HEADERS="X-Xolo-Key: <xolo-api-key>"
```

The upstream credential stays whatever the client manages on its own; Xolo does not touch it.

## Known limitations

- **The probe is assumed to be relative to the base URL.** If a client requests it at the origin root instead, mount the relay on a dedicated subdomain rather than a path prefix.
- **The upstream credential transits the gateway.** It is relayed without being logged or stored, but it does pass through the process: the relay only makes sense on an instance whose operator is trusted by its users.
- **No pipeline hook runs.** The body is relayed verbatim, so there is no virtual model and no pipeline node — hence **no pseudonymization**. An instance that routes through virtual models specifically for that node should not expose this surface to its users.
- **No quota is enforced.** Usage is metered after the fact; the proxy's quota enforcement does not apply, as there is no routing decision to hang it on.
- **The relay depends on undocumented client behaviour** — forwarding the credential to an arbitrary base URL. Nothing guarantees it survives an upstream release.
