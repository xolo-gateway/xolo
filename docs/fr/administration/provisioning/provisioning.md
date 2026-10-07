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

Chacune de ces ressources peut aussi être lue, listée et suivie via le flux
d'événements : voir [Lectures, conditions et synchronisation](#lectures-conditions-et-synchronisation).
`capabilities` liste `conditional_writes`, `events`, `identity` et `reads`.

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
renvoyés par le fournisseur d'identité.

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

## Lectures, conditions et synchronisation

Chaque ressource du contrat commun a une **projection** : la représentation que
renvoie son `PUT`, maintenue dans la même transaction que la ressource. Toute
modification d'une projection, quelle qu'en soit l'origine — cette API,
l'interface web, une connexion, une invitation acceptée, une suppression —
reçoit la position suivante d'un **flux d'événements** unique et publie un
événement.

| Méthode | Route | Réponse |
| --- | --- | --- |
| `GET` | `/v1/tenants/{tenantID}`, `…/domains/{hostname}`, `…/organizations/{orgID}`, `…/members/{memberID}`, `…/organizations/{orgID}/members/{memberID}` | La représentation, avec son en-tête `ETag` |
| `GET` | `/v1/tenants`, `…/domains`, `…/organizations`, `…/members`, `…/organizations/{orgID}/members` | `{"items":[{"key","representation","etag"}],"next_cursor"}` |
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
  `resource_type` est `tenant`, `tenant_domain`, `organization`, `member` ou
  `organization_membership`. Une suppression ne porte pas d'`etag`.
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
| `invalid_parameter` | 400 | Identifiant qui n'est pas un UUID canonique, ou paramètre de requête qu'une route commune ne définit pas. |
| `invalid_cursor` | 400 | Curseur altéré, vide, ou émis pour une autre collection, une autre limite ou le flux. |
| `invalid_precondition` | 400 | `If-Match` mal formé, ou `If-None-Match`. |
| `invalid_json` | 400 | Route commune : JSON mal formé, champ inconnu, valeur non textuelle, Unicode invalide. |
| `invalid_representation` | 400 | Route commune : champ requis absent, `null`, ou `identity` de membre invalide. |
| `invalid_hostname` | 400 | Nom d'hôte pas en minuscules, avec port, adresse IP ou label invalide. |
| `invalid_request` | 400 | `/v1/xolo` : corps mal formé, champ inconnu, paramètre de requête invalide. |
| `client_certificate_rejected` | 403 | Certificat ou URI client non autorisé. |
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
| `unprocessable` | 422 | Valeur bien formée mais refusée par le domaine. |
| `rate_limited` | 429 | Budget par URI dépassé ; attendre les secondes indiquées par `Retry-After`. |
| `internal_error` | 500 | Erreur inattendue. |

Les messages sont toujours construits explicitement. Traces d'exécution, erreurs SQL, chemins de fichiers, détails TLS et secrets n'atteignent jamais le client : le détail complet est journalisé côté serveur.

## Invariants

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
| `POST /v1/tenants/{tenantID}/organizations` `{slug, name, description, currency, active, owner}` | `PUT /v1/tenants/{tenantID}/organizations/{orgID}` `{slug, name, status}` ; `description` et `currency` via `PATCH /v1/xolo/…/organizations/{orgID}` ; le propriétaire via `PUT …/organizations/{orgID}/members/{userID}` `{"role": "owner", "status": "active"}`, une fois déclaré par `PUT …/members/{userID}` |
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

- Les fournisseurs, modèles LLM, modèles virtuels, middlewares, applications et leurs jetons, quotas, alertes et paramètres d'événements : ils restent gérés par l'interface web.
- Les portées par certificat : tout URI autorisé administre l'instance entière.
- La suppression de tenants, domaines, organisations ou adhésions : suspendez-les.
- Aucune spécification OpenAPI n'est générée à ce jour.
