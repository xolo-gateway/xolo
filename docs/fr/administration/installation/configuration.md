# Configuration

Xolo se configure entièrement par variables d'environnement, préfixées `XOLO_`. Le fichier `.env.dist` du dépôt sert de modèle de référence pour un déploiement depuis les sources ; en Docker, passez ces mêmes variables avec `-e` ou un fichier d'environnement.

## Secret et session

| Variable | Défaut | Description |
| --- | --- | --- |
| `XOLO_SECRET_KEY` | _(requis)_ | Clé hexadécimale de 32 octets utilisée pour chiffrer les clés API des fournisseurs (AES-GCM). Générez-la avec `openssl rand -hex 32`. Le serveur refuse de démarrer si elle est absente. |
| `XOLO_HTTP_SESSION_KEYS` | _(vide)_ | Liste de clés séparées par des virgules, utilisées pour signer/chiffrer les cookies de session. |

## HTTP

| Variable | Défaut | Description |
| --- | --- | --- |
| `XOLO_HTTP_ADDRESS` | `:3002` | Adresse d'écoute du serveur. |
| `XOLO_HTTP_BASE_URL` | `/` | URL publique de l'instance (utilisée pour construire les liens absolus, les redirections OIDC…). |
| `XOLO_HTTP_SHUTDOWN_TIMEOUT` | `30s` | Délai d'arrêt gracieux : sur SIGTERM/SIGINT le serveur cesse d'accepter des connexions puis laisse ce délai aux requêtes en cours (complétions streamées comprises) avant de les couper. Le délai de grâce du conteneur (`stop_grace_period` Docker, `TimeoutStopSec` systemd) doit lui être supérieur. |
| `XOLO_HTTP_SESSION_COOKIE_SECURE` | `false` | Passez à `true` derrière HTTPS. |
| `XOLO_HTTP_RATE_LIMIT_*` | — | Limitation de débit HTTP par IP. |

## Authentification

Xolo authentifie les utilisateurs via un ou plusieurs fournisseurs OAuth2/OIDC. Au moins un doit être configuré.

| Variable | Description |
| --- | --- |
| `XOLO_HTTP_AUTHN_DEFAULT_ADMINS` | Emails (séparés par des virgules) promus administrateurs plateforme dès leur première connexion. |
| `XOLO_HTTP_AUTHN_ACTIVE_BY_DEFAULT` | Si `true`, les nouveaux comptes sont actifs sans validation manuelle. |
| `XOLO_HTTP_AUTHN_PROVIDERS_GOOGLE_KEY` / `_SECRET` | Fournisseur Google OAuth2. |
| `XOLO_HTTP_AUTHN_PROVIDERS_GITHUB_KEY` / `_SECRET` | Fournisseur GitHub OAuth2. |
| `XOLO_HTTP_AUTHN_PROVIDERS_GITEA_KEY` / `_SECRET` / `_AUTH_URL` / `_TOKEN_URL` / `_PROFILE_URL` / `_DISCOVERY_URL` | Fournisseur Gitea auto-hébergé. `DISCOVERY_URL` est optionnel : sans lui, le fournisseur démarre avec un avertissement et le login interactif retombe sur `AUTH_URL` / `TOKEN_URL` ; avec lui, sa politique de validation est celle des [fournisseurs OIDC nommés](#fournisseurs-oidc-nommés). `DISCOVERY_URL` seul, ou `AUTH_URL` + `TOKEN_URL` (avec éventuellement `PROFILE_URL`), doivent être renseignés : un fournisseur configuré avec uniquement `KEY` et `SECRET` est refusé au démarrage. |

### Fournisseurs OIDC nommés

Pour un ou plusieurs fournisseurs OIDC génériques (Auth0, Keycloak, Authelia…), listez leurs identifiants puis configurez chacun sous un préfixe dédié :

```bash
XOLO_HTTP_AUTHN_OIDC_PROVIDERS=keycloak,auth0

XOLO_HTTP_AUTHN_OIDC_PROVIDER_KEYCLOAK_DISCOVERY_URL=https://idp.example.com/.well-known/openid-configuration
XOLO_HTTP_AUTHN_OIDC_PROVIDER_KEYCLOAK_KEY=xolo
XOLO_HTTP_AUTHN_OIDC_PROVIDER_KEYCLOAK_SECRET=change-me
XOLO_HTTP_AUTHN_OIDC_PROVIDER_KEYCLOAK_LABEL="Mon SSO"
XOLO_HTTP_AUTHN_OIDC_PROVIDER_KEYCLOAK_SCOPES=openid,profile,email
```

L'URL de découverte est obligatoire. Xolo télécharge le document au démarrage, avec un délai maximal de 10 secondes. Si le document est inaccessible ou incomplet, le serveur refuse de démarrer, en mode mono-tenant comme en multi-tenant. Le document doit contenir `issuer`, `authorization_endpoint` et `token_endpoint`. Chacun de ces champs, ainsi que `userinfo_endpoint`, `introspection_endpoint` et `end_session_endpoint` quand ils sont présents, doit être une URL absolue en `http` ou `https`.

Le champ `jwks_uri` n'est pas obligatoire : un IdP non conforme peut démarrer sans bloquer l'instance. Dans ce cas, Xolo émet un avertissement au démarrage qui cite le fournisseur concerné et indique que la **validation JWT des ID Tokens** est désactivée pour ce fournisseur. La validation d'introspection et de UserInfo reste active : le fournisseur reste enregistré dans la liste interne avec un `JWKSURL` vide, `oidctoken` ignore ce fournisseur (`errInvalidToken`, que la boucle d'authentification passe), tandis que `oauth2token` valide les jetons opaques via l'`introspection_endpoint` ou, à défaut, l'`userinfo_endpoint` documenté. C'est le cas type d'un IdP qui émet des jetons opaques validés par introspection — voir l'issue #27.

