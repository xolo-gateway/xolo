# Applications

## What is an application?

An application is an M2M (machine-to-machine) configuration that lets external services connect to Xolo. Each application has its own API tokens for authentication.

### Use cases

- **OpenWebUI**: integration with the OpenWebUI web interface
- **Automated scripts**: scripts or services that call the Xolo API
- **CI/CD**: integration in development pipelines
- **Custom applications**: any application that needs programmatic access

## Accessing applications

1. Go to your organization: `/orgs/{slug}/`
2. Click **Applications** in the admin menu

> **Note**: You need the `applications:write` permission to create or edit applications.

## Creating an application

1. Click **Nouvelle application** (New application)
2. Fill in the information:

| Field           | Description                                          |
| --------------- | ---------------------------------------------------- |
| **Nom** (Name)  | Name of the application (e.g. "OpenWebUI Production") |
| **Description** | Optional description                                 |
| **Équipe** (Team) | Team the application is bound to (optional)        |

3. Click **Enregistrer** (Save).

An application bound to a [team](../equipes/equipes.md) uses the team's models, and all its requests are charged to the team's budget. It cannot name another team. Its roles still govern the admin sections it reaches and, with `model:use:org`, access to every model of the organization.

## Managing API tokens

Each application can have several API tokens.

### Generating a token

1. Open the application for editing
2. In the **Jetons d'API** (API tokens) section, fill in:
   - **Libellé** (Label): a descriptive name (e.g. "Production token")
   - **Expiration (jours)** (Expiration in days): validity period (e.g. 365 for 1 year)
3. Click **Générer un jeton** (Generate a token)

### Revoking a token

To revoke a token (make it invalid immediately):

- Click the **Révoquer** (Revoke) icon (bin) on the token's row, then confirm

> **Warning**: A revoked token cannot be recovered. You have to generate a new one.

### Token expiration

| Situation                 | Behaviour                                         |
| ------------------------- | ------------------------------------------------- |
| **Expiration date set**   | The token becomes invalid on that date            |
| **No expiration**         | The token stays valid until it is revoked manually |

## Editing or disabling an application

1. Click **Modifier** (Edit) on the application's row
2. Change the information:
   - **Nom** (Name): new name of the application
   - **Description**: new description
   - **Active**: check or uncheck to enable or disable it

> **Note**: A disabled application can no longer use its API tokens.

## Deleting an application

1. Open the application for editing
2. In the **Zone de danger** (Danger zone), click **Supprimer l'application** (Delete the application)

> **Warning**: This also deletes all the associated tokens. Services using these tokens lose their access immediately.

## Application tokens vs user tokens

Xolo offers two kinds of authentication tokens:

| Property        | Application tokens                                  | User API tokens                      |
| --------------- | --------------------------------------------------- | ------------------------------------ |
| **Creation**    | By an admin, in the application settings            | By each user, in their profile       |
| **Expiration**  | Configurable (e.g. 365 days)                        | Configurable, or unlimited           |
| **Use case**    | M2M services (OpenWebUI, scripts)                   | Personal API access                  |
| **Identity**    | The application is identified, not the user         | The user is identified               |

### Recommendations

- Use **application tokens** for automated services (CI/CD, scripts, OpenWebUI)
- Use **user API tokens** for personal API access

## Using API tokens

### Authentication format

Send the token in the HTTP `Authorization` header:

```bash
Authorization: Bearer <your_api_token>
```

### Example with curl

```bash
curl -X POST https://xolo.example.com/v1/chat/completions \
  -H "Authorization: Bearer xlo_abc123def456" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "my-org/gpt-4o",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

### Token scope and the `GET /api/v1/models` endpoint

A token (application or user) is bound to **a single organization**: the one it was issued in. This scope applies the same way to every request authenticated by the token, including `GET /api/v1/models`, which only returns the enabled models of that organization.

In practice:

- **Application token**: `GET /api/v1/models` returns the enabled models of the application's organization.
- **Token of a user in several organizations**: `GET /api/v1/models` only returns the enabled models of the token's organization (the user's other memberships are ignored, as by the proxy). Personal virtual models (`~/...`) follow a separate rule: they are included as soon as the user has the `personal-vm:create` permission (or the owner role) in **at least one** of the organizations in the scope above. The list is however not filtered by organization: the data model (`internal/core/model/personal_virtual_model.go`) ties each personal virtual model to its `user_id` without an `OrgID` column, so every `~/...` of the user is returned.
- **OIDC session (human user)**: `GET /api/v1/models` returns the union of the enabled models of every organization the user is a member of.
- **Teams**: a model reachable through a team is listed as `<organization>/<team>/<model>`, once per team; a model reachable through `model:use:org` is listed as `<organization>/<model>`. A token bound to a team only sees the names of its team.
- **Session from `POST /auth/token/login`**: the session is created from a Xolo token (user or application) and `authn.User.OrgID` holds the token's organization (`internal/http/middleware/authn/token/login.go`). `GET /api/v1/models` then only returns the enabled models of that organization, as in the matching "Token" case (the user's other memberships are ignored; for an application, it is precisely the application's organization). This case differs from the previous one only by the authentication channel (a session cookie instead of the `Authorization` header).

### Capabilities and pricing of the listed models

Each model of `GET /api/v1/models` follows the OpenRouter format:

- `description`;
- `context_length`;
- `top_provider.context_length` and `top_provider.max_completion_tokens`, the context window and the most tokens the model can write in one answer, to size `max_tokens` with. A virtual model publishes the smallest windows of its candidates, or its `MaxOutputTokens` override. An unknown window is left out, and only completion models have the object. `per_request_limits` stays the token limit the operator set;
- `architecture.input_modalities`, which contains `image` when the model reads images;
- `supported_parameters`, which contains `tools` and `reasoning` according to its capabilities;
- `pricing.prompt` and `pricing.completion`, in currency units per 1,000 tokens.

Xolo adds a `xolo` object:

```json
"xolo": {"capabilities_declared": true, "currency": "EUR"}
```

- `capabilities_declared` is `false` for a virtual model whose capabilities cannot be derived from its pipeline and that declares none in its catalogue. Its card then reads as a free, text-only model: these zero values mean "unknown", not "absent".
- `currency` is the currency of the model's provider. It is missing when the price is unknown, for instance for an underived virtual model, for candidates billed in different currencies, or when the list of providers could not be read.

> **Note**: Before bug [#48](https://github.com/xolo-gateway/xolo/issues/48) was fixed,
> an application token got an empty list from `GET /api/v1/models`
> because the "shadow" user behind the application has no organization
> membership. This is now fixed: the application sees the models of its
> organization, as the proxy already did.

## Integration with OpenWebUI

OpenWebUI can be configured to use Xolo as its LLM backend.

### Configuration

In the OpenWebUI configuration file (`openwebui.ini` or environment variables):

```ini
# Xolo base URL
XOLO_BASE_URL=https://xolo.example.com

# API token of the Xolo application
XOLO_API_TOKEN=xlo_your_application_token

# Default model (optional)
DEFAULT_MODEL=my-org/gpt-4o
```

### Environment variables

You can also use environment variables:

```bash
export XOLO_BASE_URL=https://xolo.example.com
export XOLO_API_TOKEN=xlo_your_application_token
```

### Checking

Once configured, the Xolo models should appear in the list of models available in OpenWebUI.

## Permissions

| Action                     | Required permission  |
| -------------------------- | -------------------- |
| View applications          | `applications:read`  |
| Create, edit, delete       | `applications:write` |
