# Enrollment rules

## What is an enrollment rule?

An enrollment rule makes an identity a member of your organization at sign-in, with no invitation and no administrator action. It pairs a condition, on the email domain or an OIDC claim, with a role.

A rule also applies to OIDC tokens presented directly to the API: a chat application passing the token of each person has nothing else to do.

## Reaching the rules

1. Go to your organization: `/orgs/{slug}/`
2. Click **Enrollment rules** in the admin menu

> **Note**: reading the rules takes `invites:read`, managing them `invites:write`, on the whole organization.

## Creating a rule

| Field | Description |
|-------|-------------|
| **Name** | To find it in the list |
| **Condition** | Email domain or OIDC claim |
| **Claim name** | For a claim condition, for example `groups` |
| **Domain or value** | `example.org`, or the expected value of the claim |
| **Role** | Role of the new member: any role of the organization but Owner |
| **Enabled** | A disabled rule no longer applies |

### Email domain

The rule only applies to an email the identity provider asserts as verified (`email_verified`). The domain is compared as is: `example.org` does not cover `sub.example.org`.

### OIDC claim

The value must equal the claim, or one of its values for a list such as `groups`. A boolean compares with `true` or `false`.

Claims are only read for a provider proving its issuer: on GitHub, the account holder writes their profile themselves. Rules only take them into account for OIDC providers, Gitea with a discovery document, and Google. At an interactive sign-in, the claims are kept in the session cookie: those that do not fit (1 KiB in total, 32 values per claim) are ignored, and a warning in the server logs names them. An ID token presented to the API is not stored and keeps larger claims (16 KiB, 256 values per claim); beyond that, a claim rules compare is reduced to the values they compare, and a warning names the one that still does not fit.

To enroll every account of an identity provider, use the `iss` claim with the URL of its issuer.

## What a rule does

When an identity matches an enabled rule:

- it becomes a member of the organization, with the role of the rule;
- the account its sign-in creates is activated, even with `XOLO_HTTP_AUTHN_ACTIVE_BY_DEFAULT=false`; an inactive account that already existed stays inactive, as an administrator may have deactivated it;
- the event `member.enrolled` is recorded in the events explorer.

## A rule acts once per account

**What an administrator changes afterwards prevails over the rule.** A member removed from the organization does not come back at their next sign-in, even through another rule of the organization, and a deactivated account stays deactivated.

An existing member keeps their role and status, suspended included: the rule neither changes them nor activates the account.

A rule created later applies to existing accounts at their next sign-in. Changing a rule does not replay it on the accounts it already handled. Deleting a rule keeps the members it enrolled, and the organization remembers them: a rule created again does not bring back those an administrator removed.

## Limits

- A changed rule can take up to 30 seconds to apply on the other replicas.
- A custom role can not be deleted while a rule grants it: change or delete the rule first.
- When members or memberships belong to the control plane (`XOLO_OWNERSHIP`), rules do not apply and their pages are hidden.
- With several tenants, a rule only concerns the tenant of its organization.
- A rule that fails at the sign-in creating an account is retried on the following requests. After an hour it no longer activates that account: an administrator then has to.

Rules can also be managed through the [provisioning API](../../provisioning/provisioning.md#business-resources).