Pour valider des jetons d'accès opaques côté API (introspection RFC 7662, ou UserInfo à défaut) plutôt que des ID Tokens OIDC autoportés, activez `XOLO_HTTP_AUTHN_OAUTH2TOKEN_ENABLED=true`.

### Fournisseur Gitea

Le fournisseur Gitea est aligné sur les fournisseurs OIDC nommés dès que `XOLO_HTTP_AUTHN_PROVIDERS_GITEA_DISCOVERY_URL` est configuré : le document téléchargé doit alors contenir `issuer`, `authorization_endpoint` et `token_endpoint`. Si le document est inaccessible, mal formé ou incomplet, le serveur refuse de démarrer.

`XOLO_HTTP_AUTHN_PROVIDERS_GITEA_DISCOVERY_URL` est cependant **optionnel** : sans lui, Xolo démarre avec un avertissement de démarrage, et le login interactif utilise directement les champs `AUTH_URL` / `TOKEN_URL` / `PROFILE_URL` configurés. Lorsque `DISCOVERY_URL` est configuré et que le document est validé, ses `authorization_endpoint` et `token_endpoint` priment toujours sur la configuration statique : `validateOIDCDiscovery` exige que ces deux champs soient présents et absolus, donc la branche statique n'est consultée que sur le chemin `DISCOVERY_URL` vide (que la validation préalable refuse si `AUTH_URL` ou `TOKEN_URL` manquent). Pour `PROFILE_URL`, le document de découverte tient lieu de repli lorsqu'il publie un `userinfo_endpoint` et que le champ statique est vide ; avec `DISCOVERY_URL`, si `PROFILE_URL` est vide et que le document ne publie pas `userinfo_endpoint`, le fournisseur refuse de démarrer avec un message explicite plutôt que d'échouer à la première connexion.

Qu'il passe par la découverte ou par la configuration statique, un fournisseur Gitea a besoin d'au moins **un** de ces deux chemins :

- `DISCOVERY_URL` renseigné et résolu (avec un document complet),
- `AUTH_URL` **et** `TOKEN_URL` renseignés (avec éventuellement `PROFILE_URL`).

