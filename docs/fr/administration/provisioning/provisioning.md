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
| `XOLO_PROVISIONNING_API_EVENT_RETENTION` | `720h` | Historique conservé dans le flux d'événements, `0` pour illimité ; s'applique même lorsque l'écouteur est désactivé. |

Le débit et la rafale s'appliquent à chaque processus : avec N réplicas, un même URI dispose de N fois les valeurs configurées.

Le multi-tenant se configure au niveau de l'instance, pas de cette API :

| Variable | Défaut | Description |
| --- | --- | --- |
| `XOLO_MULTITENANCY_ENABLED` | `false` | Autorise plus d'un tenant et route les requêtes par domaine. |
| `XOLO_MULTITENANCY_HOST_PATTERN` | — | Mise à niveau uniquement : développé une seule fois en un domaine par tenant existant, voir [Domaines et routage](#domaines-et-routage). |
| `XOLO_MULTITENANCY_DEFAULT_TENANT_SLUG` | `default` | Tenant servi lorsque le multi-tenant est désactivé. |

Les webhooks ont leurs propres variables, voir [Webhooks](#webhooks).

N'exposez pas ce port sur un réseau public : réservez-le au réseau d'administration ou au maillage de services interne.

## Contrat commun

| Méthode | Route | Corps |
| --- | --- | --- |
| `GET` | `/v1/manifest` | — renvoie `{"name","version","contract_version","capabilities"}` |
| `PUT` | `/v1/tenants/{tenantID}` | `{"slug","name","status"}` |
| `PUT` | `/v1/tenants/{tenantID}/domains/{hostname}` | `{"status"}` |
| `PUT` | `/v1/tenants/{tenantID}/organizations/{orgID}` | `{"slug","name","status"}` |
| `PUT` | `/v1/tenants/{tenantID}/members/{memberID}` | `{"email","tenant_role","status"}`, `"display_name"` et `"identity"` facultatifs |
| `PUT` | `/v1/tenants/{tenantID}/organizations/{orgID}/members/{memberID}` | `{"role","status"}` |
| `PUT` | `/v1/tenants/{tenantID}/organizations/{orgID}/teams/{teamID}` | `{"slug","name","status"}` |
| `PUT` | `/v1/tenants/{tenantID}/organizations/{orgID}/teams/{teamID}/members/{memberID}` | `{"status"}` |
| `DELETE` | `/v1/tenants/{tenantID}/domains/{hostname}` | — supprimé immédiatement, `204` |
| `DELETE` | `/v1/tenants/{tenantID}/organizations/{orgID}/members/{memberID}` | — supprimée immédiatement, `204` |
| `DELETE` | `/v1/tenants/{tenantID}/organizations/{orgID}/teams/{teamID}`, `…/teams/{teamID}/members/{memberID}` | — supprimée immédiatement, `204` (extension Xolo) |
| `DELETE` | `/v1/tenants/{tenantID}`, `…/organizations/{orgID}`, `…/members/{memberID}` | — suppression enregistrée, purge différée : voir [Cycle de vie des ressources](#cycle-de-vie-des-ressources) |

Chacune de ces ressources peut aussi être lue, listée et suivie via le flux
d'événements : voir [Lectures, conditions et synchronisation](#lectures-conditions-et-synchronisation).
`capabilities` liste `adoption`, `business_resources`, `conditional_writes`,
`events`, `identity`, `ownership` et `reads`, plus `webhooks` et `lifecycle`
lorsqu'ils sont activés.
Traitez-la comme un ensemble : son ordre n'est pas significatif.

- **Les identifiants** sont des UUID canoniques en minuscules choisis par le
  client. Toute autre valeur est refusée avec `400 invalid_parameter`. Un membre
  est un utilisateur : `memberID` est l'identifiant de l'utilisateur.
- **Chaque `PUT` répond `200`**, création comprise, avec la représentation
  enregistrée et son `ETag`. Un `PUT` identique à l'état enregistré ne change
  rien, n'écrit pas d'audit et conserve son `ETag` : un client peut rejouer tout
  son état souhaité.
- **Un `PUT` remplace la représentation.** Un `display_name` omis est vide. Une
  `identity` omise est conservée, voir [Identité déclarée](#identite-declaree).
  Les champs hors contrat — descriptions, devises, liens de connexion, rôles de
  plateforme, rôles personnalisés — ne sont jamais modifiés.
- **Valeurs :** `status` vaut `active` ou `suspended` ; `tenant_role` vaut
  `owner` ou `member` ; le `role` d'une organisation vaut `owner`, `admin` ou
  `member`. Les slugs sont mis en minuscules ; les noms font 1 à 200 caractères,
  sans caractère de contrôle.
- **Les corps** sont un objet JSON de chaînes (hormis l'`identity` d'un membre) avec
  `Content-Type: application/json` (sinon `415 unsupported_media_type`), de
  1 Mio au plus. Un champ inconnu, une valeur non textuelle ou un Unicode
  invalide donne `400 invalid_json` ; un champ requis absent ou `null` donne
  `400 invalid_representation`. Les `PUT` n'acceptent aucun paramètre de
  requête (`400 invalid_parameter`).

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

`PUT …/members/{memberID}` crée le membre ou le met à jour : email, nom affiché,
rôle de tenant, statut et identité déclarée. `suspended` désactive le compte.
Les rôles de plateforme ne sont jamais modifiés.

**Un membre peut être provisionné avant sa première connexion.** Un `PUT` sur un
identifiant inconnu crée le compte avec le rôle de plateforme `user`, lié à
aucune connexion : sa première connexion le lie, par son identité déclarée ou un
email vérifié (voir ci-dessous). Un email qu'un autre compte du tenant détient
déjà, quelle que soit sa casse, est refusé avec `409 conflict`. Un jeton d'API
continue de désigner son propriétaire, lié ou non.

`tenant_role` déclare les propriétaires du tenant ; il n'accorde aucun privilège
de plateforme. Un tenant garde un propriétaire actif dès qu'il en a un :
rétrograder ou suspendre le dernier est refusé avec `409 last_owner`.

**Les administrateurs de plateforme sont protégés.** Tout `PUT` qui modifierait
un compte portant le rôle de plateforme `admin` — email, nom affiché, statut,
rôle de tenant ou identité — est refusé avec `409 platform_admin_protected` ; un
`PUT` identique répond toujours `200`. La même protection s'applique à
`PUT /v1/xolo/tenants/{tenantID}/users`. Une connexion ne rattache pas non plus
un administrateur de plateforme à une nouvelle identité, ni par une déclaration,
ni par un email. Le provisioning n'agit jamais sur les privilèges de plateforme.

### Identité déclarée

Le champ facultatif `identity` désigne la connexion d'un membre :

```json
{"email":"jane@corp.tld","tenant_role":"member","status":"active",
 "identity":{"issuer":"https://id.corp.tld/realms/main","subject":"6f0c2a1e"}}
```

| `identity` | Effet |
| --- | --- |
| absente | L'identité déclarée et le lien de connexion sont conservés : un client qui ignore le champ ne détache jamais personne. |
| `null` | L'identité déclarée est retirée et le lien de connexion détaché. Le membre ne se reconnecte que par une nouvelle déclaration ou un email vérifié. |
| objet | L'identité est déclarée. Un membre déjà lié à une autre connexion est refusé avec `409 conflict` : pour faire passer un compte à un autre fournisseur ou une autre identité, envoyez `null`, puis la nouvelle identité. |

- `issuer` est une URL HTTPS exacte de 2048 octets au plus, avec un hôte, sans
  userinfo, requête, fragment, espaces de bord ni caractère de contrôle.
  `subject` est une chaîne UTF-8 exacte de 1 à 255 octets sans caractère de
  contrôle. Rien n'est normalisé : casse, espaces et barre oblique finale
  comptent. Toute autre valeur — `{}`, un champ manquant, vide ou en trop, une
  valeur non textuelle — donne `400 invalid_representation`. Xolo n'appelle
  aucun émetteur.
- Une identité désigne au plus un membre **par tenant**, déclarée ou déjà liée :
  déclarer une identité qu'un autre membre détient donne `409 conflict`. La même
  identité peut posséder un membre distinct dans chaque tenant.
- `GET`, listes et `ETag` incluent l'identité déclarée. Les événements ne portent
  que des clés et des ETags, jamais l'identité.

**À la connexion**, Xolo résout le compte dans cet ordre :

1. le compte déjà lié à cette connexion ;
2. le membre dont l'identité déclarée est l'émetteur et le sujet prouvés par le
   fournisseur d'identité ;
3. le seul compte du tenant portant cet email, comparé sans tenir compte de la
   casse, quand le fournisseur d'identité l'affirme vérifié (`email_verified`,
   ou `verified_email` pour Google) et que le compte n'est lié à aucune
   connexion et ne déclare aucune identité ;
4. sinon un nouveau compte, selon `XOLO_HTTP_AUTHN_AUTO_CREATE_USERS`, les
   administrateurs par défaut et les invitations en attente, comme avant.

Une connexion ne correspond à une identité déclarée que si son fournisseur prouve
l'émetteur : fournisseurs OIDC nommés et Gitea avec document de découverte
(l'`issuer` découvert), Google (`https://accounts.google.com`), et les
authentificateurs de jetons de ces fournisseurs. GitHub OAuth et un Gitea sans
découverte n'en prouvent aucun : leurs membres se connectent par un email
vérifié.

Rien n'est jamais fusionné ni réattribué. Plusieurs comptes dont les emails ne
diffèrent que par la casse refusent le rattachement et restent tels quels ; une
identité en conflit ne se rabat jamais sur l'email. Une connexion refusée répond
`409` et enregistre un événement `auth.login.failed`. Une requête d'un compte
déjà lié ne fait que le lire : ni transaction, ni verrou. Lier une connexion ne
modifie aucune projection et ne publie aucun événement.

Limites connues : les administrateurs par défaut (`XOLO_HTTP_AUTHN_DEFAULT_ADMINS`)
sont reconnus à l'email que renvoie le fournisseur d'identité, vérifié ou non ;
et chaque connexion recopie encore sur le compte l'email et le nom affiché
renvoyés par le fournisseur d'identité, sauf lorsque le système de pilotage
gère les membres (voir *Membres gérés*).

### Adhésions

`PUT …/organizations/{orgID}/members/{memberID}` ajoute le membre à
l'organisation ou met à jour son adhésion. `role` fixe le rôle intégré de
l'adhésion ; les rôles personnalisés attribués via `/v1/xolo` sont conservés.
Une adhésion `suspended` ne donne aucun accès à l'organisation et garde ses
rôles.

Une organisation garde un propriétaire actif dès qu'elle en a un : rétrograder
ou suspendre le dernier est refusé avec `409 last_owner`. Une organisation ou un
membre d'un autre tenant donne `404 parent_not_found`.

### Équipes

`PUT …/organizations/{orgID}/teams/{teamID}` crée une équipe de
l'organisation ou change son slug, son nom ou son statut. Les équipes sont à
plat. Le slug est unique dans l'organisation et forme le segment d'équipe des
noms de modèles `<org>/<équipe>/<modèle>` : un slug nouveau ou modifié est
vérifié comme celui d'une nouvelle équipe (`409 conflict` quand un nom de
modèle de l'organisation commence par `<slug>/`), et les clients qui ont
enregistré des noms de modèles avec l'ancien slug ne les résolvent plus. Un
UUID d'équipe est global : celui d'une autre organisation répond
`409 conflict`. Un `PUT` ne touche jamais aux réglages de l'équipe, voir
[Ressources métier](#ressources-metier) ; une nouvelle équipe n'en a pas.

`PUT …/teams/{teamID}/members/{memberID}` place un membre de l'organisation
dans l'équipe, ou change son statut. Le membre doit avoir une adhésion à
l'organisation, quel que soit son statut, et l'équipe doit appartenir à
l'organisation : sinon `404 parent_not_found`. Une appartenance à une équipe
n'a pas de rôle dans le contrat commun : une nouvelle n'a aucun rôle
d'équipe, une existante garde les siens.

Une équipe `suspended`, ou un membre d'équipe `suspended`, ne donne rien par
l'équipe : aucun modèle, aucun rôle d'équipe, aucune part du budget de
l'équipe. Membres, rôles et réglages sont conservés. La suspension ne se
propage jamais. Retirer une adhésion à l'organisation retire le membre de ses
équipes et publie `team_membership.deleted.v1`. Le `DELETE` d'une équipe ou
d'une appartenance est une extension du contrat commun : immédiat, idempotent,
avec un `If-Match` facultatif. Une équipe à laquelle une application est
rattachée, ou qui porte seule un middleware, n'est pas supprimée
(`409 conflict`).

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

## Lectures, conditions et synchronisation

Chaque ressource du contrat commun a une **projection** : la représentation que
renvoie son `PUT`, maintenue dans la même transaction que la ressource. Toute
modification d'une projection, quelle qu'en soit l'origine — cette API,
l'interface web, une connexion, une invitation acceptée, une suppression —
reçoit la position suivante d'un **flux d'événements** unique et publie un
événement.

| Méthode | Route | Réponse |
| --- | --- | --- |
| `GET` | `/v1/tenants/{tenantID}`, `…/domains/{hostname}`, `…/organizations/{orgID}`, `…/members/{memberID}`, `…/organizations/{orgID}/members/{memberID}`, `…/organizations/{orgID}/teams/{teamID}`, `…/teams/{teamID}/members/{memberID}` | La représentation, avec son en-tête `ETag` |
| `GET` | `/v1/tenants`, `…/domains`, `…/organizations`, `…/members`, `…/organizations/{orgID}/members`, `…/organizations/{orgID}/teams`, `…/teams/{teamID}/members` | `{"items":[{"key","representation","etag"}],"next_cursor"}` |
| `GET` | `/v1/events/cursor` | `{"cursor"}` : la fin actuelle du flux |
| `GET` | `/v1/events?cursor=` | `{"items":[…],"next_cursor","has_more"}` |

### ETags et écritures conditionnelles

- **L'ETag est une révision**, `W/"<n>"` : la position du flux de la dernière
  modification de la représentation. Les révisions sont persistées et
  strictement croissantes sur toute l'instance ; elles ne dépendent d'aucune
  horloge : ni un saut d'horloge, ni un décalage entre réplicas, ni la
  suppression puis la recréation d'une ressource ne font réapparaître un ETag.
  Les ressources existantes ont reçu leur propre révision lors de la mise à
  niveau. Les ETags sont des validateurs opaques : comparez-les, ne les
  calculez pas.
- Un `PUT` renvoie la projection écrite par sa propre transaction et son
  `ETag`. Un `PUT` qui ne change rien conserve la révision.
- **`If-Match`** sur un `PUT` est vérifié dans la transaction de mutation, avant
  toute écriture : `*` ou une liste d'ETags séparés par des virgules, comparés
  faiblement. Une condition périmée répond `412 precondition_failed`, même pour
  un corps identique à l'état enregistré ; `*` sur une ressource absente donne
  aussi `412`. Sans `If-Match`, l'écriture est inconditionnelle. De deux
  écrivains détenant le même ETag, un seul réussit. Une condition mal formée,
  ou tout `If-None-Match`, donne `400 invalid_precondition`.

### Listes et curseurs

- Une collection est le chemin de sa ressource unitaire sans le dernier
  segment. Les listes n'acceptent que `limit` (1 à 1000, 100 par défaut) et
  `cursor`, une fois chacun (sinon `400 invalid_parameter`) ; les lectures
  unitaires, `/v1/manifest` et `/v1/events/cursor` n'acceptent aucun paramètre
  de requête.
- Les éléments sont triés selon l'ordre binaire de leur clé. Une page n'est pas
  un instantané de la collection : les modifications validées pendant une
  énumération sont rattrapées par le flux. Il n'y a pas de total.
- `next_cursor` vaut `null` sur la dernière page. Les curseurs sont signés avec
  un secret de l'instance et liés à la collection, à ses parents et à la
  limite : un curseur altéré, ou utilisé pour une autre collection, une autre
  limite ou le flux, répond `400 invalid_cursor`. Un curseur de liste expire
  24 heures après la première page, sans renouvellement (`410 cursor_expired`).
  Les curseurs sont opaques mais pas chiffrés.
- Un parent inconnu répond `404 parent_not_found` sur une liste et
  `404 not_found` sur une lecture unitaire.

### Flux d'événements

Les événements suivent un profil fermé de [CloudEvents 1.0](https://cloudevents.io),
sans représentation ni donnée personnelle :

```json
{"specversion":"1.0","id":"…","source":"urn:uuid:…","type":"organization.updated.v1",
 "time":"…","datacontenttype":"application/json","sequence":"42","requestid":"…",
 "data":{"resource_type":"organization","key":{"tenant_id":"…","organization_id":"…"},"etag":"W/\"42\""}}
```

- `type` vaut `<resource_type>.created.v1`, `.updated.v1` ou `.deleted.v1`, où
  `resource_type` est `tenant`, `tenant_domain`, `organization`, `member`,
  `organization_membership`, `team`, `team_membership` ou l'une des familles
  métier `provider`, `llm_model`, `custom_role`, `virtual_model`,
  `middleware`, `personal_virtual_model`, `team_settings`, `application`,
  `quota` et `alert`. La clé d'une équipe est `{"tenant_id","organization_id","team_id"}`,
  celle d'une appartenance y ajoute `member_id`. Une suppression ne porte pas
  d'`etag`.
- **Un événement par ressource et par commit.** Un no-op ou une transaction
  annulée ne publie rien. Dans un commit, les suppressions viennent d'abord,
  des enfants vers les parents, puis les créations et modifications, des
  parents vers les enfants.
- `sequence` ordonne les événements **dans l'ordre des commits** : une position
  n'est visible qu'une fois chaque position inférieure visible ou annulée ; un
  consommateur qui reprend après la dernière position lue ne saute donc jamais
  d'événement. Les positions ont des trous. `time` est purement informatif.
  `requestid` est le `X-Request-ID` de la requête de provisioning, ou une
  corrélation générée pour les autres modifications.
- `has_more=false` signifie que la page a atteint la fin du flux ; son
  `next_cursor` reprend à cet endroit. Les curseurs d'événements n'expirent
  jamais d'eux-mêmes.
- Les utilisateurs techniques des applications ne sont pas des membres : ils ne
  sont ni projetés ni modifiables via `PUT …/members/{memberID}`.

**Algorithme du consommateur.**

1. Capturez un curseur avec `GET /v1/events/cursor`, **avant** l'inventaire.
2. Énumérez `/v1/tenants`, puis les domaines, organisations, membres et
   adhésions de chaque tenant.
3. Interrogez `GET /v1/events?cursor=` ; pour chaque événement, relisez la
   ressource et appliquez sa représentation actuelle (`404` signifie qu'elle a
   disparu). Dédupliquez les événements par `(source, id)` ; ne laissez jamais
   une lecture plus ancienne écraser le résultat d'une lecture plus récente.
4. Persistez l'état appliqué, **puis** le `next_cursor`. Un arrêt brutal entre
   les deux rejoue des événements, sans conséquence.
5. Sur `410 cursor_expired`, reconstruisez une nouvelle génération depuis
   l'étape 1 et basculez dessus seulement une fois complète.

### Rétention

`XOLO_PROVISIONNING_API_EVENT_RETENTION` (`720h` par défaut, `0` conserve tous
les événements) supprime chaque heure les plus anciens événements créés avant
la période de rétention. La suppression s'arrête au premier événement conservé :
un recul d'horloge ne supprime jamais un événement qui suit un événement
conservé. Un curseur antérieur aux événements supprimés répond
`410 cursor_expired`. La rétention s'exécute même lorsque l'écouteur est
désactivé, puisque les modifications sont publiées dans tous les cas. Seule
cette rétention supprime des événements : supprimer des ressources n'invalide
jamais les curseurs des autres consommateurs.

### Stockage et performances

- La source du flux et le secret qui signe les curseurs sont stockés en base :
  sauvegardez-les avec elle. Restaurer une autre base invalide les curseurs
  (`400 invalid_cursor`) et change la `source` : les consommateurs
  reconstruisent.
- Une transaction ne prend le **verrou du flux** que lorsqu'elle modifie une
  projection, à sa toute fin, et le garde jusqu'au commit ; sous PostgreSQL,
  c'est un verrou consultatif associé à une séquence. Les lectures, les no-op,
  les connexions qui ne changent rien et le proxy LLM ne le prennent jamais.
  Les modifications de projections sont ainsi sérialisées le court instant de
  leur commit : un débit d'écriture moindre contre un flux sans trou.

## Webhooks

Les webhooks poussent les événements du
[flux](#lectures-conditions-et-synchronisation) vers des récepteurs HTTPS, au
fil des commits. Ce sont des notifications, pas une source de vérité : un
consommateur conserve son propre point de reprise dans le flux, interroge
`/v1/events` au démarrage, après une reconnexion et périodiquement, et ne fait
jamais avancer ce point de reprise à cause d'un webhook. Un webhook perdu ne
fait donc jamais perdre de modification.

Les webhooks sont désactivés par défaut. Le worker de livraison tourne dans
chaque processus où `XOLO_WEBHOOKS_ENABLED=true`, que l'écouteur de
provisioning y soit activé ou non ; les abonnements se gèrent par l'écouteur.

| Variable | Défaut | Description |
|---|---|---|
| `XOLO_WEBHOOKS_ENABLED` | `false` | Exécute la préparation, la livraison et le nettoyage des webhooks, et annonce `webhooks` dans le manifeste. |
| `XOLO_WEBHOOKS_ALLOWED_ORIGINS` | _(requis si activé)_ | Origines HTTPS séparées par des virgules (`https://hôte[:port]`, sans chemin) qu'un abonnement peut viser. |
| `XOLO_WEBHOOKS_ALLOW_PRIVATE_NETWORKS` | `false` | Autorise aussi les adresses de loopback et privées. |
| `XOLO_WEBHOOKS_TLS_CA_FILE` | — | Autorités de confiance supplémentaires (PEM) ; celles du système restent reconnues. |
| `XOLO_WEBHOOKS_WORKERS` | `2` | Livraisons simultanées par processus, de 1 à 16. |
| `XOLO_WEBHOOKS_POLL_INTERVAL` | `1s` | Intervalle de préparation et d'interrogation, de 100 ms à 1 minute. |
| `XOLO_WEBHOOKS_QUEUE_CAPACITY` | `10000` | Livraisons en attente ou en cours sur l'instance. |
| `XOLO_WEBHOOKS_SUBSCRIPTION_CAPACITY` | `1000` | Livraisons en attente ou en cours pour un abonnement, au plus la capacité de la file. |

### Abonnements

| Méthode | Route | Réponse |
|---|---|---|
| `GET` | `/v1/xolo/tenants/{tenantID}/webhooks` | `{"items":[…]}` |
| `GET` | `/v1/xolo/tenants/{tenantID}/webhooks/{subscriptionID}` | L'abonnement |
| `PUT` | `/v1/xolo/tenants/{tenantID}/webhooks/{subscriptionID}` | Le crée ou le remplace : `200` et l'abonnement |
| `DELETE` | `/v1/xolo/tenants/{tenantID}/webhooks/{subscriptionID}` | Le supprime avec ses livraisons : `204` |
| `POST` | `/v1/xolo/tenants/{tenantID}/webhooks/{subscriptionID}/reset` | `{"acknowledgeLoss":true}` : abandonne ses livraisons et reprend à la fin du flux, `204` |
| `GET` | `/v1/xolo/tenants/{tenantID}/webhooks/{subscriptionID}/deliveries` | Les 100 dernières livraisons, sans l'événement ni la réponse |

```json
{"destination":"https://hooks.example.com/xolo","events":["organization.updated.v1","organization.deleted.v1"],
 "enabled":true,"secrets":["whsec_…"]}
```

- Un abonnement livre les événements de **son seul tenant**. Son identifiant
  est un UUID choisi par le client et unique sur l'instance : il ne passe
  jamais à un autre tenant (`409 conflict`), et l'abonnement d'un autre tenant
  répond `404 not_found`. Un tenant compte au plus 10 abonnements
  (`409 webhook_capacity`).
- `events` vaut `["*"]` ou une liste d'au plus 20 types d'événements distincts
  du flux. `destination` doit appartenir à une origine autorisée.
- `secrets` contient un secret, ou deux secrets distincts pendant une
  rotation : du base64, éventuellement préfixé par `whsec_`, de 32 à 64 octets
  aléatoires générés par le client. Ils sont obligatoires à la création ; les
  omettre lors d'un `PUT` ultérieur conserve ceux enregistrés. Ils ne sont
  jamais renvoyés : l'abonnement n'expose que `secretCount`. Ils sont chiffrés
  avec `XOLO_SECRET_KEY` et liés à leur tenant et à leur abonnement :
  sauvegardez cette clé avec la base.
- Un nouvel abonnement démarre à la fin du flux : reconstruisez d'abord l'état
  du consommateur, puis appuyez-vous sur les notifications.
- `enabled: false` met l'abonnement en pause sans perdre sa position. Les
  changements de destination et de secrets s'appliquent aux livraisons déjà en
  file.
- Chaque écriture d'un abonnement est auditée avec l'appelant et le
  `X-Request-ID`, jamais avec ses secrets.

### Livraison

Chaque tentative est un `POST` HTTPS de l'événement exact du flux, selon
[Standard Webhooks](https://www.standardwebhooks.com) :

| En-tête | Valeur |
|---|---|
| `Content-Type` | `application/cloudevents+json` |
| `webhook-id` | L'`id` de l'événement, identique à chaque tentative |
| `webhook-timestamp` | Secondes Unix de cette tentative |
| `webhook-signature` | `v1,<HMAC-SHA256 en base64 de "<id>.<timestamp>.<corps>">` pour chaque secret, séparées par des espaces |

Un récepteur vérifie l'une des signatures sur le corps brut avant de le
décoder, refuse un horodatage éloigné de plus de cinq minutes, dédoublonne sur
`(source, id)` et répond `2xx` une fois l'événement accepté de façon durable.

- **Au moins une fois, sans ordre garanti.** Une nouvelle tentative, un arrêt
  après la réponse du récepteur ou deux réplicas peuvent répéter un événement ;
  utilisez `sequence` pour ordonner et le `GET` unitaire pour lire l'état
  courant.
- Un `2xx` acquitte la livraison ; son corps est lu jusqu'à 64 Kio puis
  ignoré. Les redirections ne sont pas suivies. Tout le reste est retenté après
  5, 10, 20… secondes, au plus une heure d'écart, pendant au plus 12 tentatives
  ou 24 heures ; la livraison passe alors `failed`. Réconciliez par le flux.
- Une livraison est réservée pour 30 secondes : un worker qui s'arrête en cours
  de tentative, sur n'importe quel réplica, la laisse à un autre une fois la
  réservation expirée. Un résultat tardif n'écrase jamais une tentative plus
  récente.
- Les livraisons sont des copies de leur événement : la rétention du flux ne
  supprime jamais une livraison en file. Les livraisons terminées sont
  conservées sept jours pour le diagnostic.
- Les workers ne prennent jamais le verrou du flux : ils ne lisent que le flux
  de leur tenant, et le chemin des requêtes ne les attend jamais.

### Destinations

Seules les origines autorisées sont joignables. Chaque adresse résolue pour le
nom est vérifiée à la connexion, et c'est l'adresse vérifiée qui est composée :
le nom ne peut pas être redirigé entre-temps. Les adresses link-local — points
de métadonnées cloud compris —, multicast, non spécifiées, partagées et à usage
spécial sont toujours refusées ; le loopback et les adresses privées seulement
avec `XOLO_WEBHOOKS_ALLOW_PRIVATE_NETWORKS=true`. Les proxys de
l'environnement sont ignorés et les certificats toujours vérifiés. Restreignez
aussi le trafic sortant du processus par un pare-feu.

### États et reprise

| `state` | Signification |
|---|---|
| `ready` | À jour, ou en rattrapage. |
| `backpressure` | La capacité de la file est atteinte : la préparation s'arrête à sa position et reprend quand des livraisons se terminent. Seules les livraisons en attente et en cours comptent, et un abonnement ne peut remplir que sa propre part. |
| `history_lost` | La [rétention](#retention) a supprimé des événements que l'abonnement n'avait pas encore préparés, typiquement après une longue pause. Ils ne sont jamais sautés en silence : reconstruisez le consommateur, puis réinitialisez l'abonnement. |

Un tenant suspendu ne reçoit rien : ses abonnements se mettent en pause et
reprennent à la réactivation, suspension comprise. Une pause — abonnement
désactivé ou tenant suspendu — plus longue que la rétention aboutit à
`history_lost`, car les événements purgés ne peuvent plus être distingués. Supprimer un tenant
supprime ses abonnements et ses livraisons. Seule la rétention de l'ensemble du
flux peut faire passer un abonnement en `history_lost`.

### Supervision

| Métrique | Signification |
|---|---|
| `xolo_webhook_attempts_total` | Tentatives de ce processus |
| `xolo_webhook_failures_total{reason}` | Échecs de ce processus, par diagnostic |
| `xolo_webhook_queue{state}` | Livraisons de la base, par état |
| `xolo_webhook_lag_seconds{stage}` | Âge du plus ancien événement pas encore préparé (`materialization`) ou livré (`delivery`) |
| `xolo_webhook_history_lost`, `xolo_webhook_backpressure` | Abonnements dans cet état |

Les jauges décrivent toute la base et sont relevées par chaque processus :
prenez leur maximum entre réplicas, pas leur somme. Aucun label ne porte de
tenant, de destination ni d'événement. Alertez sur un retard durable, sur les
échecs et sur tout `history_lost`.

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
| `GET`, `POST`, `PUT`, `DELETE` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles[/{key}]` | Rôles personnalisés : voir *Ressources métier*. |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles/builtin` | Rôles intégrés. |
| `GET` | `/v1/xolo/tenants/{tenantID}/users` | `?provider=&subject=` pour une recherche exacte, sinon `?search=&active=&page=&limit=`. |
| `PUT` | `/v1/xolo/tenants/{tenantID}/users` | Upsert idempotent sur `(provider, subject)` : `201` à la création, `200` sinon. |
| `GET` | `/v1/xolo/tenants/{tenantID}/users/{userID}` | |
| `GET` | `/v1/xolo/ownership` | Autorité d'écriture effective de chaque famille |
| `GET` | `/v1/xolo/adoption/export` | Export d'adoption, flux NDJSON : voir *Autorité d'écriture, adoption et détachement* |

## Ressources métier

Dix familles propres à Xolo suivent le même contrat que les familles
communes : lecture unitaire avec `ETag`, listes paginées, `PUT` conditionnels
et événements. `llm_model` et `personal_virtual_model` sont en lecture
seule : ni `PUT` ni `DELETE` (`405`), voir [Modèles LLM](#modeles-llm) et
[Modèles personnels](#modeles-personnels). Le manifeste annonce
`business_resources`.

| Méthode | Route |
|---|---|
| `GET`, `GET`, `PUT`, `DELETE` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles[/{key}]` |
| `GET`, `GET`, `PUT` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/team-settings[/{teamID}]` |
| `GET`, `GET`, `PUT`, `DELETE` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/applications[/{key}]` |
| `GET`, `GET`, `PUT`, `DELETE` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/alerts[/{key}]` |
| `GET`, `GET`, `PUT`, `DELETE` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/providers[/{key}]` |
| `GET`, `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/llm-models[/{key}]` |
| `GET`, `GET` | `/v1/xolo/tenants/{tenantID}/members/{memberID}/personal-models[/{key}]` |
| `GET`, `GET`, `PUT`, `DELETE` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/virtual-models[/{key}]` |
| `GET`, `GET`, `PUT`, `DELETE` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/middlewares[/{key}]` |
| `GET`, `GET`, `PUT` | `…/virtual-models/{key}/nodes[/{nodeID}]`, `…/middlewares/{key}/nodes[/{nodeID}]`, voir [Modèles virtuels et middlewares](#modeles-virtuels-et-middlewares) |
| `GET`, `GET`, `PUT`, `DELETE` | `/v1/xolo/tenants/{tenantID}/quotas[/{key}]` |
| `POST` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles` — crée un rôle personnalisé sous une clé choisie par le serveur ; répond `201` avec `{key, representation, etag}` |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles/builtin` — les rôles intégrés, `{"items":[{"id","builtin_kind","name"}]}`, pour `PUT …/members/{membershipID}/roles` |

Les représentations sont en snake_case et complètes : chaque champ est requis,
`null` seulement là où c'est indiqué.

| Famille | Représentation |
|---|---|
| `custom_role` | `{"name","description","permissions":[…]}` |
| `team_settings` | `{"description","share_quota_equally","model_grants":[{"model_id","kind"}],"middleware_ids":[…],"member_roles":[{"user_id","role_ids":[…]}]}` |
| `application` | `{"name","description","active","role_ids":[…]}`, plus `"team_id"` facultatif (vide pour aucune équipe) |
| `quota` | `{"scope","scope_id","currency","daily_budget","monthly_budget","yearly_budget"}`, budgets en microcents ou `null`, plus `"cascade"` facultatif (`free` par défaut, `capped` ou `split`) |
| `alert` | `{"name","description","scope","owner_id","query","aggregation","window_seconds","comparator","threshold","for_seconds","enabled"}` |
| `virtual_model` | `{"name","description","catalog_overrides","graph"}`, `catalog_overrides` à `null` ou `{"contextWindow","maxOutputTokens","capabilities"}`, comme dans l'interface web |
| `middleware` | `{"name","description","enabled","priority","applies_to_all","targets":[{"kind","id"}],"graph"}` |
| `llm_model` | `{"provider_id","proxy_name","kind","enabled"}`, en lecture seule |
| `personal_virtual_model` | `{"name","description","team_id","catalog_overrides","graph"}`, en lecture seule, sous un membre |
| `provider` | `{"name","type","base_url","active","currency","cloud_tier","billing_mode","subscription_plan","retry_config","rate_limit_config"}`, les trois derniers `null` ou objets, plus `"api_key"` en écriture seule et `"request_headers"` facultatif : `null` ou `{"headers":{"Nom":"valeur"},"session_header":"x-opencode-session"}`, les en-têtes qu'exige une passerelle (les valeurs vides et les en-têtes d'authentification, de cookies, de transport et de transfert sont refusés) |

- **Clés.** Une nouvelle ressource est créée sous un UUID canonique choisi par
  le client. Une ressource créée depuis l'interface web garde son identifiant
  local, accepté en lecture et en mise à jour, jamais en création. Les
  événements et les éléments de liste portent la clé dans `key.resource_id`,
  avec `key.organization_id` sauf pour les quotas.
- **Parents.** Chaque référence est vérifiée dans la transaction de
  l'écriture. Un grant d'équipe désigne un modèle de l'organisation (et, pour un modèle
  LLM, un provider de l'organisation) ; `middleware_ids` désigne des
  middlewares de l'organisation, et une équipe ne peut ni retirer un middleware
  qu'elle seule porte, ni être supprimée avec lui (`409`) : il s'appliquerait
  alors à toute l'organisation ; un utilisateur de `member_roles` est un membre
  de l'équipe, et ses `role_ids`, comme ceux d'une application, désignent
  des rôles de l'organisation, mais jamais, à la différence de ces derniers, un rôle intégré (`422`) ; `team_id` désigne une équipe de l'organisation ;
  un quota plafonne une organisation du tenant, un membre du tenant, une
  application ou une équipe d'une de ses organisations, ou un membre d'une
  équipe (portée `team_member`, `scope_id` `<team_id>:<user_id>`) ; le propriétaire d'une
  alerte est un membre actif de l'organisation, obligatoire pour une alerte
  `personal`. Un parent absent ou étranger répond `404 parent_not_found` ; une
  ressource d'une autre organisation ou d'un autre tenant répond
  `404 not_found`, comme une ressource absente.
- **Champs immuables.** Le `scope` et le `scope_id` d'un quota, le `scope` et
  l'`owner_id` d'une alerte ne changent jamais (`409 conflict`). Une portée porte un seul quota : une autre clé sur la même
  portée répond `409 conflict`.
- **Les réglages d'une équipe** existent exactement quand l'équipe existe : le
  contrat commun crée et supprime l'équipe, voir [Équipes](#equipes), un `PUT`
  de réglages sur une équipe absente répond `404 parent_not_found`, et la
  collection n'a pas de `DELETE`. `member_roles` liste les membres de l'équipe
  qui y tiennent des rôles ; un membre omis, ou listé sans rôle, n'en tient
  aucun. Leur autorité d'écriture est celle de la famille `team`.
- **Répartition des budgets.** Le `cascade` d'un quota d'organisation ou
  d'équipe contraint les quotas de ses équipes ou de ses membres à leur
  écriture : `capped`, chacun au plus égal au parent, période par période ;
  `split`, leur somme au plus égale au parent. Un quota qui la viole, enfant ou
  parent, répond `422`. Seuls les quotas `org` et `team` en portent un autre
  que `free`.
- **Normalisation.** Permissions, grants et identifiants de rôles sont triés
  et dédoublonnés : un rejeu dans un autre ordre est un no-op.
- **L'état d'exécution reste hors du contrat.** Un `PUT` de quota conserve la
  consommation déjà enregistrée. L'état d'évaluation d'une alerte n'est ni
  renvoyé ni publié ; une modification de l'alerte la fait repartir de `ok`,
  comme une modification dans l'interface web. Les rôles intégrés ne font pas
  partie de la collection : en écrire un répond `409 conflict`.
- **Les clés d'API des providers sont en écriture seule.** `api_key` est
  obligatoire à la création et conservée lorsqu'elle est omise. Elle est
  chiffrée avec `XOLO_SECRET_KEY` et n'apparaît jamais dans une réponse, une
  projection, un événement, ni en clair dans l'audit : une rotation ne change
  aucun `ETag` et ne publie aucun événement, mais l'audit enregistre le
  changement d'une empreinte du chiffré.
- **Les suppressions sont immédiates** : `DELETE …/{key}` répond `204`, avec
  un `If-Match` facultatif, et publie `<family>.deleted.v1`. Supprimer une
  ressource absente, ou celle d'une autre organisation, ne change rien, sauf si
  `If-Match` désigne une révision (`412`). Supprimer une application retire ses
  rôles, ses jetons et son quota ; un provider, ses modèles ; un quota conserve
  la consommation enregistrée sur son périmètre. Un rôle intégré est refusé
  (`409 conflict`). Un périmètre gelé les refuse toutes
  (`409 resource_deleted`). Elles ne dépendent pas de
  `XOLO_LIFECYCLE_ENABLED`. Les jetons d'application restent gérés localement.
- Les écritures de l'interface web publient aussi leurs événements. La
  migration `202610120001` projette les ressources métier existantes sans
  publier d'événement ; comme les précédentes, elle exige l'arrêt de tous les
  serveurs.
- La migration `202610150002` introduit les équipes : chaque rôle
  personnalisé qui autorisait des modèles devient une équipe, publiée avec
  `team.created.v1`, et les représentations des rôles (sans `model_grants`),
  des applications (avec `team_id`) et des quotas (avec `cascade`) sont
  publiées de nouveau quand elles changent.
- La migration `202610200001` fait passer les équipes au contrat commun, elle
  aussi avec tous les serveurs arrêtés. Équipes et membres d'équipe reçoivent
  un statut, `active`. Une équipe créée localement avec un xid reçoit un UUID :
  ce qui lui est rattaché (grants, middlewares, membres, rôles, applications,
  jetons, invitations, modèles personnels, enregistrements d'usage, budgets)
  suit, et le flux publie la suppression de l'ancienne clé et la création de la
  nouvelle. Chaque équipe est ensuite publiée en ressources `team`,
  `team_settings` et `team_membership` ; l'ancienne route `/v1/xolo/…/teams`,
  qui intégrait les membres, disparaît.

Limite connue : le cache de chaque processus garde valides les jetons d'une
application désactivée jusqu'à l'expiration de ses entrées
(`XOLO_STORAGE_DATABASE_CACHE_USERS_TTL`) : une écriture via cet écouteur ne
vide que le cache de son propre processus.

### Modèles virtuels et middlewares

Les modèles virtuels et les middlewares portent un graphe de pipeline, écrit
en entier par leur `PUT` ou nœud par nœud :

| Méthode | Route |
|---|---|
| `GET` | `…/virtual-models/{key}/nodes`, `…/middlewares/{key}/nodes` — `{"items":[{"id","type","data","secret_keys":[…]}]}`, triés par identifiant |
| `GET` | `…/virtual-models/{key}/nodes/{nodeID}`, `…/middlewares/{key}/nodes/{nodeID}` — un nœud |
| `PUT` | `…/virtual-models/{key}/nodes/{nodeID}`, `…/middlewares/{key}/nodes/{nodeID}` — `{"data":{…},"secrets":{"nom":"valeur"}}`, `secrets` facultatif |

- **Graphe.** `graph` vaut `{"nodes":[{"id","type","position":{"x","y"},"data"}],"edges":[{"id","source","source_port","target","target_port"}]}`.
  `type` est l'un de `generator`, `sink`, `model`, `value`, `model_ref`,
  `compare`, `select`, `math`, `sample`, `context`, `trace`, `note`,
  `model_fallback`, `block` et `plugin`. `data` est la configuration du type,
  telle que l'éditeur web l'écrit, en camelCase : `{"pluginName":"…","config":{…}}`
  pour un nœud plugin, `{"proxyName":"…"}` ou `{"passthrough":true}` pour un
  nœud modèle. `position` ne sert qu'à l'éditeur et vaut l'origine par défaut.
  Nœuds et arêtes sont triés par identifiant, un `data` absent se lit `{}` et
  les clés de `data` sont triées (nombres gardés tels qu'écrits, `100` et
  `1E2` diffèrent) : un rejeu dans un autre ordre ou une autre mise en forme
  est un no-op. Un graphe sans nœud est un
  pipeline non configuré.
- **La validation**, la même que celle de l'interface web, répond `422` : au
  plus 500 nœuds et 2000 arêtes ; des identifiants de nœuds et d'arêtes non
  vides, uniques, sans espace autour, d'au plus 200 caractères ; un type
  connu ; un `data` qui est un objet JSON ; un nœud plugin qui nomme son
  plugin ; des arêtes entre nœuds existants ; aucune référence de modèle
  qualifiée par une équipe. Un générateur et une sortie connectée ne sont pas
  exigés pendant la construction du graphe : le moteur refuse un pipeline
  incomplet quand il l'exécute. Les noms de plugins ne sont pas confrontés aux
  plugins chargés par l'instance.
- **Les noms** sont uniques par organisation et par famille (`409 conflict`) ;
  un nom de modèle virtuel dont le premier segment avant `/` est le slug d'une
  équipe est refusé (`409`).
- **Nœuds plugin.** Leurs secrets sont rangés par organisation, plugin et
  identifiant de nœud : deux pipelines d'une organisation ne peuvent pas
  porter un nœud plugin de même plugin et de même identifiant
  (`409 conflict`). Un `PUT` qui retire un nœud plugin, ou en change le
  plugin, supprime ses secrets ; supprimer le pipeline les supprime tous.
- **Les cibles** d'un middleware sont des `{"kind":"llm"|"virtual","id"}`,
  modèles ou modèles virtuels de l'organisation (`404 parent_not_found`),
  triées et dédoublonnées ; elles sont conservées, mais ignorées, quand
  `applies_to_all` est vrai. Un ID `llm` est la clé d'un
  [modèle LLM](#modeles-llm).
- **Un nœud n'a pas de révision propre.** Les routes de nœuds renvoient
  l'`ETag` du pipeline, et un `PUT` de nœud se conditionne sur lui : une
  modification de nœud et une réécriture concurrente du graphe, par cette API
  ou par l'interface web, se heurtent en `412` au lieu de s'écraser.
- **Un `PUT` de nœud ne remplace que `data`.** Le type, les arêtes et la
  position s'écrivent par le pipeline. Un nœud absent répond `404 not_found`,
  un changement de plugin `409 conflict`. Les nœuds se créent et se retirent
  par le `PUT` du pipeline.
- **Les secrets de nœud sont en écriture seule**, comme une clé de provider :
  une valeur pose la clé, `null` la supprime, une clé omise est conservée.
  Seul un nœud plugin porte des secrets (`422` sinon) ; au plus 50 par nœud,
  des noms d'au plus 200 caractères, des valeurs d'au plus 16384 octets. Ils
  sont chiffrés avec `XOLO_SECRET_KEY` dans le stockage que le plugin lit par
  `GetSecret`, et jamais renvoyés : les lectures listent leurs noms dans
  `secret_keys`, ce qui indique à un client ce qui reste à renseigner sur une
  autre instance. Un changement de secret seul ne change aucun `ETag` et ne
  publie aucun événement ; l'audit enregistre une empreinte des chiffrés. Une
  valeur égale à celle enregistrée n'écrit rien. Il reste soumis à l'autorité
  d'écriture de la famille (`XOLO_OWNERSHIP`, `403`).
- **Événements.** Les nœuds ne sont pas un type de ressource du flux : une
  modification de nœud publie `virtual_model.updated.v1` ou
  `middleware.updated.v1` avec le nouvel `ETag` du pipeline, un événement par
  commit quel que soit le nombre de nœuds modifiés. Un consommateur relit le
  pipeline, ou ses nœuds, pour savoir ce qui a changé.
- La migration `202610220003` projette les modèles virtuels et middlewares
  existants sans publier d'événement ; comme les précédentes, elle exige
  l'arrêt de tous les serveurs.
- Limites connues : supprimer un modèle virtuel ou un middleware, ici comme
  dans l'interface web, laisse en place les grants et rattachements d'équipe
  qui le désignent.

### Modèles LLM

Les modèles des fournisseurs d'une organisation forment la famille
`llm_model`, en lecture seule : ils se gèrent depuis l'interface web, et
l'API expose ce dont un plan de contrôle a besoin pour résoudre les cibles
`llm` d'un middleware et les `model_grants` d'une équipe, dont le `model_id`
est leur clé.

| Méthode | Route |
|---|---|
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/llm-models` — paginée comme les autres collections |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/llm-models/{key}` — un modèle, avec son `ETag` |

```json
{"provider_id":"…","proxy_name":"meta/llama","kind":"completion","enabled":true}
```

- `kind` vaut `completion`, `embeddings` ou `decision`. Les modèles
  désactivés sont listés, avec `enabled` à `false` : une cible ou un grant
  peut en désigner un, et la passerelle n'en sert aucun.
- **Résolution des noms.** `proxy_name` est le nom du modèle dans son
  organisation, sans le slug de l'organisation ni segment d'équipe. La
  passerelle résout un nom demandé `<org>/<team>/<model>` en retirant le
  segment d'équipe, quand c'est le slug d'une équipe de l'organisation, puis
  `<org>/<model>` en retirant le slug de l'organisation ; le reste, qui peut
  lui-même contenir `/`, est comparé au `proxy_name` des modèles activés.
- Ni le modèle amont, ni l'URL de base, ni le corps additionnel, ni la
  tarification, ni le catalogue ne sont exposés : les modifier ne change
  aucun `ETag` et ne publie aucun événement. Créer, renommer, activer,
  désactiver ou supprimer un modèle publie `llm_model.created.v1`,
  `.updated.v1` ou `.deleted.v1` ; supprimer un fournisseur publie la
  suppression de ses modèles avant la sienne.
- `PUT` et `DELETE` répondent `405`. La famille n'a pas d'autorité
  d'écriture : elle ne fait pas partie de `XOLO_OWNERSHIP`, et seule
  l'instance locale l'écrit.
- La migration `202610230001` projette les modèles existants sans publier
  d'événement ; comme les précédentes, elle exige l'arrêt de tous les
  serveurs.

### Modèles personnels

Les modèles virtuels qu'un membre construit pour lui-même, nommés
`~/<nom>`, forment la famille `personal_virtual_model`, en lecture seule. Ils
dépendent du membre, pas d'une organisation, et se gèrent depuis le profil.

| Méthode | Route |
|---|---|
| `GET` | `/v1/xolo/tenants/{tenantID}/members/{memberID}/personal-models` — paginée comme les autres collections |
| `GET` | `/v1/xolo/tenants/{tenantID}/members/{memberID}/personal-models/{key}` — un modèle, avec son `ETag` |

```json
{"name":"router","description":"","team_id":"…","catalog_overrides":null,"graph":{"nodes":[…],"edges":[…]}}
```

- La clé d'un élément et d'un événement est
  `{"tenant_id","member_id","resource_id"}`.
- `team_id` est l'équipe à laquelle les requêtes sont imputées, choisie par
  le membre, vide s'il n'en a choisi aucune ; elle peut désigner une équipe
  supprimée depuis, ce qui refuse les requêtes jusqu'à ce que le membre en
  choisisse une autre. `graph` est le pipeline, comme pour un modèle virtuel,
  sans les secrets de ses nœuds plugin, qui ne sont ni renvoyés ni listés.
- **Résolution des noms.** La passerelle résout `~/<nom>` parmi les modèles
  du membre authentifié par la requête ; `name` est ce `<nom>`.
- Créer, modifier ou supprimer un modèle personnel publie
  `personal_virtual_model.created.v1`, `.updated.v1` ou `.deleted.v1` ;
  supprimer ou purger un membre publie la suppression de ses modèles. Un
  changement des seuls secrets de ses nœuds ne publie rien.
- `PUT` et `DELETE` répondent `405`. La famille n'a pas d'autorité
  d'écriture : elle ne fait pas partie de `XOLO_OWNERSHIP`, et seule
  l'instance locale l'écrit.
- La migration `202610230001` projette aussi les modèles personnels
  existants.

## Autorité d'écriture, adoption et détachement

Chaque famille du contrat commun, ainsi que les abonnements webhook, a une
**autorité d'écriture** : l'instance locale (interface web, connexion,
invitations), le système de pilotage (cet écouteur), ou les deux.

| Variable | Défaut | Description |
|---|---|---|
| `XOLO_OWNERSHIP` | — | Paires `famille=autorité` séparées par des virgules. Familles : `tenant`, `tenant_domain`, `organization`, `member`, `organization_membership`, `team`, `team_membership`, `subscription`, et les familles métier `provider`, `custom_role`, `virtual_model`, `middleware`, `application`, `quota`, `alert` ; `team` gouverne aussi les réglages d'équipe. Autorités : `shared`, `local`, `control_plane`. Les familles omises valent `shared` ; une famille ou une autorité inconnue empêche le démarrage. |

```dotenv
XOLO_OWNERSHIP=tenant=control_plane,tenant_domain=control_plane,organization=control_plane,member=control_plane,organization_membership=control_plane,subscription=control_plane
```

- `shared`, la valeur par défaut, conserve le comportement précédent : les deux
  écrivent.
- `local` refuse les écritures de cet écouteur avec `403 ownership_denied`.
- `control_plane` refuse les écritures locales : l'interface web répond `403`,
  pour les pages complètes comme pour les fragments htmx. Les invitations
  produisent des adhésions : en créer, en révoquer ou en accepter relève de
  `organization_membership`.

La politique porte sur la **représentation publique** des ressources, celle que
renvoie `GET`. Elle est vérifiée dans la transaction de l'écriture, sur chaque
projection que l'écriture modifie, cascades comprises : supprimer un tenant
dont les membres relèvent d'une autre autorité est refusé en bloc, sans rien
supprimer ni publier. Les champs hors contrat restent locaux quelle que soit la
politique : rôles de plateforme, devise et partage de quota d'une organisation,
modèles LLM et jetons d'application. Les comptes d'application et les jetons d'API restent utilisables,
et les lectures gardent leurs permissions. `GET /v1/xolo/ownership` renvoie la
politique effective, et le manifeste annonce `ownership`.

Une autorité d'écriture n'accorde jamais de privilège : les administrateurs de
plateforme restent protégés (`409 platform_admin_protected`) lorsque le
système de pilotage gère les membres.

La politique est lue au démarrage ; elle n'est pas stockée en base. Arrêtez
**tous** les serveurs et workers avant de la changer, et redémarrez chaque
réplique avec la même valeur : une réplique qui tourne encore avec une ancienne
politique continue d'accepter les écritures que celle-ci autorise. Chaque
processus journalise sa politique effective au démarrage
(`write authority policy`). Les commandes opérateur hors ligne
(`xolo-migrate`, `xolo-adoption`) ont l'autorité de la base et ne vérifient
aucune politique.

### Membres gérés

Avec `member=control_plane`, une connexion ne résout que les membres déclarés
par le système de pilotage :

- elle rattache l'identité prouvée à un membre existant — son identité
  déclarée, puis un email vérifié — sans modifier sa projection ;
- elle ne recopie jamais l'email ni le nom affiché du fournisseur d'identité :
  le profil déclaré est conservé ;
- elle ne crée aucun compte, quoi que disent `XOLO_HTTP_AUTHN_AUTO_CREATE_USERS`
  ou une invitation en attente, sauf pour les administrateurs par défaut, qui
  amorcent l'instance, et les comptes d'application. Toute autre identité est
  refusée avec `403`.

### Export d'adoption

`GET /v1/xolo/adoption/export`, comme `xolo-adoption export`, diffuse en flux
l'inventaire de l'instance : chaque projection des familles communes et métier
de tous les tenants, ressources suspendues comprises, lues sur un même instantané cohérent
sans prendre de verrou. Le format, `xolo-adoption/1`, est du NDJSON
(`application/x-ndjson`) :

```text
{"version":"xolo-adoption/1","contract":"0.1.0-draft.2","source":"urn:uuid:…","c0":"…","families":["tenant","tenant_domain","organization","member","organization_membership","team","team_membership","provider","custom_role","team_settings","application","quota","alert"]}
{"family":"tenant","key":{"tenant_id":"…"},"representation":{…},"etag":"W/\"42\""}
…
{"count":128,"complete":true,"sha256":"…"}
```

- Les enregistrements suivent l'ordre de `families`, parents avant enfants ;
  `key`, `representation` et `etag` sont ceux que renvoie `GET`.
- `c0` est un curseur de `/v1/events` pris sur le même instantané : les
  événements qui le suivent sont exactement les changements absents de
  l'export.
- `sha256` est le SHA-256 hexadécimal en minuscules des octets exacts de toutes
  les lignes précédant la dernière, sauts de ligne compris. Il détecte la
  corruption et la troncature, pas un remplacement délibéré : le transport et
  les permissions du fichier en établissent la provenance.
- Un export interrompu n'a pas de dernière ligne, et la vérification le rejette.
- L'export contient des emails de contact et des identités déclarées :
  protégez-le comme la base. Il ne contient aucun lien de connexion non déclaré,
  session, jeton, secret ni entrée d'audit. `verify` accepte aussi un export ne
  listant que les cinq familles communes, produit avant les familles métier.

`xolo-adoption verify -in <fichier>` (`-in -` lit l'entrée standard) vérifie la
somme de contrôle, le nombre d'enregistrements, la version et le contrat de
l'en-tête, la syntaxe des clés, les statuts, les doublons et que chaque parent
précède ses enfants, en ne gardant que les clés en mémoire. Il affiche la
source, `c0` et le nombre d'enregistrements de chaque famille.

### Adopter une instance

1. Testez une vraie connexion d'administrateur de plateforme, puis sauvegardez
   la base. Laissez toutes les familles à `shared`.
2. Exportez (`xolo-adoption export -out /secure/inventory.ndjson` avec
   `XOLO_STORAGE_DATABASE_DSN`, ou la route ci-dessus), vérifiez le fichier,
   puis préparez-le dans le système de pilotage en gardant les UUID qualifiés
   par la `source` du flux. Résolvez les collisions de slug, de domaine et
   d'email dans le système de pilotage sans changer aucun identifiant.
3. Rejouez `/v1/events` depuis `c0` jusqu'à être à jour. Un
   `410 cursor_expired` impose de recommencer depuis un nouvel export.
4. Arrêtez tous les serveurs et workers, rattrapez le flux une dernière fois,
   définissez `XOLO_OWNERSHIP` et redémarrez chaque réplique. Vérifiez
   `GET /v1/xolo/ownership`, le refus d'une écriture locale et une connexion
   d'administrateur. Aucune ressource n'est réécrite.

### Détacher une instance

Le détachement rend l'instance à son administration locale.

1. Testez une connexion d'administrateur de plateforme qui reste utilisable
   sans le système de pilotage, puis sauvegardez la base.
2. Arrêtez tous les serveurs, workers et autres écrivains de la base.
3. Exécutez :

   ```sh
   XOLO_STORAGE_DATABASE_DSN=… xolo-adoption detach -writers-stopped -operator-access-verified
   ```

4. Retirez les valeurs `control_plane` de `XOLO_OWNERSHIP` et redémarrez
   chaque réplique, puis vérifiez une connexion et une écriture locale.

La commande refuse de s'exécuter si aucun administrateur de plateforme actif
d'un tenant actif n'a de lien de connexion. En une transaction, elle supprime
chaque abonnement webhook, ses secrets chiffrés et ses livraisons en attente,
enregistre une entrée d'audit nommant l'opérateur
(`urn:xolo:operator:<uid>`) et affiche le nombre d'abonnements et de
livraisons supprimés. Ressources, UUID, identités déclarées, liens de
connexion, audit, flux d'événements et sa source restent intacts. Un webhook
déjà envoyé ne peut pas être rappelé. Les deux options attestent les
vérifications ci-dessus : la commande ne peut pas arrêter les autres processus.

`xolo-adoption` est livré à côté de `xolo-server` dans les releases et les
images de conteneur (`/usr/local/bin/xolo-adoption`). `export` et `verify` ne
font que lire, et aucune action ne migre le schéma : utilisez le binaire de la
version qui a migré la base.

## Cycle de vie des ressources

Les domaines, les adhésions et les ressources métier sont supprimés
immédiatement. Un tenant, une organisation ou un membre porte des données
qu'un client peut devoir conserver : sa suppression est différée. `DELETE`
l'enregistre, ce qui **gèle** la ressource et tout ce qu'elle contient ; le
client exporte le périmètre, confirme l'export par son condensat, et
l'instance purge le périmètre une fois la rétention écoulée.

| Variable | Défaut | Description |
|---|---|---|
| `XOLO_LIFECYCLE_ENABLED` | `false` | Autorise l'enregistrement des suppressions, installe les protections en base des périmètres gelés et lance le worker de purge. |
| `XOLO_LIFECYCLE_RETENTION` | `720h` | Durée pendant laquelle une ressource supprimée reste gelée avant de pouvoir être purgée, de 1 s à 3650 jours. |
| `XOLO_LIFECYCLE_POLL_INTERVAL` | `1m` | Fréquence à laquelle le worker de purge cherche les suppressions échues, de 1 s à 1 h. |
| `XOLO_LIFECYCLE_PURGE_BATCH` | `1000` | Lignes supprimées par une transaction de purge, de 1 à 100 000. |

Avec `P` l'une des routes `/v1/tenants/{tenantID}`,
`/v1/tenants/{tenantID}/organizations/{orgID}` et
`/v1/tenants/{tenantID}/members/{memberID}` :

| Méthode | Route | Réponse |
|---|---|---|
| `DELETE` | `P`, `If-Match` facultatif | `202` avec la suppression et l'`ETag` de la ressource gelée ; un `DELETE` répété renvoie la même suppression |
| `GET` | `P/deletion` | `200` avec la suppression et son `ETag` |
| `GET` | `P/deletion/export` | `200`, l'export du périmètre en flux NDJSON |
| `POST` | `P/purge-confirmation`, `If-Match` obligatoire, `{"export_sha256": "…"}` | `200` avec la suppression confirmée |

Une suppression se lit ainsi :

```json
{"resource_type":"organization","tenant_id":"…","resource_id":"…",
 "deleted_at":"…","purge_after":"…","etag":"W/\"42\"",
 "export_sha256":"…","confirmed_at":"…","purged_at":"…","attempts":0,"diagnostic":"purged"}
```

`etag` est présent jusqu'à la purge ; `export_sha256` et `confirmed_at` une
fois l'export confirmé ; `purged_at`, et `purged_with` pour une ressource
purgée avec son tenant, une fois la purge faite. `diagnostic` vaut
`purge_failed` après une tentative échouée, que le worker renouvelle.

- **Geler** une ressource la désactive, ce qui la publie `suspended` avec un
  événement `updated`, et enregistre sa suppression dans la même transaction.
  Le gel vérifie la condition `If-Match` et l'autorité d'écriture de chaque
  famille que contient le périmètre. Il refuse le dernier propriétaire actif
  d'un tenant ou d'une organisation encore vivante (`409 last_owner`), le
  tenant `default`, ainsi qu'un administrateur de plateforme ou un tenant qui
  en contient un (`409 platform_admin_protected`). Un tenant gelé perd
  aussitôt ses sessions OIDC ; un membre gelé les conserve, mais son compte
  désactivé est refusé à chaque requête. L'`ETag` de la suppression est la
  révision de la ressource gelée : elle ne change plus.
- **Un périmètre gelé est en lecture seule.** Toute écriture d'une ligne
  qu'il contient est refusée, quel que soit son auteur : API
  (`409 resource_deleted`), interface web (`403`), workers et SQL brut. Cela
  couvre le tenant, ses domaines, organisations et utilisateurs ; une
  organisation avec ses rôles, applications, jetons, providers, modèles,
  quotas, alertes, consommation, événements, secrets et invitations ; un
  membre avec ses adhésions, préférences, secrets personnels, alertes, quota et
  consommation. Un gel est définitif. Les webhooks d'un tenant gelé ne sont
  plus préparés ni livrés, et ses abonnements ne peuvent plus changer ; le
  détachement les conserve. L'évaluation des alertes et l'éviction des
  événements ignorent les périmètres gelés.
- **L'export** (`xolo-deletion/1`) contient chaque ligne du périmètre, table
  par table, et les audits de ses ressources, lus sur un seul instantané et
  transmis une ligne à la fois. Les secrets en sont exclus : clés des
  providers, valeurs des jetons, secrets des plugins et des webhooks, sessions
  OIDC et livraisons en file.

  ```
  {"format":"xolo-deletion/1","source":…,"deletion":{…,"etag":…}}
  {"table":"organizations","row":{…}}          (une par ligne)
  {"count":…,"complete":true,"sha256":…}
  ```

  Le SHA-256 du trailer couvre toutes les lignes qui le précèdent, retours à
  la ligne compris ; un export sans trailer a été interrompu. Rien n'y dépend
  du moment où il est produit : deux exports de la même suppression donnent
  les mêmes octets.
- **La confirmation** porte le `sha256` du trailer, sous un `If-Match`
  explicite sur la suppression (`*` ou son absence donnent
  `428 precondition_required`). Le serveur exporte de nouveau le périmètre et
  compare : un condensat différent donne `409 export_mismatch`. C'est le cas
  lorsque la purge d'une autre suppression a retiré entre-temps des lignes de
  ce périmètre, par exemple un membre d'une organisation supprimée purgé en
  premier : exportez de nouveau et confirmez le nouveau condensat. Une
  confirmation répétée avec le même condensat renvoie la suppression ; un
  autre condensat, une fois l'export confirmé, donne `409 export_mismatch`.
- **La purge** s'exécute sur chaque réplica, toutes les
  `XOLO_LIFECYCLE_POLL_INTERVAL`, pour les suppressions confirmées dont
  `purge_after` est passé. Modifier `XOLO_LIFECYCLE_RETENTION` ne raccourcit
  jamais une suppression déjà enregistrée. Un tenant emporte les suppressions
  de ses organisations et de ses membres. La purge retire, par transactions
  courtes qu'elle reprend après un arrêt brutal :
  1. les projections du périmètre, les événements qui les concernent et les
     livraisons webhook en file pour ces événements, puis publie
     `<family>.deleted.v1` : un tenant pour lui seul, une organisation pour
     elle et les quotas qu'elle portait, un membre pour lui, ses adhésions,
     son quota et ses alertes personnelles ;
  2. chaque ligne du périmètre, enfants d'abord, par lots de
     `XOLO_LIFECYCLE_PURGE_BATCH` ;
  3. les audits de ses ressources. Les audits des opérations d'un membre
     purgé sont conservés, avec un acteur anonymisé ; les alertes partagées
     dont il était propriétaire perdent leur propriétaire (`alert.updated.v1`).
- **Le plancher du flux ne bouge jamais.** Les événements retirés laissent un
  trou dans la séquence, dont aucun consommateur n'a besoin : l'événement
  `deleted` final donne l'issue. Aucun autre abonnement ne perd son
  historique ni ses livraisons en file.
- **Les identifiants sont retirés.** Une suppression purgée reste enregistrée,
  ainsi que celles des organisations et des membres d'un tenant purgé : leurs
  UUID sont refusés pour toujours (`409 resource_deleted`), pour qu'aucun
  client ne confonde une nouvelle ressource avec la ressource purgée. Rien
  d'autre n'est conservé : ni email, ni identité.
- **Les protections** sont des triggers, installés au démarrage seulement si
  le cycle de vie est activé. Désactivé, ils sont retirés, sauf si une
  suppression est enregistrée : une instance qui n'a jamais rien gelé ne paie
  rien, et les gels existants restent protégés. Désactivé, les routes
  répondent `409 lifecycle_disabled` et rien n'est purgé.
- **Aucun verrou global.** Un gel verrouille la ligne qu'il gèle ; une
  écriture verrouille les lignes de son tenant, de son organisation et de son
  membre, comme le fait une clé étrangère. Une écriture n'attend qu'un gel de
  son propre périmètre, puis échoue ; une écriture validée avant le gel est
  conservée. Une transaction de purge verrouille sa suppression et celles de
  son tenant, dont elle seule ignore les protections jusqu'à sa validation ;
  la purge d'un tenant n'attend jamais un autre tenant.

Une organisation suspendue n'accorde plus rien : ses adhésions, les
permissions de ses applications et leurs jetons sont refusés, et le proxy ne
sert aucun de ses modèles, administrateurs de plateforme compris.

L'interface web supprime toujours les tenants, organisations et membres
immédiatement, sans export.

## Erreurs

Toutes les erreurs partagent la même enveloppe :

```json
{"error": {"code": "last_owner", "message": "…"}}
```

| Code | HTTP | Cause |
| --- | --- | --- |
| `invalid_parameter` | 400 | Identifiant qui n'est pas un UUID canonique, ou paramètre de requête qu'une route commune ne définit pas. |
| `invalid_cursor` | 400 | Curseur altéré, vide, ou émis pour une autre collection, une autre limite ou le flux. |
| `invalid_precondition` | 400 | `If-Match` mal formé, ou `If-None-Match`. |
| `invalid_json` | 400 | Route commune : JSON mal formé, champ inconnu, valeur non textuelle, Unicode invalide. |
| `invalid_representation` | 400 | Route commune : champ requis absent, `null`, ou `identity` de membre invalide. |
| `invalid_hostname` | 400 | Nom d'hôte pas en minuscules, avec port, adresse IP ou label invalide. |
| `invalid_request` | 400 | `/v1/xolo` : corps mal formé, champ inconnu, paramètre de requête invalide. |
| `client_certificate_rejected` | 403 | Certificat ou URI client non autorisé. |
| `ownership_denied` | 403 | La politique d'autorité réserve la famille à l'instance locale. |
| `resource_deleted` | 409 | La ressource, ou une ressource qui la contient, est gelée par sa suppression. |
| `lifecycle_disabled` | 409 | Suppression d'un tenant, d'une organisation ou d'un membre alors que `XOLO_LIFECYCLE_ENABLED` est désactivé. |
| `export_mismatch` | 409 | Le condensat confirmé n'est pas celui de l'export actuel, ou un autre condensat est déjà confirmé. |
| `purge_not_ready` | 409 | La suppression n'est pas confirmée, ou sa rétention n'est pas écoulée. |
| `not_found` | 404 | Ressource ou route inconnue, ou ressource d'un autre tenant ou d'une autre organisation. |
| `parent_not_found` | 404 | Le tenant, l'organisation ou le membre dont dépend la ressource n'existe pas dans ce périmètre. |
| `method_not_allowed` | 405 | Ressource connue, mauvaise méthode. |
| `unsupported_media_type` | 415 | Route commune sans `Content-Type: application/json`. |
| `conflict` | 409 | Identifiant ou nom d'hôte détenu par un autre tenant, slug déjà utilisé, identité ou email détenu par un autre membre, ou invariant métier. |
| `last_owner` | 409 | La modification laisserait un tenant ou une organisation sans propriétaire actif. |
| `platform_admin_protected` | 409 | La modification vise un administrateur de plateforme. |
| `webhook_capacity` | 409 | Le tenant compte déjà le nombre maximal d'abonnements webhook. |
| `cursor_expired` | 410 | Curseur de liste de plus de 24 heures, ou curseur d'événements antérieur aux événements conservés : reconstruisez. |
| `precondition_failed` | 412 | `If-Match` ne désigne pas la révision actuelle. |
| `precondition_required` | 428 | Confirmation de purge sans `If-Match` explicite. |
| `unprocessable` | 422 | Valeur bien formée mais refusée par le domaine. |
| `rate_limited` | 429 | Budget par URI dépassé ; attendre les secondes indiquées par `Retry-After`. |
| `internal_error` | 500 | Erreur inattendue. |

Les messages sont toujours construits explicitement. Traces d'exécution, erreurs SQL, chemins de fichiers, détails TLS et secrets n'atteignent jamais le client : le détail complet est journalisé côté serveur.

## Invariants

- Une autorité d'écriture n'accorde jamais de privilège : la politique
  d'autorité décide seulement qui peut écrire une famille, et tous les autres
  invariants s'appliquent toujours.
- Le provisioning n'accorde ni ne modifie **jamais** de privilège de plateforme.
  Un utilisateur créé via `PUT /v1/xolo/tenants/{tenantID}/users` ou un `PUT` de
  membre reçoit exactement le rôle de plateforme `user`, les rôles de plateforme
  ne sont jamais modifiés, et un administrateur de plateforme n'est jamais
  modifié du tout.
- Une identité désigne au plus un compte par tenant. Une connexion ne fusionne
  jamais de comptes, ne relie jamais un compte déjà lié et ne rattache jamais un
  administrateur de plateforme.
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
- L'identifiant d'un tenant, d'une organisation ou d'un membre purgé n'est
  jamais réutilisé.
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
| `DELETE /v1/tenants/{tenantID}` | Même route, désormais différée : `202`, puis export, confirmation et purge, voir [Cycle de vie des ressources](#cycle-de-vie-des-ressources) ; `PUT /v1/tenants/{tenantID}` avec `"status": "suspended"` pour seulement suspendre |
| `GET /v1/tenants/{tenantID}/organizations[/{orgID}]` | `GET /v1/xolo/tenants/{tenantID}/organizations[/{orgID}]` |
| `POST /v1/tenants/{tenantID}/organizations` `{slug, name, description, currency, active, owner}` | `PUT /v1/tenants/{tenantID}/organizations/{orgID}` `{slug, name, status}` ; `description` et `currency` via `PATCH /v1/xolo/…/organizations/{orgID}` ; le propriétaire via `PUT …/organizations/{orgID}/members/{userID}` `{"role": "owner", "status": "active"}`, une fois déclaré par `PUT …/members/{userID}` |
| `PATCH /v1/tenants/{tenantID}/organizations/{orgID}` | `PATCH /v1/xolo/tenants/{tenantID}/organizations/{orgID}` (même corps) |
| `DELETE /v1/tenants/{tenantID}/organizations/{orgID}` | Même route, désormais différée comme un tenant ; `PUT …/organizations/{orgID}` avec `"status": "suspended"` pour seulement suspendre |
| `GET …/organizations/{orgID}/members[/{membershipID}]` | `GET /v1/xolo/…/organizations/{orgID}/members[/{membershipID}]` |
| `POST …/organizations/{orgID}/members` `{userId \| user, roleIds, builtinRoles}` | `PUT /v1/tenants/{tenantID}/organizations/{orgID}/members/{userID}` `{role, status}` ; rôles personnalisés via `PUT /v1/xolo/…/members/{membershipID}/roles` |
| `PUT …/members/{membershipID}/roles` | `PUT /v1/xolo/…/members/{membershipID}/roles` (même corps) |
| `DELETE …/members/{membershipID}` | `DELETE /v1/tenants/{tenantID}/organizations/{orgID}/members/{userID}`, par l'UUID du membre : `204` ; `PUT …` avec `"status": "suspended"` pour seulement suspendre |
| `…/organizations/{orgID}/roles[/{roleID}]` (toutes méthodes) | `/v1/xolo/…/organizations/{orgID}/roles[/{key}]`, avec les changements ci-dessous |
| `GET /v1/xolo/…/roles` (intégrés et personnalisés, `roleDTO` en camelCase) | `GET /v1/xolo/…/roles` liste les rôles personnalisés en page de projections ; les rôles intégrés via `GET /v1/xolo/…/roles/builtin` |
| `GET /v1/xolo/…/roles/{roleID}` (`roleDTO` en camelCase) | Même route : la représentation snake_case et son `ETag` |
| `POST /v1/xolo/…/roles` `{name, description?, permissions?, modelGrants?}` | Même route, la représentation snake_case complète `{name, description, permissions}` ; `201` avec `{key, representation, etag}`. Les accès aux modèles passent par les équipes : `/v1/xolo/…/team-settings/{teamID}` |
| `PUT /v1/xolo/…/roles/{roleID}` partiel `{name?, description?, permissions?, modelGrants?}` | Même route, la représentation complète, `If-Match` optionnel ; crée le rôle sous un UUID de votre choix |
| `GET`, `PUT /v1/tenants/{tenantID}/users` | `GET`, `PUT /v1/xolo/tenants/{tenantID}/users` (mêmes corps) |
| `GET`, `PUT`, `DELETE /v1/xolo/…/organizations/{orgID}/teams[/{key}]` (membres intégrés) | L'équipe via le contrat commun, `/v1/tenants/{tenantID}/organizations/{orgID}/teams[/{teamID}]` `{slug, name, status}`, chaque membre via `…/teams/{teamID}/members/{memberID}` `{status}`, le reste via `/v1/xolo/…/team-settings/{teamID}` |
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

**La migration `202610080001` exige aussi l'arrêt de tous les serveurs.** Elle
crée les projections et le flux d'événements, et donne à chaque ressource
existante sa propre révision sans publier d'événement : les consommateurs
commencent par un inventaire. Un ancien serveur encore actif écrirait sans
publier, et les projections, les ETags et le flux divergeraient sans bruit. La
migration ne peut pas être annulée.

## Transactions, audit et corrélation

Chaque mutation de provisioning revérifie ses parents et effectue toutes ses
écritures dans une transaction unique. Une erreur annule aussi les modifications
d'un utilisateur préexistant, les associations de rôles et l'audit. PostgreSQL utilise
`SERIALIZABLE` ; les conflits d'écriture SQLite et les conflits de sérialisation
PostgreSQL rejouent l'opération entière avec une attente bornée et annulable.
Seule une transaction qui modifie une projection prend le verrou du flux, à sa
toute fin (voir [Stockage et performances](#stockage-et-performances)).

`mutation_audits` conserve un état avant/après par ressource effectivement modifiée :
tenant, domaine, organisation, utilisateur, adhésion ou rôle, y compris les associations de
rôles et les suppressions en cascade. Les changements successifs sont regroupés ;
une opération sans changement ne produit pas d'audit. Les états excluent les secrets
et les horodatages techniques. L'historique et son périmètre tenant/organisation
survivent à la suppression des ressources. Les UUID d'audit ne donnent pas l'ordre
des commits. Seules les mutations de provisioning alimentent cet audit, ainsi que
les connexions qui créent, lient ou mettent à jour un compte, avec ce compte pour
acteur. L'état d'un utilisateur inclut son identité déclarée : contrairement aux
événements, la table d'audit la contient.

`X-Request-ID` doit contenir une seule valeur de 32 caractères hexadécimaux minuscules.
Toute valeur absente, répétée ou invalide est remplacée. La valeur retenue est renvoyée
dans la réponse et utilisée dans les journaux, l'audit et les événements, y compris
après reprise. L'URI de l'acteur provient exclusivement du certificat autorisé.
Sans acteur HTTP, les appels internes utilisent `urn:xolo:operator:local` avec une
corrélation générée au début de l'opération.

Les événements locaux de membres et de rôles gardent leurs types et messages,
y compris les événements distincts d'ajout de membre et d'attribution de rôles.
Ils sont émis après commit via le mécanisme asynchrone existant. Ils ne constituent
pas une outbox durable ; l'audit, les projections et le flux d'événements sont
persistés atomiquement avec la modification. Les lectures
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

- L'écriture des modèles LLM et des modèles virtuels personnels, en lecture seule ici ; les jetons d'application et paramètres d'événements : ils restent gérés par l'interface web.
- Les portées par certificat : tout URI autorisé administre l'instance entière.
- Aucune spécification OpenAPI n'est générée à ce jour.
