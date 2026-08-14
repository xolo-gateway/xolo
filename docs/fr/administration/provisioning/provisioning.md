# API de provisioning

L'API de provisioning (*Provisionning API*) permet à un système externe — plan de contrôle, opérateur Kubernetes, provider Terraform, playbook Ansible ou simple script — de créer et de réconcilier les tenants, les organisations, les membres et les rôles d'une instance Xolo **sans aucune interaction humaine**.

Elle ne fait délibérément pas partie de l'API `/api/v1/` utilisée par l'interface web : son périmètre de sécurité est différent (privilèges à l'échelle de l'instance, aucun contexte utilisateur). Elle dispose donc de son propre écouteur, sur son propre port, avec sa propre configuration TLS et son propre mécanisme d'authentification.

```
Processus Xolo
├── serveur HTTP public      Interface web, OIDC, /api/v1, proxy LLM
└── serveur de provisioning  Écouteur et port dédiés, TLS mutuel
```

Les deux serveurs partagent les **mêmes instances de stockage** (caches et décorateurs d'événements compris) : aucune seconde connexion à la base de données.

> **Hiérarchie.** Un **tenant** contient des **organisations**, qui contiennent des **membres** et des **rôles**. Les utilisateurs appartiennent au tenant, pas à l'organisation : le couple `(provider, subject)` n'est unique qu'au sein d'un tenant, si bien qu'une même personne connectée sur deux tenants dispose de deux comptes distincts.
>
> Par défaut, une instance ne possède qu'un seul tenant, `default`, créé automatiquement à la migration. Il est invisible pour les utilisateurs — aucun sous-domaine, aucune URL modifiée — mais c'est lui qui fournit le `{tenantID}` attendu par les routes ci-dessous.

## Authentification : TLS mutuel

TLS mutuel, et rien d'autre. Pas d'OIDC, pas de session, pas de cookie, pas de jeton d'API utilisateur sur ce port — et aucune route de provisioning n'est montée sur le port HTTP public. Il n'existe aucun accès anonyme.

L'écouteur est configuré en `RequireAndVerifyClientCert` : la couche TLS rejette toute connexion sans certificat client, ou dont le certificat n'est pas signé par l'autorité configurée, avant même qu'un handler ne s'exécute.

**Tout client porteur d'un certificat valide administre l'instance entière.** Son identité (*common name*, numéro de série, sujet) est enregistrée dans les journaux, mais n'est pas utilisée pour des décisions d'autorisation à ce jour. Traitez la clé privée du client comme un secret d'administration.

Le matériel TLS est chargé au démarrage : un certificat, une clé ou un bundle d'autorité manquant ou incohérent provoque un échec au démarrage, jamais à la première requête.

## Configuration

| Variable | Défaut | Description |
| --- | --- | --- |
| `XOLO_PROVISIONNING_API_ENABLED` | `false` | Ouvre l'écouteur d'administration. |
| `XOLO_PROVISIONNING_API_ADDRESS` | `:3003` | Adresse d'écoute. |
| `XOLO_PROVISIONNING_API_TLS_CERT_FILE` | _(requis si activé)_ | Certificat serveur (PEM). |
| `XOLO_PROVISIONNING_API_TLS_KEY_FILE` | _(requis si activé)_ | Clé privée du serveur (PEM). |
| `XOLO_PROVISIONNING_API_TLS_CLIENT_CA_FILE` | _(requis si activé)_ | Autorité vérifiant les certificats clients. |
| `XOLO_PROVISIONNING_API_SHUTDOWN_TIMEOUT` | `10s` | Délai d'arrêt gracieux. |

Le multi-tenant se configure au niveau de l'instance, pas de cette API :

| Variable | Défaut | Description |
| --- | --- | --- |
| `XOLO_MULTITENANCY_ENABLED` | `false` | Autorise plus d'un tenant. |
| `XOLO_MULTITENANCY_HOST_PATTERN` | _(requis si activé)_ | Modèle de nom d'hôte, par exemple `{tenant}.xolo.example.com`. |
| `XOLO_MULTITENANCY_DEFAULT_TENANT_SLUG` | `default` | Tenant servi lorsque le multi-tenant est désactivé. |

N'exposez pas ce port sur un réseau public : réservez-le au réseau d'administration ou au maillage de services interne.

## Endpoints

| Méthode | Route | Notes |
| --- | --- | --- |
| `GET` | `/v1/healthz` | Également derrière TLS mutuel. |
| `GET` | `/v1/permissions` | Catalogue RBAC : seule source des codes de permission valides. |
| `GET` | `/v1/tenants` | `?slug=` pour une recherche exacte, sinon `?page=&limit=`. |
| `POST` | `/v1/tenants` | **Refusé avec `409` sur une instance mono-tenant.** |
| `GET` | `/v1/tenants/{tenantID}` | |
| `PATCH` | `/v1/tenants/{tenantID}` | `name`, `description`, `active`. Le slug est immuable. |
| `DELETE` | `/v1/tenants/{tenantID}` | Supprime le tenant et tout ce qu'il contient. |
| `GET` | `/v1/tenants/{tenantID}/organizations` | `?slug=` pour une recherche exacte, sinon `?page=&limit=`. |
| `POST` | `/v1/tenants/{tenantID}/organizations` | Crée l'organisation, ses rôles intégrés et, optionnellement, son propriétaire initial. |
| `GET` | `/v1/tenants/{tenantID}/organizations/{orgID}` | |
| `PATCH` | `/v1/tenants/{tenantID}/organizations/{orgID}` | `name`, `description`, `active`, `currency`, `shareQuotaEqually`. Le slug est immuable. |
| `DELETE` | `/v1/tenants/{tenantID}/organizations/{orgID}` | Supprime l'organisation et toutes ses données. |
| `GET` | `/v1/tenants/{tenantID}/organizations/{orgID}/members` | Paginé. |
| `POST` | `/v1/tenants/{tenantID}/organizations/{orgID}/members` | `userId` **ou** `user{provider,subject,…}`, plus `roleIds[]` et/ou `builtinRoles[]`. |
| `GET` | `/v1/tenants/{tenantID}/organizations/{orgID}/members/{membershipID}` | |
| `PUT` | `/v1/tenants/{tenantID}/organizations/{orgID}/members/{membershipID}/roles` | Remplacement complet du jeu de rôles. |
| `DELETE` | `/v1/tenants/{tenantID}/organizations/{orgID}/members/{membershipID}` | |
| `GET` | `/v1/tenants/{tenantID}/organizations/{orgID}/roles` | Rôles intégrés et personnalisés. |
| `POST` | `/v1/tenants/{tenantID}/organizations/{orgID}/roles` | Rôle personnalisé. |
| `GET` | `/v1/tenants/{tenantID}/organizations/{orgID}/roles/{roleID}` | |
| `PUT` | `/v1/tenants/{tenantID}/organizations/{orgID}/roles/{roleID}` | Rôles personnalisés uniquement. |
| `DELETE` | `/v1/tenants/{tenantID}/organizations/{orgID}/roles/{roleID}` | Rôles personnalisés uniquement. |
| `GET` | `/v1/tenants/{tenantID}/users` | `?provider=&subject=` pour une recherche exacte, sinon `?search=&active=&page=&limit=`. |
| `PUT` | `/v1/tenants/{tenantID}/users` | Upsert idempotent sur `(provider, subject)` : `201` à la création, `200` sinon. |
| `GET` | `/v1/tenants/{tenantID}/users/{userID}` | |
| `PATCH` | `/v1/tenants/{tenantID}/users/{userID}` | `email`, `displayName`, `active`. |

### Instances mono-tenant

Sur une installation par défaut, commencez par récupérer l'identifiant du tenant unique :

```bash
curl -s --cacert dev-pki/ca.crt --cert dev-pki/client.crt --key dev-pki/client.key \
  "https://localhost:3003/v1/tenants?slug=default"
```

Toutes les routes ci-dessus s'utilisent ensuite avec cet identifiant. La création d'un second tenant est refusée avec `409` tant que `XOLO_MULTITENANCY_ENABLED` vaut `false` : aucun nom d'hôte ne permettrait de l'atteindre, ses organisations seraient donc inaccessibles.

Les charges utiles sont en JSON *camelCase*, les horodatages au format RFC 3339, et les collections sont renvoyées sous la forme `{"items": […], "page": 1, "limit": 50, "total": 123}`. Les champs inconnus sont rejetés : un nom de champ mal orthographié est signalé plutôt qu'ignoré silencieusement.

### Erreurs

Toutes les erreurs utilisent la même enveloppe :

```json
{"error": {"code": "conflict", "message": "organization with slug \"acme\" already exists in this tenant (id: c9m2…)"}}
```

| Code | HTTP | Cause |
| --- | --- | --- |
| `invalid_request` | 400 | Corps mal formé, champ inconnu, paramètre de requête invalide. |
| `unauthorized` | 401 | Aucun certificat client vérifié. |
| `not_found` | 404 | Ressource inconnue, ou appartenant à un autre tenant ou à une autre organisation. |
| `method_not_allowed` | 405 | Ressource connue, mauvaise méthode. |
| `conflict` | 409 | Ressource existante, ou invariant métier qui refuse la modification. |
| `unprocessable` | 422 | Valeur bien formée mais refusée par le domaine. |
| `internal_error` | 500 | Échec inattendu. |

Les messages sont toujours construits explicitement : ni traces d'exécution, ni erreurs SQL, ni chemins de fichiers, ni détails TLS, ni secrets ne parviennent au client. Le détail complet est journalisé côté serveur.

## Modèle d'identité

Un utilisateur est identifié par le couple `provider` + `subject`, la même clé que celle utilisée par l'authentification interactive : un utilisateur provisionné peut donc se connecter ensuite. L'API n'offre délibérément aucune identité fondée sur l'email — `email` est un champ de profil, jamais un identifiant.

### Provisionner avant la première connexion

`POST /v1/tenants/{tenantID}/organizations` crée son propriétaire avant même que cette personne ne se connecte. Cela suppose que l'appelant connaisse son `subject` à l'avance, ce qui est le cas lorsque le plan de contrôle pilote aussi le fournisseur d'identité, ou lorsque le sujet est dérivé de façon déterministe.

Lorsque le sujet ne peut pas être connu à l'avance, ne désactivez pas `XOLO_HTTP_AUTHN_AUTO_CREATE_USERS` : les personnes concernées ne pourraient plus se connecter. Utilisez plutôt `XOLO_HTTP_AUTHN_ACTIVE_BY_DEFAULT=false`. Le compte est alors créé à la première connexion mais reste inactif et n'accorde aucun droit. Le plan de contrôle le récupère avec `GET /v1/tenants/{tenantID}/users?active=false`, le rattache à une organisation via `POST /v1/tenants/{tenantID}/organizations/{orgID}/members`, puis l'active avec `PATCH /v1/tenants/{tenantID}/users/{userID} {"active": true}`.

## Invariants

- Provisionner un administrateur d'organisation **n'accorde jamais** de privilèges à l'échelle de la plateforme. Un utilisateur créé via cette API reçoit exactement le rôle plateforme `user`, et les rôles plateforme d'un utilisateur existant ne sont jamais modifiés.
- Les adresses listées dans `XOLO_HTTP_AUTHN_DEFAULT_ADMINS` sont **réservées** : l'API refuse (`422`) de les affecter à un utilisateur, faute de quoi ce dernier deviendrait administrateur plateforme à sa prochaine connexion.
- Une organisation conserve toujours au moins un propriétaire : retirer ou rétrograder le dernier est refusé avec un `409`.
- Un rôle ne peut être affecté qu'à un membre de l'organisation à laquelle il appartient. Tout autre cas donne un `422`, sans qu'aucun rôle ne soit modifié.
- Un membre ou un rôle appartenant à une autre organisation est signalé comme `404`, tout comme une organisation ou un utilisateur appartenant à un autre tenant.
- Le tenant `default` ne peut être ni supprimé ni désactivé : c'est celui vers lequel toute instance mono-tenant se rabat.
- Les rôles intégrés ne peuvent être ni modifiés, ni supprimés.
- Seuls les codes de permission présents dans le catalogue RBAC sont acceptés.

## Réconciliation

Les identifiants sont stables, `PUT /v1/tenants/{tenantID}/users` est idempotent, la création d'un tenant ou d'une organisation sur un slug existant répond `409` en incluant l'identifiant existant, et les endpoints de lecture permettent de relire l'état courant intégralement. Le seul effet de bord de `POST /v1/tenants/{tenantID}/organizations` est documenté : la création des rôles intégrés de l'organisation.

La création d'une organisation orchestre plusieurs magasins de données sans transaction transverse. Tout échec survenant après la création de la ligne d'organisation déclenche une compensation au mieux (l'organisation est supprimée, les rattachements suivent en cascade), journalisée si elle échoue à son tour. Un utilisateur préexistant n'est jamais supprimé.

## Mise en place d'une PKI de développement

```bash
mkdir -p dev-pki && cd dev-pki

# Autorité de certification
openssl req -x509 -newkey rsa:4096 -nodes -days 365 \
  -keyout ca.key -out ca.crt -subj "/CN=xolo-dev-ca"

# Certificat serveur
openssl req -newkey rsa:4096 -nodes -keyout server.key -out server.csr \
  -subj "/CN=localhost"
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out server.crt -days 365 \
  -extfile <(printf "subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth")

# Certificat client
openssl req -newkey rsa:4096 -nodes -keyout client.key -out client.csr \
  -subj "/CN=control-plane"
openssl x509 -req -in client.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out client.crt -days 365 \
  -extfile <(printf "extendedKeyUsage=clientAuth")
```

Démarrage du serveur avec l'API activée :

```bash
XOLO_SECRET_KEY=$(openssl rand -hex 32) \
XOLO_PROVISIONNING_API_ENABLED=true \
XOLO_PROVISIONNING_API_TLS_CERT_FILE=dev-pki/server.crt \
XOLO_PROVISIONNING_API_TLS_KEY_FILE=dev-pki/server.key \
XOLO_PROVISIONNING_API_TLS_CLIENT_CA_FILE=dev-pki/ca.crt \
bin/server
```

Premiers appels :

```bash
CURL="curl -s --cacert dev-pki/ca.crt --cert dev-pki/client.crt --key dev-pki/client.key"

# Refusé : aucun certificat client
curl -sk https://localhost:3003/v1/permissions

# Accepté : on récupère d'abord l'identifiant du tenant
$CURL "https://localhost:3003/v1/tenants?slug=default"
TENANT=…  # l'identifiant lu ci-dessus

# Puis on crée une organisation et son propriétaire
$CURL -X POST "https://localhost:3003/v1/tenants/$TENANT/organizations" \
  -d '{"slug":"acme","name":"Acme","owner":{"provider":"openid-connect","subject":"sub-123","email":"owner@acme.tld","displayName":"Owner"}}'
```

En production, utilisez une autorité de certification gérée (Vault, cert-manager, PKI interne) et faites tourner les certificats clients.

## Hors périmètre actuel

- Les fournisseurs, modèles LLM, modèles virtuels, middlewares, applications et leurs jetons, quotas, alertes et paramètres d'événements : ils restent gérés par l'interface web.
- Les portées par certificat : tout certificat valide administre l'instance entière.
- Les mutations effectuées via cette API n'émettent aucun événement Xolo (elles sont journalisées côté serveur) : c'est le comportement documenté lorsqu'aucun utilisateur n'est présent dans le contexte.
- Le pré-provisionnement par email : le mécanisme d'[invitation](../organisation/invitation/invitation.md) reste la voie par email, via l'interface web.
- Aucune spécification OpenAPI n'est générée à ce jour.