Un fournisseur configuré avec uniquement `KEY` / `SECRET` est refusé au démarrage avec l'erreur `gitea provider requires either DISCOVERY_URL or both AUTH_URL and TOKEN_URL`. C'est un changement de comportement par rapport aux versions précédentes, qui acceptaient ce mode et échouaient à la première connexion ; l'erreur explicite permet aujourd'hui de repérer la mauvaise configuration dès le boot.

Comme pour les fournisseurs OIDC nommés, `jwks_uri` est optionnel : en son absence, ou lorsqu'il est présent mais mal formé (URL relative, scheme non `http(s)`), un avertissement est émis au démarrage et la validation JWT des ID Tokens est désactivée pour ce fournisseur — l'introspection reste active si le document la publie.

## Stockage

| Variable | Défaut | Description |
| --- | --- | --- |
| `XOLO_STORAGE_DATABASE_DSN` | `data.sqlite` | Base de données. Un DSN commençant par `postgres://` (ou une chaîne libpq `host=… dbname=…`) cible PostgreSQL ; toute autre valeur est interprétée comme un chemin de fichier SQLite. |
| `XOLO_STORAGE_DATABASE_POOL_MAX_OPEN_CONNS` | `25` | Connexions ouvertes maximum (PostgreSQL uniquement ; SQLite reste sur une seule connexion). |
| `XOLO_STORAGE_DATABASE_POOL_MAX_IDLE_CONNS` | `5` | Connexions inactives conservées dans le pool (PostgreSQL uniquement). |
| `XOLO_STORAGE_DATABASE_POOL_CONN_MAX_LIFETIME` | `60m` | Durée de vie maximale d'une connexion (PostgreSQL uniquement). |
| `XOLO_STORAGE_DATABASE_CACHE_USERS_*` / `_PROVIDERS_*` | — | Taille et TTL des caches en mémoire pour les utilisateurs et fournisseurs (activés par défaut, 25 entrées, 60 min). |
| `XOLO_STORAGE_DATABASE_CACHE_QUOTA_ENABLED` | `true` | Réutilise les totaux de dépense lus à chaque requête proxy pour la vérification des budgets, au lieu de les relire à chaque fois. Les totaux eux-mêmes viennent de compteurs journaliers tenus à l'enregistrement de l'usage, pas d'une agrégation de l'historique. |
| `XOLO_STORAGE_DATABASE_CACHE_QUOTA_SIZE` | `4096` | Nombre maximal de totaux conservés. Chaque organisation active en occupe trois (jour, mois, année), plus trois par utilisateur actif. |
| `XOLO_STORAGE_DATABASE_CACHE_QUOTA_TTL` | `10s` | Durée de réutilisation d'un total. Les dépenses sont appliquées aux totaux en cache au fil des enregistrements, donc une instance seule reste exacte pendant toute la durée ; avec plusieurs instances, c'est le délai pendant lequel l'une peut ignorer la dépense d'une autre. `0` relit les compteurs à chaque vérification. |

### Choisir entre SQLite et PostgreSQL

SQLite reste le défaut : aucune dépendance externe, idéal pour une instance unique. PostgreSQL est recommandé dès que plusieurs instances de Xolo partagent le même stockage, ou lorsque le volume d'événements et d'enregistrements d'usage devient important.

```bash
XOLO_STORAGE_DATABASE_DSN="postgres://xolo:motdepasse@postgres:5432/xolo?sslmode=disable"
```

Le schéma est créé et migré automatiquement au démarrage. Il n'existe pas de migration automatique des données d'une base SQLite existante vers PostgreSQL : la bascule suppose une base neuve.

## Plugins

| Variable | Défaut | Description |
| --- | --- | --- |
| `XOLO_PLUGINS_DIR` | `./plugins` | Répertoire scanné au démarrage pour les binaires de plugins exécutables. |
| `XOLO_PLUGINS_MEM_LIMIT` | _(désactivé)_ | Limite mémoire (`GOMEMLIMIT`) appliquée à chaque sous-processus plugin, ex. `512MiB`. |
| `XOLO_PLUGINS_RESTART_COOLDOWN` | `30s` | Délai minimum entre deux redémarrages d'un même plugin, pour éviter les redémarrages en boucle. |

