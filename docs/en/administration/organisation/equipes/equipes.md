# Teams

## What is a team?

A team groups members of the organization, the models and middlewares they use, and the budget they consume. A member can belong to several teams.

A role says what a member can do. A team says with what. Model access goes through teams: a member uses a model if one of their teams grants access to it. The `model:use:org` and `model:use:virtual` permissions remain the only access that does not go through a team. They open every model of the organization, and the builtin **Membre** (Member) role carries them.

Teams do not nest.

## Choosing the charged team

Each request is attributed to a single team, or to the organization alone. The model name carries the team: `<organization>/<team>/<model>`, for example `acme/legal/gpt-4o`. A member of several teams chooses the charged team by choosing the model, conversation by conversation, in any client (OpenWebUI, the OpenAI and Anthropic SDKs, Claude Code).

- `/v1/models` lists one name per team that grants access to the model, in the qualified form. Members who have `model:use:org` also see the short form `acme/gpt-4o`, charged to the organization alone.
- The short form `acme/gpt-4o` is still accepted when only one path exists: the token is attached to a team, only one of the member's teams grants access to the model, or the member has `model:use:org` (only the organization is then charged). The same member naming one of their teams, for instance `acme/legal/gpt-4o`, charges that team, even when it does not grant the model. Otherwise, the gateway answers `400` with the code `model_ambiguous` and the list of usable qualified names.
- An API key or an application attached to a team always charges that team, even when its holder has `model:use:org`, and cannot name another team.
- A personal virtual model (`~/…`) charges the team chosen in its settings. If its author leaves that team, the team is deleted, or it belongs to another organization than the request's, its requests are refused (`400 model_team_required`) until the author picks another team.

The identifier of a team can be changed on its page, with the same rules as at creation. Clients store model names in their configuration: those using the former identifier stop working and must be updated. The tokens, applications, personal virtual models and budgets attached to the team follow it unchanged.

> **Name conflicts.** A model name can contain a slash, like a vLLM model declared under its Hugging Face identifier (`meta/llama-3`). Xolo refuses a team whose identifier would start an existing model name (`meta`), and a model name that would start with a team identifier followed by `/`.

## Creating a team

1. Go to your organization and click **Équipes** (Teams) in the **Gouvernance** (Governance) menu.
2. Click **Nouvelle équipe** (New team).
3. Enter the name, the identifier (lowercase letters, digits and hyphens) and a description.
4. Click **Créer l'équipe** (Create team).

> **Note**: You need the `teams:write` permission to create a team or change what it uses.

## Granting access to models and middlewares

On the team page, the **Modèles accessibles** (Accessible models) section lists the real and virtual models of the organization. Check the ones the team can use, then click **Enregistrer** (Save).

A virtual model delegates access to the models its pipeline references in the configuration of its nodes. A team that has access to the virtual model can use these models through it, without direct access to them. A model name computed during the request (by a plugin or a value node) is checked against the team's access. See [Virtual models](../virtual_model/virtual_model.md).

The **Middlewares de l'équipe** (Team middlewares) section attaches middlewares to the team. A middleware attached to at least one team only applies to the requests of these teams. It runs after the organization middlewares, which carry the compliance rules and apply to every request. A middleware attached to this team alone cannot be unchecked: with no team left, it would apply to the whole organization. Attach it to another team first, or delete it.

## Managing members

The **Membres** (Members) section lists the members of the team. Choose a member of the organization and click **Ajouter à l'équipe** (Add to team). The remove icon removes a member from the team and deletes their budget in the team. They stay a member of the organization.

### Roles in the team

A role assigned to a member from the team page only applies to that team. Only its permissions marked "team" in the role editor apply:

| Permission | Effect on the team |
| --- | --- |
| `members:read`, `members:write` | View and manage the team members and their roles in the team |
| `quota:read`, `quota:write` | View the team budget and distribute it among its members |
| `usage:read` | View the usage of the team's requests |
| `events:read:all` | View the events of the team members |
| `invites:read`, `invites:write` | Invite people who join the organization and the team, as members |

A "Team lead" role carrying `members:write`, `quota:write` and `usage:read`, assigned on the legal team, lets its holder manage that team only.

The organization allocates, the team distributes. A team lead can never widen what the team uses or its envelope. They can only assign roles whose permissions they hold on the team themselves.

## Team budget

The budgets of the organization, of the team and of a member in the team are checked on every request. A request from the legal team is refused as soon as one of the levels is reached.

- **Team envelope.** You need `quota:write` on the organization to set it.
- **Distribution.** You need `quota:write`, on the organization or on the team, to set the budget of a member in the team (pencil icon on the member's row), choose the distribution rule and enable equal sharing.

The **Partager l'enveloppe à parts égales** (Share the envelope equally) option divides the team budget among its members who have no budget of their own in the team.

The **distribution** rule constrains member budgets when they are saved:

| Rule | Effect |
| --- | --- |
| **Libre** (Free) | No constraint. Only the limits checked on each request apply. |
| **Plafonnée** (Capped) | Each member's budget is at most equal to the team budget. Their sum can exceed it. |
| **Partagée** (Split) | The sum of the member budgets does not exceed the team budget. |

The rule works both ways: lowering the team budget is refused when the member budgets no longer fit in it.

The same rule can be set on the organization budget, for the envelopes of its teams. See [Budgets and quotas](../../../concepts/budgets-et-quotas.md).

## API keys and applications

- An **API key** can be attached to one of your teams when you create it, from **Profil > Jetons API** (Profile > API tokens). If you leave the team or it is deleted, the key is refused and the list says so ("équipe quittée ou supprimée", team left or deleted): delete it and create another one. A key never changes team on its own.
- An **application** can be attached to a team from its page. It then uses the team's models and all its requests are charged to the team.
- A **personal virtual model** chooses the team it charges in its settings. It delegates nothing: each model it calls must be accessible to that team.

A team that has applications attached to it cannot be deleted. Attach them elsewhere first. The same goes while a middleware is attached to this team alone: with no team left, it would apply to the whole organization. Attach it to another team, or delete it.

## Permissions

| Action | Required permission |
| --- | --- |
| View teams | `teams:read` |
| Create, edit, delete a team, choose its models and middlewares | `teams:write` |
| Manage the members of a team | `members:write`, on the organization or the team |
| Set the envelope of a team | `quota:write` on the organization |
| Distribute the budget of a team | `quota:write`, on the organization or the team |

## Migrating from restricted role access

Before teams, a custom role could grant specific models. The upgrade turns each custom role that granted models into a team, with the same models and the role holders as members. An application that held such a role is attached to its team. An application that held several gets its own team combining their models. Nobody loses access. Roles keep their permissions.

A member who held several of these roles now belongs to several teams. If two of them grant access to the same model, the short form of the name becomes ambiguous and the member must name the team.

> **Note**: Stop the other Xolo instances during the upgrade. The migration drops the table of role access, which an older instance still reads. It cannot be rolled back: back up the database before the upgrade to be able to return to the previous version.
