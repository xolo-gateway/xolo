# API de provisioning

L'API de provisioning (*Provisionning API*) permet à un système externe — plan de contrôle, opérateur Kubernetes, provider Terraform, playbook Ansible ou simple script — de créer et de réconcilier les tenants, leurs domaines, les organisations, les membres et les rôles d'une instance Xolo **sans aucune interaction humaine**.

Elle expose un **contrat commun** — des `PUT` idempotents de tenants, domaines, organisations, membres et adhésions, identifiés par des UUID choisis par le client — et, sous `/v1/xolo`, les opérations propres à Xolo. Ce contrat remplace les routes précédentes : voir [Migration depuis les routes précédentes](#migration-depuis-les-routes-precedentes).

Elle ne fait délibérément pas partie de l'API `/api/v1/` utilisée par l'interface web : son périmètre de sécurité est différent (privilèges à l'échelle de l'instance, aucun contexte utilisateur). Elle dispose donc de son propre écouteur, sur son propre port, avec sa propre configuration TLS et son propre mécanisme d'authentification.

```
Processus Xolo
├── serveur HTTP public      Interface web, OIDC, /api/v1, proxy LLM
└── serveur de provisioning  Écouteur et port dédiés, TLS mutuel
```

Les deux serveurs partagent les **mêmes instances de stockage** (caches et décorateurs d'événements compris) : aucune seconde connexion à la base de données.

> **Hiérarchie.** Un **tenant** contient des **organisations**, qui contiennent des **membres** et des **rôles**. Les utilisateurs appartiennent au tenant, pas à l'organisation : le couple `(provider, subject)` n'est unique qu'au sein d'un tenant, si bien qu'une même personne connectée sur deux tenants dispose de deux comptes distincts.
>
> Par défaut, une instance ne possède qu'un seul tenant, `default`, créé automatiquement à la migration. Il est invisible pour les utilisateurs — aucun sous-domaine, aucune URL modifiée — mais c'est lui qui fournit le `{tenantID}` attendu par les routes ci-dessous. Son identifiant se lit avec `GET /v1/xolo/tenants?slug=default`.

## Authentification : TLS mutuel

TLS mutuel, et rien d'autre. Pas d'OIDC, pas de session, pas de cookie, pas de jeton d'API utilisateur sur ce port — et aucune route de provisioning n'est montée sur le port HTTP public. Il n'existe aucun accès anonyme.

L'écouteur impose **TLS 1.3**, un certificat client vérifié par l'autorité configurée et
**exactement un URI SAN** correspondant à une entrée de
`XOLO_PROVISIONNING_API_AUTHORIZED_URIS`. Le Common Name ne sert jamais à autoriser.
Les contrôles sont effectués au handshake puis dans le middleware de toutes les
routes, y compris santé, permissions, routes inconnues et méthodes refusées. Un
refus HTTP utilise `403` et le code `client_certificate_rejected`.

Un client autorisé administre l'instance entière, avec vérification des parents
tenant et organisation. Il n'y a pas de permissions par certificat. Un certificat
renouvelé avec le même URI conserve son identité et son budget de requêtes.

**Mise à niveau obligatoire si l'écouteur est déjà activé :** réémettez les
certificats clients avec un URI SAN unique et configurez la liste autorisée avant
le redémarrage. Une liste vide, un URI invalide ou un doublon bloque le démarrage.
Les clients doivent prendre en charge TLS 1.3.

Le matériel TLS est chargé au démarrage : un certificat, une clé ou un bundle d'autorité manquant ou incohérent provoque un échec au démarrage, jamais à la première requête.

## Configuration

| Variable | Défaut | Description |
| --- | --- | --- |
| `XOLO_PROVISIONNING_API_ENABLED` | `false` | Ouvre l'écouteur d'administration. |
| `XOLO_PROVISIONNING_API_ADDRESS` | `:3003` | Adresse d'écoute. |
| `XOLO_PROVISIONNING_API_TLS_CERT_FILE` | _(requis si activé)_ | Certificat serveur (PEM). |
| `XOLO_PROVISIONNING_API_TLS_KEY_FILE` | _(requis si activé)_ | Clé privée du serveur (PEM). |
| `XOLO_PROVISIONNING_API_TLS_CLIENT_CA_FILE` | _(requis si activé)_ | Autorité vérifiant les certificats clients. |
| `XOLO_PROVISIONNING_API_AUTHORIZED_URIS` | _(requis si activé)_ | URI absolus distincts, séparés par des virgules. |
| `XOLO_PROVISIONNING_API_RATE_LIMIT` | `10` | Requêtes par seconde, par URI et par processus. |
| `XOLO_PROVISIONNING_API_RATE_BURST` | `20` | Rafale autorisée par URI. |
| `XOLO_PROVISIONNING_API_SHUTDOWN_TIMEOUT` | `10s` | Délai d'arrêt gracieux. |

Le débit et la rafale s'appliquent à chaque processus : avec N réplicas, un même URI dispose de N fois les valeurs configurées.

Le multi-tenant se configure au niveau de l'instance, pas de cette API :

| Variable | Défaut | Description |
| --- | --- | --- |
| `XOLO_MULTITENANCY_ENABLED` | `false` | Autorise plus d'un tenant et route les requêtes par domaine. |
| `XOLO_MULTITENANCY_HOST_PATTERN` | — | Mise à niveau uniquement : développé une seule fois en un domaine par tenant existant, voir [Domaines et routage](#domaines-et-routage). |
| `XOLO_MULTITENANCY_DEFAULT_TENANT_SLUG` | `default` | Tenant servi lorsque le multi-tenant est désactivé. |

N'exposez pas ce port sur un réseau public : réservez-le au réseau d'administration ou au maillage de services interne.

## Contrat commun

| Méthode | Route | Corps |
| --- | --- | --- |
| `GET` | `/v1/manifest` | — renvoie `{"name","version","contract_version"}` |
| `PUT` | `/v1/tenants/{tenantID}` | `{"slug","name","status"}` |
| `PUT` | `/v1/tenants/{tenantID}/domains/{hostname}` | `{"status"}` |
| `PUT` | `/v1/tenants/{tenantID}/organizations/{orgID}` | `{"slug","name","status"}` |
| `PUT` | `/v1/tenants/{tenantID}/members/{memberID}` | `{"email","tenant_role","status"}`, `"display_name"` facultatif |
| `PUT` | `/v1/tenants/{tenantID}/organizations/{orgID}/members/{memberID}` | `{"role","status"}` |

- **Les identifiants** sont des UUID canoniques en minuscules choisis par le
  client. Toute autre valeur est refusée avec `400 invalid_parameter`. Un membre
  est un utilisateur : `memberID` est l'identifiant de l'utilisateur.
- **Chaque `PUT` répond `200`**, création comprise, avec la représentation
  enregistrée. Un `PUT` identique à l'état enregistré ne change rien et n'écrit
  pas d'audit : un client peut rejouer tout son état souhaité.
- **Un `PUT` remplace la représentation.** Un `display_name` omis est vide. Les
  champs hors contrat — descriptions, devises, identités, rôles de plateforme,
  rôles personnalisés — ne sont jamais modifiés.
- **Valeurs :** `status` vaut `active` ou `suspended` ; `tenant_role` vaut
  `owner` ou `member` ; le `role` d'une organisation vaut `owner`, `admin` ou
  `member`. Les slugs sont mis en minuscules ; les noms font 1 à 200 caractères,
  sans caractère de contrôle.
- **Les corps** sont un objet JSON de chaînes avec
  `Content-Type: application/json` (sinon `415 unsupported_media_type`), de
  1 Mio au plus. Un champ inconnu, une valeur non textuelle ou un Unicode
  invalide donne `400 invalid_json` ; un champ requis absent ou `null` donne
  `400 invalid_representation`. Les routes communes n'acceptent aucun paramètre
  de requête (`400 invalid_parameter`).

### Tenants

`PUT /v1/tenants/{tenantID}` crée le tenant ou le renomme : slug et nom changent
librement, les domaines restent attachés. La création d'un second tenant est
refusée avec `409` tant que `XOLO_MULTITENANCY_ENABLED` vaut `false`. Le tenant
`default` garde son slug et reste `active` : c'est celui que résout toute
instance mono-tenant. Son identifiant se lit avec
`GET /v1/xolo/tenants?slug=default`.

Un tenant `suspended` répond `404` sur tous ses domaines.

### Organisations

`PUT …/organizations/{orgID}` crée l'organisation avec ses rôles intégrés, ou
met à jour son slug, son nom et son statut. Un identifiant d'organisation déjà
utilisé par un autre tenant donne `409`.

### Membres

`PUT …/members/{memberID}` met à jour un utilisateur du tenant : email, nom
affiché, rôle de tenant et statut. `suspended` désactive le compte. L'identité
d'authentification et les rôles de plateforme ne sont jamais modifiés.

**Un membre se provisionne une fois qu'il s'est connecté.** Un `PUT` sur un
utilisateur inconnu répond `404` : un compte créé sans identité bloquerait la
première connexion de la personne sur son email. Le plan de contrôle retrouve le
compte avec `GET /v1/xolo/tenants/{tenantID}/users?provider=&subject=`, ou le
laisse se créer inactif à la connexion (`XOLO_HTTP_AUTHN_ACTIVE_BY_DEFAULT=false`)
puis le récupère avec `GET /v1/xolo/tenants/{tenantID}/users?active=false`.

`tenant_role` déclare les propriétaires du tenant ; il n'accorde aucun privilège
de plateforme. Un tenant garde un propriétaire actif dès qu'il en a un :
rétrograder ou suspendre le dernier est refusé avec `409 last_owner`.

**Les administrateurs de plateforme sont protégés.** Tout `PUT` qui modifierait
un compte portant le rôle de plateforme `admin` — email, nom affiché, statut ou
rôle de tenant — est refusé avec `409 platform_admin_protected` ; un `PUT`
identique répond toujours `200`. La même protection s'applique à
`PUT /v1/xolo/tenants/{tenantID}/users`. Le provisioning n'agit jamais sur les
privilèges de plateforme.

### Adhésions

`PUT …/organizations/{orgID}/members/{memberID}` ajoute le membre à
l'organisation ou met à jour son adhésion. `role` fixe le rôle intégré de
l'adhésion ; les rôles personnalisés attribués via `/v1/xolo` sont conservés.
Une adhésion `suspended` ne donne aucun accès à l'organisation et garde ses
rôles.

Une organisation garde un propriétaire actif dès qu'elle en a un : rétrograder
ou suspendre le dernier est refusé avec `409 last_owner`. Une organisation ou un
membre d'un autre tenant donne `404 parent_not_found`.

## Domaines et routage

`PUT /v1/tenants/{tenantID}/domains/{hostname}` déclare un nom d'hôte du tenant
ou change son statut. Le nom d'hôte doit déjà être en minuscules, sans port ni
adresse IP (sinon `400 invalid_hostname`), et appartient à un seul tenant (sinon
`409`). Un tenant peut posséder plusieurs domaines.

Avec `XOLO_MULTITENANCY_ENABLED=true`, le serveur public route chaque requête par
ces domaines : l'hôte de la requête doit être un domaine `active` d'un tenant
`active`, sinon la requête répond `404`. Liens, redirections et callbacks OAuth
gardent le schéma, le port et le chemin de `XOLO_HTTP_BASE_URL` et prennent le
domaine comme hôte. Sur une instance mono-tenant, les domaines sont enregistrés
mais ne servent pas au routage.

Au premier démarrage multi-tenant d'une instance mise à niveau,
`XOLO_MULTITENANCY_HOST_PATTERN`, s'il est défini, est développé une seule fois en
un domaine `active` par tenant existant : chaque tenant reste joignable sur son
ancien nom d'hôte. Ce développement ne se rejoue jamais : les tenants créés
ensuite doivent déclarer leurs domaines via l'API, et la variable peut être
retirée. Un nom d'hôte déjà déclaré est conservé et journalisé, jamais réattribué.

## Extensions Xolo

Les opérations propres à Xolo sont sous `/v1/xolo`. Leurs corps sont en JSON
camelCase, les horodatages en RFC 3339, les collections au format
`{"items": […], "page": 1, "limit": 50, "total": 123}`, et les champs inconnus
sont refusés.

| Méthode | Route | Remarques |
| --- | --- | --- |
| `GET` | `/v1/xolo/healthz` | Également derrière le TLS mutuel. |
| `GET` | `/v1/xolo/permissions` | Le catalogue RBAC : seule source des codes de permission valides. |
| `GET` | `/v1/xolo/tenants` | `?slug=` pour une recherche exacte, sinon `?page=&limit=`. |
| `GET` | `/v1/xolo/tenants/{tenantID}` | |
| `PATCH` | `/v1/xolo/tenants/{tenantID}` | `name`, `description`, `active`. |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations` | `?slug=` pour une recherche exacte, sinon `?page=&limit=`. |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}` | |
| `PATCH` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}` | `name`, `description`, `active`, `currency`, `shareQuotaEqually`. |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/members` | Paginé. |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/members/{membershipID}` | |
| `PUT` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/members/{membershipID}/roles` | Remplacement complet des rôles. |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles` | Rôles intégrés et personnalisés. |
| `POST` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles` | Rôle personnalisé. |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles/{roleID}` | |
| `PUT` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles/{roleID}` | Rôles personnalisés uniquement. |
| `DELETE` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles/{roleID}` | Rôles personnalisés uniquement. |
| `GET` | `/v1/xolo/tenants/{tenantID}/users` | `?provider=&subject=` pour une recherche exacte, sinon `?search=&active=&page=&limit=`. |
| `PUT` | `/v1/xolo/tenants/{tenantID}/users` | Upsert idempotent sur `(provider, subject)` : `201` à la création, `200` sinon. |
| `GET` | `/v1/xolo/tenants/{tenantID}/users/{userID}` | |

## Erreurs

Toutes les erreurs partagent la même enveloppe :

```json
{"error": {"code": "last_owner", "message": "…"}}
```

| Code | HTTP | Cause |
| --- | --- | --- |
| `invalid_parameter` | 400 | Identifiant qui n'est pas un UUID canonique, ou paramètre de requête sur une route commune. |
| `invalid_json` | 400 | Route commune : JSON mal formé, champ inconnu, valeur non textuelle, Unicode invalide. |
| `invalid_representation` | 400 | Route commune : champ requis absent ou `null`. |
| `invalid_hostname` | 400 | Nom d'hôte pas en minuscules, avec port, adresse IP ou label invalide. |
| `invalid_request` | 400 | `/v1/xolo` : corps mal formé, champ inconnu, paramètre de requête invalide. |
| `client_certificate_rejected` | 403 | Certificat ou URI client non autorisé. |
| `not_found` | 404 | Ressource ou route inconnue, ou ressource d'un autre tenant ou d'une autre organisation. |
| `parent_not_found` | 404 | Le tenant, l'organisation ou le membre dont dépend la ressource n'existe pas dans ce périmètre. |
| `method_not_allowed` | 405 | Ressource connue, mauvaise méthode. |
| `unsupported_media_type` | 415 | Route commune sans `Content-Type: application/json`. |
| `conflict` | 409 | Identifiant ou nom d'hôte détenu par un autre tenant, slug déjà utilisé, ou invariant métier. |
| `last_owner` | 409 | La modification laisserait un tenant ou une organisation sans propriétaire actif. |
| `platform_admin_protected` | 409 | La modification vise un administrateur de plateforme. |
| `unprocessable` | 422 | Valeur bien formée mais refusée par le domaine. |
| `rate_limited` | 429 | Budget par URI dépassé ; attendre les secondes indiquées par `Retry-After`. |
| `internal_error` | 500 | Erreur inattendue. |

Les messages sont toujours construits explicitement. Traces d'exécution, erreurs SQL, chemins de fichiers, détails TLS et secrets n'atteignent jamais le client : le détail complet est journalisé côté serveur.

## Invariants

- Le provisioning n'accorde ni ne modifie **jamais** de privilège de plateforme.
  Un utilisateur créé via `PUT /v1/xolo/tenants/{tenantID}/users` reçoit
  exactement le rôle de plateforme `user`, les rôles de plateforme ne sont jamais
  modifiés, et un administrateur de plateforme n'est jamais modifié du tout.
- Les adresses de `XOLO_HTTP_AUTHN_DEFAULT_ADMINS` sont réservées : les écrire
  sur un utilisateur est refusé avec `422`. Le bridge d'authentification accorde
  le rôle d'administrateur à quiconque se connecte avec l'une d'elles ;
  l'accepter ici serait une élévation de privilèges indirecte.
- Un tenant ou une organisation garde au moins un propriétaire actif dès qu'il
  en a un.
- Une adhésion suspendue ne donne rien ; un domaine ou un tenant suspendu ne
  route rien.
- Un rôle ne peut être attribué qu'à une adhésion de son organisation. Sinon
  `422`, et aucun rôle n'est modifié.
- Une adhésion ou un rôle d'une autre organisation donne `404`, de même qu'une
  organisation ou un utilisateur d'un autre tenant.
- Le tenant `default` garde son slug et reste actif.
- Les rôles intégrés ne peuvent être ni modifiés ni supprimés.
- Seuls les codes de permission du catalogue RBAC sont acceptés.

## Migration depuis les routes précédentes

Toutes les routes précédentes ont été déplacées ou remplacées, sans alias. Les
identifiants ne changent pas : une ressource existante s'adresse par son UUID
actuel.

| Avant | Maintenant |
| --- | --- |
| `GET /v1/healthz`, `GET /v1/permissions` | `GET /v1/xolo/healthz`, `GET /v1/xolo/permissions` |
| `GET /v1/tenants` | `GET /v1/xolo/tenants` |
| `POST /v1/tenants` `{slug, name, description, active}` | `PUT /v1/tenants/{tenantID}` `{slug, name, status}` avec un UUID de votre choix ; `description` via `PATCH /v1/xolo/tenants/{tenantID}` |
| `GET /v1/tenants/{tenantID}` | `GET /v1/xolo/tenants/{tenantID}` |
| `PATCH /v1/tenants/{tenantID}` `{name, description, active}` | `PATCH /v1/xolo/tenants/{tenantID}` (même corps), ou `PUT /v1/tenants/{tenantID}` `{slug, name, status}` |
| `DELETE /v1/tenants/{tenantID}` | Supprimée : `PUT /v1/tenants/{tenantID}` avec `"status": "suspended"` |
| `GET /v1/tenants/{tenantID}/organizations[/{orgID}]` | `GET /v1/xolo/tenants/{tenantID}/organizations[/{orgID}]` |
| `POST /v1/tenants/{tenantID}/organizations` `{slug, name, description, currency, active, owner}` | `PUT /v1/tenants/{tenantID}/organizations/{orgID}` `{slug, name, status}` ; `description` et `currency` via `PATCH /v1/xolo/…/organizations/{orgID}` ; le propriétaire via `PUT …/organizations/{orgID}/members/{userID}` `{"role": "owner", "status": "active"}` une fois connecté |
| `PATCH /v1/tenants/{tenantID}/organizations/{orgID}` | `PATCH /v1/xolo/tenants/{tenantID}/organizations/{orgID}` (même corps) |
| `DELETE /v1/tenants/{tenantID}/organizations/{orgID}` | Supprimée : `PUT …/organizations/{orgID}` avec `"status": "suspended"` |
| `GET …/organizations/{orgID}/members[/{membershipID}]` | `GET /v1/xolo/…/organizations/{orgID}/members[/{membershipID}]` |
| `POST …/organizations/{orgID}/members` `{userId \| user, roleIds, builtinRoles}` | `PUT /v1/tenants/{tenantID}/organizations/{orgID}/members/{userID}` `{role, status}` ; rôles personnalisés via `PUT /v1/xolo/…/members/{membershipID}/roles` |
| `PUT …/members/{membershipID}/roles` | `PUT /v1/xolo/…/members/{membershipID}/roles` (même corps) |
| `DELETE …/members/{membershipID}` | Supprimée : `PUT …/organizations/{orgID}/members/{userID}` avec `"status": "suspended"` |
| `…/organizations/{orgID}/roles[/{roleID}]` (toutes méthodes) | `/v1/xolo/…/organizations/{orgID}/roles[/{roleID}]` (mêmes corps) |
| `GET`, `PUT /v1/tenants/{tenantID}/users` | `GET`, `PUT /v1/xolo/tenants/{tenantID}/users` (mêmes corps) |
| `GET /v1/tenants/{tenantID}/users/{userID}` | `GET /v1/xolo/tenants/{tenantID}/users/{userID}` |
| `PATCH /v1/tenants/{tenantID}/users/{userID}` `{email, displayName, active}` | `PUT /v1/tenants/{tenantID}/members/{userID}` `{email, display_name, tenant_role, status}` |

**Arrêtez tous les serveurs avant la mise à niveau.** La migration
`202610070001` ajoute les domaines, les rôles de tenant et les statuts
d'adhésion. Un ancien serveur encore actif continuerait de router selon le
modèle d'hôte et donnerait accès via des adhésions suspendues. La migration ne
peut pas être annulée. Avec `XOLO_STORAGE_AUTO_MIGRATE=false`, arrêtez tous les
écrivains, sauvegardez la base puis exécutez `bin/migrate apply -writers-stopped`
avant le démarrage. Le développement de `XOLO_MULTITENANCY_HOST_PATTERN` en
domaines est fait par le serveur au démarrage, dans les deux modes.

## Transactions, audit et corrélation

Chaque mutation de provisioning revérifie ses parents et effectue toutes ses
écritures dans une transaction unique. Une erreur annule aussi les modifications
d'un utilisateur préexistant, les associations de rôles et l'audit. PostgreSQL utilise
`SERIALIZABLE` ; les conflits d'écriture SQLite et les conflits de sérialisation
PostgreSQL rejouent l'opération entière avec une attente bornée et annulable.
Aucun verrou de publication global n'est utilisé.

`mutation_audits` conserve un état avant/après par ressource effectivement modifiée :
tenant, domaine, organisation, utilisateur, adhésion ou rôle, y compris les associations de
rôles et les suppressions en cascade. Les changements successifs sont regroupés ;
une opération sans changement ne produit pas d'audit. Les états excluent les secrets
et les horodatages techniques. L'historique et son périmètre tenant/organisation
survivent à la suppression des ressources. Les UUID d'audit ne donnent pas l'ordre
des commits. Seules les mutations de provisioning alimentent ce nouvel audit.

`X-Request-ID` doit contenir une seule valeur de 32 caractères hexadécimaux minuscules.
Toute valeur absente, répétée ou invalide est remplacée. La valeur retenue est renvoyée
dans la réponse et utilisée dans les journaux, l'audit et les événements, y compris
après reprise. L'URI de l'acteur provient exclusivement du certificat autorisé.
Sans acteur HTTP, les appels internes utilisent `urn:xolo:operator:local` avec une
corrélation générée au début de l'opération.

Les événements locaux de membres et de rôles gardent leurs types et messages,
y compris les événements distincts d'ajout de membre et d'attribution de rôles.
Ils sont émis après commit via le mécanisme asynchrone existant. Ils ne constituent
pas une outbox durable ; seul l'audit est persisté atomiquement. Les lectures
transactionnelles vont directement en base. Après commit, les utilisateurs concernés,
leurs clés secondaires et les jetons supprimés en cascade sont invalidés dans le cache.

Les budgets de requêtes sont locaux au processus et partagés entre certificats
portant le même URI. Seules les identités configurées obtiennent un budget. Un
dépassement renvoie `429`, `rate_limited`, `Retry-After` en secondes et l'identifiant de requête.

La migration `202610060001`, après celle des UUID, crée l'audit sur une installation
neuve comme sur une mise à niveau. Son rollback refuse d'effacer l'historique.

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
  -extfile <(printf "subjectAltName=URI:urn:xolo:client:control-plane\nextendedKeyUsage=clientAuth")
```

Démarrage du serveur avec l'API activée :

```bash
XOLO_SECRET_KEY=$(openssl rand -hex 32) \
XOLO_PROVISIONNING_API_ENABLED=true \
XOLO_PROVISIONNING_API_AUTHORIZED_URIS=urn:xolo:client:control-plane \
XOLO_PROVISIONNING_API_TLS_CERT_FILE=dev-pki/server.crt \
XOLO_PROVISIONNING_API_TLS_KEY_FILE=dev-pki/server.key \
XOLO_PROVISIONNING_API_TLS_CLIENT_CA_FILE=dev-pki/ca.crt \
bin/server
```

Premiers appels :

```bash
CURL="curl -s --cacert dev-pki/ca.crt --cert dev-pki/client.crt --key dev-pki/client.key -H Content-Type:application/json"

# Refusé : aucun certificat client
curl -sk https://localhost:3003/v1/manifest

# Accepté : on récupère d'abord l'identifiant du tenant
$CURL "https://localhost:3003/v1/xolo/tenants?slug=default"
TENANT=…  # l'identifiant lu ci-dessus

# Puis on crée une organisation avec un identifiant choisi
ORG=$(uuidgen | tr A-Z a-z)
$CURL -X PUT "https://localhost:3003/v1/tenants/$TENANT/organizations/$ORG" \
  -d '{"slug":"acme","name":"Acme","status":"active"}'
```

En production, utilisez une autorité de certification gérée (Vault, cert-manager, PKI interne) et faites tourner les certificats clients.

## Hors périmètre actuel

- Les fournisseurs, modèles LLM, modèles virtuels, middlewares, applications et leurs jetons, quotas, alertes et paramètres d'événements : ils restent gérés par l'interface web.
- Les portées par certificat : tout URI autorisé administre l'instance entière.
- La création d'un membre avant sa première connexion : un membre se provisionne une fois connecté. Le mécanisme d'[invitation](../organisation/invitation/invitation.md) reste la voie par email, via l'interface web.
- La suppression de tenants, domaines, organisations ou adhésions : suspendez-les.
- Les écritures conditionnelles (`ETag`/`If-Match`), les lectures communes paginées, le flux d'événements et les webhooks.
- Aucune spécification OpenAPI n'est générée à ce jour.