## Proxy

| Variable | Défaut | Description |
| --- | --- | --- |
| `XOLO_PROXY_UPSTREAM_TIMEOUT` | `5m` | Délai maximal accordé à un fournisseur. Pour une complétion non streamée ou un calcul d'embeddings, il borne l'appel complet, tentatives de retry comprises. Pour une complétion streamée, il borne l'attente du premier fragment puis le silence entre deux fragments, sans jamais couper une réponse longue qui continue d'arriver. À expiration le client reçoit une erreur 504 explicite au lieu d'une coupure opaque du reverse proxy. Le timeout du reverse proxy placé devant Xolo doit rester strictement supérieur à cette valeur. `0` désactive la borne. |
| `XOLO_PROXY_ACTIVE_USER_CACHE_TTL` | `30s` | Durée de réutilisation du nombre d'utilisateurs actifs sur un forfait par abonnement, qui sert de dénominateur à la [répartition entre utilisateurs](../organisation/fournisseurs/fournisseurs.md#repartition-entre-utilisateurs). Ce comptage parcourt toute la fenêtre du forfait dans la table d'usage ; il est pris une fois par forfait et par fenêtre, partagé par tous ses utilisateurs, et une valeur expirée est servie telle quelle pendant qu'il est repris en arrière-plan. Une valeur ancienne peut manquer les utilisateurs qui ont consommé pour la première fois depuis qu'elle a été prise, ce qui élargit les parts dans la limite du budget du forfait. À augmenter quand la table d'usage est volumineuse. `0` recompte à chaque requête. |
| `XOLO_PROXY_ACTIVE_USER_COUNT_TIMEOUT` | `10s` | Durée maximale d'un comptage d'utilisateurs actifs. Le comptage tourne indépendamment de la requête proxy qui l'a déclenché, et la première requête d'une fenêtre ne l'attend qu'une seconde avant de calculer sa part sur l'effectif complet. Au-delà de cette durée le comptage est abandonné, l'échec est mémorisé pour la durée du cache et la part de chaque utilisateur retombe sur l'effectif complet. Si le compteur `xolo_fair_share_degraded_shares_total` monte en continu, c'est ce délai qu'il faut relever, ou l'index de la table d'usage qu'il faut vérifier. |

## Événements

| Variable | Défaut | Description |
| --- | --- | --- |
| `XOLO_EVENTS_MAX_PER_ORG` | `100000` | Plafond global d'événements non épinglés conservés par organisation. |
| `XOLO_EVENTS_DEFAULT_PER_ORG` | `10000` | Rétention appliquée aux organisations sans réglage explicite (voir [Paramètres](../organisation/parametres/parametre.md)). |
| `XOLO_EVENTS_EVALUATION_INTERVAL` | `30s` | Fréquence d'évaluation des alertes. |
| `XOLO_EVENTS_PURGE_INTERVAL` | `5m` | Fréquence de purge de la fenêtre glissante. |

## API de provisioning

Écouteur d'administration dédié, protégé par TLS mutuel. Voir [API de provisioning](../provisioning/provisioning.md).

| Variable | Défaut | Description |
| --- | --- | --- |
| `XOLO_PROVISIONNING_API_ENABLED` | `false` | Ouvre l'écouteur d'administration. |
| `XOLO_PROVISIONNING_API_ADDRESS` | `:3003` | Adresse d'écoute. |
| `XOLO_PROVISIONNING_API_TLS_CERT_FILE` | _(requis si activé)_ | Certificat serveur (PEM). |
| `XOLO_PROVISIONNING_API_TLS_KEY_FILE` | _(requis si activé)_ | Clé privée du serveur (PEM). |
| `XOLO_PROVISIONNING_API_TLS_CLIENT_CA_FILE` | _(requis si activé)_ | Autorité vérifiant les certificats clients. |
| `XOLO_PROVISIONNING_API_SHUTDOWN_TIMEOUT` | `10s` | Délai d'arrêt gracieux. |

