# Budgets and quotas

Budgets cap the spending of an organization, a team or a member. Once a cap is reached, new API requests are blocked.

> Budgets only apply to **Pay-as-you-go** providers. Subscription providers have their own plan limits.

## Budget hierarchy

Budgets work on several levels:

| Level | Description |
| --- | --- |
| Organization | Overall budget of the organization |
| Member | Individual budget of a member |
| Team | Envelope of a [team](../organisation/equipes.md), all its members included |
| Member in a team | Budget of a member for the requests they send to a team |

A request attributed to a team is checked against every level on its path: the organization budget, the member's, the team's and the member's in the team. It is refused as soon as one of them is reached. A request attributed to the organization alone does not read the team levels.

Each level can set a **daily**, **monthly** and/or **yearly** budget. A field left empty means "unlimited".

## Effective budget

For each period, Xolo takes the **minimum** of the budgets set:

- If the organization has a monthly budget of €100 and a member one of €50 → the member is limited to €50/month.
- If the member has no personal budget → they use the organization budget.

## Sharing the budget equally

The **Partager le budget équitablement** (Share the budget equally) option (in the organization [Settings](../organisation/parametres.md)) automatically divides the organization budget between all its members.

**Example**: an organization with €500/month and 5 members → each member has €100/month.

This rule only applies **to members without an individual quota**:

1. if a member has a personal quota → their quota is used;
2. otherwise → the shared quota (org budget ÷ number of members) is used.

> The share is recomputed automatically whenever the number of members changes.

A team has the same option, on its page: its envelope is then divided between its members without a budget of their own in the team.

The division is an integer one, rounded down to the millionth of the currency unit: shared between 3 members, a €1 budget gives €0.333333 to each. The remainder cannot be spent.

A team that shares its envelope but has no members left has nothing to divide: a request still attributed to it, by a bound application for instance, is bounded by the whole envelope.

## Distribution between levels

The budget of an organization constrains those of its teams, and the budget of a team those of its members, following the **distribution** rule chosen on the parent level. The rule applies when a budget is saved, a child's as well as the parent's:

| Rule | Effect |
| --- | --- |
| **Libre** (Free, default) | No constraint: only the limits checked on each request apply. |
| **Plafonnée** (Capped) | Each child budget is at most the parent's, period by period. Their sum may exceed it. |
| **Partagée** (Split) | The sum of the child budgets does not exceed the parent's, period by period: the envelope is really shared. |

Lowering a parent budget is therefore refused if the child budgets already saved no longer fit in it: lower them first.

A child without a budget of its own for a period is not constrained: it stays bounded by the parent on each request.

## Reading your own budget

A user reads their own budget in an organization with `GET /api/v1/quota?organization=<orgID>`. The route accepts the same authentications as the proxy: user token, OIDC ID token or session. An application can therefore show its users their consumption by relaying their ID token.

The response covers the user level only, that is the budget checked against the user's own counter:

- their personal quota when they have one (`"source": "personal"`);
- otherwise their share of the organization budget, when the organization shares it equally (`"shared"`);
- otherwise no budget (`"none"`).

Organization, team and team-member budgets have their own counters and are not returned. They may therefore block a request before the user's budget does.

When the organization budget is lower than the user's, requests stop at the organization's figure: `remaining` is then only an upper bound, and the refusal can be reported at the user level (`User daily budget exceeded`, for instance), since the user's counter is checked against the lower of the two.

```json
{
  "organization_id": "00000000-0000-0000-0000-000000000102",
  "source": "personal",
  "currency": "EUR",
  "periods": {
    "daily":   {"budget": 1000000, "spent": 120000, "remaining": 880000, "resets_at": "2026-10-14T00:00:00+02:00"},
    "monthly": {"budget": null, "spent": 2400000, "remaining": null, "resets_at": "2026-11-01T00:00:00+01:00"},
    "yearly":  {"budget": null, "spent": 9800000, "remaining": null, "resets_at": "2027-01-01T00:00:00+01:00"}
  }
}
```

Amounts are in millionths of the currency unit. `budget` and `remaining` are `null` when the period is unlimited, and `remaining` never goes below zero. `spent` counts the user's spending in the organization since the start of the period, excluding usage covered by a subscription. Periods follow the server's time zone.

The organization is chosen in this order:

1. the token's organization, for a token bound to one;
2. otherwise the `organization` parameter;
3. otherwise the user's only organization.

Without the parameter, a user of several organizations gets `400`. An organization they are not an active member of, or one other than the token's, gives `404`. An application token gets `403`, since its budget is the application's. No permission is required: everyone reads only their own budget.

Like `GET /api/v1/models`, this route is not subject to the per-IP rate limit. Applications often call it from a single address for all their users.

## Related permissions

| Action | Required permission |
| --- | --- |
| View budgets | `quota:read` |
| Edit budgets | `quota:write` |

See the [Budget](../organisation/budget.md) tutorial for the steps.
