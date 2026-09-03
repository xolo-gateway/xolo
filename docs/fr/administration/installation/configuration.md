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
| `XOLO_HTTP_AUTHN_PROVIDERS_GITEA_KEY` / `_SECRET` / `_AUTH_URL` / `_TOKEN_URL` / `_PROFILE_URL` | Fournisseur Gitea auto-hébergé. |

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

Pour valider des jetons d'accès opaques côté API (introspection RFC 7662, ou UserInfo à défaut) plutôt que des ID Tokens OIDC autoportés, activez `XOLO_HTTP_AUTHN_OAUTH2TOKEN_ENABLED=true`.

## Stockage

| Variable | Défaut | Description |
| --- | --- | --- |
| `XOLO_STORAGE_DATABASE_DSN` | `data.sqlite` | Base de données. Un DSN commençant par `postgres://` (ou une chaîne libpq `host=… dbname=…`) cible PostgreSQL ; toute autre valeur est interprétée comme un chemin de fichier SQLite. |
| `XOLO_STORAGE_DATABASE_POOL_MAX_OPEN_CONNS` | `25` | Connexions ouvertes maximum (PostgreSQL uniquement ; SQLite reste sur une seule connexion). |
| `XOLO_STORAGE_DATABASE_POOL_MAX_IDLE_CONNS` | `5` | Connexions inactives conservées dans le pool (PostgreSQL uniquement). |
| `XOLO_STORAGE_DATABASE_POOL_CONN_MAX_LIFETIME` | `60m` | Durée de vie maximale d'une connexion (PostgreSQL uniquement). |
| `XOLO_STORAGE_DATABASE_CACHE_USERS_*` / `_PROVIDERS_*` | — | Taille et TTL des caches en mémoire pour les utilisateurs et fournisseurs (activés par défaut, 25 entrées, 60 min). |

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

## Taux de change et tâches de fond

| Variable | Défaut | Description |
| --- | --- | --- |
| `XOLO_EXCHANGE_RATE_PROVIDER` | `frankfurter` | Source des taux de change (`frankfurter` ou `file`). |
| `XOLO_EXCHANGE_RATE_TTL` / `_REFRESH_INTERVAL` | `24h` | Fraîcheur et fréquence de rafraîchissement des taux. |
| `XOLO_TASK_RUNNER_URI` | `memory://taskrunner?parallelism=5&cleanupInterval=10m&cleanupDelay=1h` | Configuration du planificateur de tâches de fond. |
| `XOLO_LOGGER_LEVEL` | `0` | Niveau de log (`slog`, valeurs négatives = debug). |

## Vérification

Le fichier `.env.dist` à la racine du dépôt liste l'ensemble des variables avec leurs commentaires. En cas de configuration invalide (secret manquant, fournisseur OIDC mal formé…), `xolo-server` refuse de démarrer et affiche l'erreur en clair sur la sortie standard.