## Multi-tenant

Désactivé par défaut : l'instance possède un unique tenant `default`, créé par la migration et jamais exposé à ses utilisateurs — pas de sous-domaine, pas de changement d'URL. Une fois activé, le tenant est identifié par l'hôte de la requête, et un hôte ne correspondant à aucun tenant répond 404 : un sous-domaine inconnu ne doit pas révéler que l'instance existe.

Les tenants se créent exclusivement par l'[API de provisioning](../provisioning/provisioning.md) ; `POST /v1/tenants` répond 409 tant que `XOLO_MULTITENANCY_ENABLED` vaut `false`.

| Variable | Défaut | Description |
| --- | --- | --- |
| `XOLO_MULTITENANCY_ENABLED` | `false` | Active la résolution du tenant par nom d'hôte. |
| `XOLO_MULTITENANCY_HOST_PATTERN` | _(requis si activé)_ | Gabarit d'hôte contenant exactement une fois le marqueur `{tenant}`, par exemple `{tenant}.xolo.example.com`. Il peut porter un port, ignoré lors de la comparaison. Le serveur refuse de démarrer s'il est absent ou mal formé. |
| `XOLO_MULTITENANCY_DEFAULT_TENANT_SLUG` | `default` | Tenant servi lorsque le multi-tenant est désactivé. |

### Conséquences sur la configuration HTTP

`XOLO_HTTP_BASE_URL` doit alors être **absolue** — schéma et hôte — sous peine de refus de démarrage. Elle n'est plus l'URL de l'instance mais le gabarit dont chaque tenant dérive la sienne : son schéma et son chemin sont conservés, son hôte est remplacé par celui de la requête. Liens, redirections et URL de rappel OAuth restent ainsi sur l'hôte du tenant, ce qui est nécessaire : le cookie de session est propre à un hôte et la session porte le tenant sur lequel elle a été ouverte.

L'URL de rappel à déclarer auprès du fournisseur d'identité varie donc par tenant :

```
https://{tenant}.xolo.example.com/auth/oidc/providers/{fournisseur}/callback
```

Un fournisseur acceptant un `redirect_uri` à joker (Keycloak, Authentik…) couvre tous les tenants d'une seule déclaration. Une OAuth App GitHub n'accepte qu'une URL de rappel unique et Google n'accepte que des URI exactes : ces deux fournisseurs imposent une déclaration par sous-domaine, ce qui les rend impraticables au-delà de quelques tenants.

Prévoyez enfin un DNS et un certificat TLS joker (`*.xolo.example.com`) sur le reverse proxy placé devant Xolo, et passez `XOLO_HTTP_SESSION_COOKIE_SECURE=true`.

## Taux de change et tâches de fond

| Variable | Défaut | Description |
| --- | --- | --- |
| `XOLO_EXCHANGE_RATE_PROVIDER` | `frankfurter` | Source des taux de change (`frankfurter` ou `file`). |
| `XOLO_EXCHANGE_RATE_TTL` / `_REFRESH_INTERVAL` | `24h` | Fraîcheur et fréquence de rafraîchissement des taux. |
| `XOLO_TASK_RUNNER_URI` | `memory://taskrunner?parallelism=5&cleanupInterval=10m&cleanupDelay=1h` | Configuration du planificateur de tâches de fond. |
| `XOLO_LOGGER_LEVEL` | `0` | Niveau de log (`slog`, valeurs négatives = debug). Au niveau `0` (info) seules les requêtes SQL en erreur ou lentes sont journalisées ; le niveau debug (`-4`) trace chaque requête SQL. |

## Vérification

Le fichier `.env.dist` à la racine du dépôt liste l'ensemble des variables avec leurs commentaires. En cas de configuration invalide (secret manquant, fournisseur OIDC mal formé…), `xolo-server` refuse de démarrer et affiche l'erreur en clair sur la sortie standard.
