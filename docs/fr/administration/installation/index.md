# Installation & déploiement

Avant de mettre à niveau une base existante, suivez la [procédure de migration UUID](./uuid-migration.md). Tous les anciens réplicas et workers doivent être arrêtés.

Xolo est un binaire Go unique (`xolo-server`) qui embarque le serveur HTTP, l'interface web d'administration et le proxy LLM. Les plugins sont des binaires séparés, découverts au démarrage dans un répertoire dédié.

## Prérequis

- Un secret de 32 octets (`XOLO_SECRET_KEY`) pour chiffrer les clés API des fournisseurs en base. Générez-le avec `openssl rand -hex 32`.
- Un fournisseur d'authentification : OIDC générique, ou l'un des fournisseurs intégrés (Google, GitHub, Gitea). Voir [Configuration](./configuration.md#authentification).
- Une base SQLite (par défaut `data.sqlite`, dans le répertoire de travail) — aucune base externe n'est requise pour démarrer.

## Option 1 : image Docker

L'image officielle est publiée sur `ghcr.io/xolo-gateway/xolo-server` à chaque tag sémantique (`vX.Y.Z`), avec en plus le tag `latest`.

```bash
XOLO_SECRET_KEY=$(openssl rand -hex 32) # Devrait rester stable dans le temps
docker run -d \
  --name xolo \
  -p 3002:3002 \
  -e XOLO_SECRET_KEY="${XOLO_SECRET_KEY}" \
  -e XOLO_HTTP_BASE_URL=https://xolo.example.com \
  -e XOLO_HTTP_AUTHN_DEFAULT_ADMINS=admin@example.com \
  -v xolo-data:/data \
  ghcr.io/xolo-gateway/xolo-server:latest
```

Deux points spécifiques à l'image :

- `XOLO_STORAGE_DATABASE_DSN` vaut `/data/data.sqlite` par défaut : montez un volume sur `/data` pour persister la base entre redémarrages. Pour utiliser PostgreSQL à la place, positionnez cette variable sur un DSN `postgres://…` (voir [Configuration](./configuration.md)) ; le volume `/data` devient alors inutile.
- `XOLO_PLUGINS_DIR` vaut `/plugins` par défaut. L'image embarque déjà les plugins officiels (`time-restriction`, `dummy-model`, `fuzzy-evaluator`, `request-inspector`, `complexity-scorer`, `text-classifier`, `llm-classifier`, `energy-estimator`, `budget-pressure`, `script-processor`, `pseudonymizer`, `mcp-bridge`, `system-prompt`) à cet emplacement.
- Le port exposé est `3002`.

### Sondes de santé

Chaque instance répond sans authentification sur deux chemins, à destination
d'un répartiteur de charge ou d'un orchestrateur :

| Chemin     | Répond `200` quand…                                  | Usage                          |
| ---------- | ---------------------------------------------------- | ------------------------------ |
| `/healthz` | le processus sert des requêtes HTTP                  | sonde de vivacité (*liveness*) |
| `/readyz`  | l'instance joint aussi sa base de données (en 0,8 s) | sonde de disponibilité         |

`/readyz` répond `503` tant que la base est injoignable : l'instance sort du
répartiteur sans être redémarrée. Son résultat est partagé pendant 0,8 s :
un afflux de sondes ne coûte qu'une vérification à la base. Les deux chemins
passent avant la résolution du locataire, la journalisation, les métriques HTTP
et la limitation de débit : une sonde vise l'instance par son adresse, qui n'est
le domaine d'aucun locataire.

Sur PostgreSQL, `/readyz` passe par une connexion qui lui est propre, hors du
pool de l'application : un pool saturé par le trafic ne fait pas sortir toutes
les instances du répartiteur en même temps. La sonde dit si la base répond, pas
si l'instance est occupée. Comptez une connexion de plus par instance.

```yaml
healthcheck:
  test: ["CMD", "wget", "-qO-", "http://localhost:3002/readyz"]
```

Pour un déploiement avec `docker-compose`, ajoutez un service pointant vers cette image, un volume nommé pour `/data`, et les variables d'environnement décrites dans [Configuration](./configuration.md).

## Option 2 : paquets RPM et Debian

Chaque version publie sur sa [page GitHub](https://github.com/xolo-gateway/xolo/releases) des paquets aux formats `.rpm` et `.deb`, pour `x86_64` et `arm64` :

| Paquet                 | Contenu                                                                    |
| ---------------------- | -------------------------------------------------------------------------- |
| `xolo`                 | `xolo-server`, `xolo-migrate`, `xolo-adoption`, `xolo-admin`, le service systemd `xolo` |
| `xolo-plugin-<nom>`    | un plugin officiel, installé sous `/usr/lib/xolo/plugins` (un paquet par plugin : `xolo-plugin-pseudonymizer`, `xolo-plugin-mcp-bridge`…) |
| `xolo-plugin-all`      | aucun fichier : dépend de tous les paquets de plugins officiels            |

Les binaires sont liés statiquement : les paquets ne dépendent d'aucune bibliothèque du système. Ils sont installés et vérifiés à chaque modification sur Red Hat Enterprise Linux 10 et 9, Debian 13 et 12 ; les distributions qui en dérivent (AlmaLinux, Rocky Linux, Ubuntu) conviennent aussi.

Téléchargez le paquet `xolo` et ceux des plugins voulus, ou tous les paquets de la version avec `xolo-plugin-all`, puis installez-les ensemble :

```bash
# Red Hat Enterprise Linux, AlmaLinux, Rocky Linux : tout
sudo dnf install ./xolo-*.x86_64.rpm ./xolo-plugin-all-*.noarch.rpm

# Debian, Ubuntu : le serveur et deux plugins
sudo apt install ./xolo_*_amd64.deb ./xolo-plugin-pseudonymizer_*_amd64.deb ./xolo-plugin-mcp-bridge_*_amd64.deb
```

Le serveur ne découvre ses plugins qu'au démarrage : installer, mettre à jour ou retirer des paquets de plugins redémarre le service s'il tournait, une seule fois pour tous les paquets d'une même commande. Sur Red Hat, la relance qui suit une suppression a lieu 5 secondes après la commande.

Les paquets ne sont pas signés et ne sont pas servis par un dépôt `dnf` ou `apt` : contrôlez les fichiers téléchargés avec le `checksums.txt` de la version.

L'installation crée le compte système `xolo` et les fichiers suivants :

| Chemin                    | Rôle                                                                                           |
| ------------------------- | ---------------------------------------------------------------------------------------------- |
| `/etc/xolo/xolo.env`      | Configuration : une variable `XOLO_*` par ligne. Conservé lors des mises à jour.               |
| `/etc/xolo/secret.env`    | `XOLO_SECRET_KEY` et `XOLO_HTTP_SESSION_KEYS`, générés à la première installation, jamais remplacés. |
| `/var/lib/xolo`           | Base SQLite (`data.sqlite`), créée au premier démarrage.                                       |
| `/var/cache/xolo`         | Modèles téléchargés par le plugin `pseudonymizer`.                                             |

Sauvegardez `/etc/xolo/secret.env` avec la base : sans `XOLO_SECRET_KEY`, les clés API des fournisseurs qu'elle contient sont illisibles.

Le service n'est ni activé ni démarré par le paquet, puisqu'il lui faut d'abord un fournisseur d'authentification. Renseignez `/etc/xolo/xolo.env` (voir [Configuration](./configuration.md)), puis :

```bash
sudo systemctl enable --now xolo
sudo journalctl -u xolo -f
```

Une mise à jour du paquet redémarre le service s'il tournait. Sa désinstallation l'arrête et laisse en place la configuration, `/etc/xolo/secret.env` et la base.

Les commandes d'administration hors ligne `xolo-migrate` ([migration UUID](./uuid-migration.md)) et `xolo-adoption` ne lisent leur configuration que dans l'environnement. `xolo-admin` les lance avec celle du service (`/etc/xolo/xolo.env` et `/etc/xolo/secret.env`), sous le compte `xolo` : lancées en root, elles laisseraient à la base SQLite des fichiers que le service ne pourrait plus ouvrir.

```bash
sudo systemctl stop xolo
sudo xolo-admin migrate diagnose
sudo xolo-admin migrate plan -out /var/lib/xolo/recovery.json
```

Les fichiers qu'elles écrivent doivent être accessibles en écriture au compte `xolo`, sous `/var/lib/xolo` par exemple.

Sur Red Hat Enterprise Linux, ouvrez le port dans le pare-feu si l'instance est jointe sans mandataire inverse sur la même machine : `sudo firewall-cmd --permanent --add-port=3002/tcp && sudo firewall-cmd --reload`.

## Option 3 : binaire compilé depuis les sources

```bash
git clone https://github.com/xolo-gateway/xolo.git
cd xolo
cp .env.dist .env
# éditez .env : XOLO_SECRET_KEY, XOLO_HTTP_AUTHN_DEFAULT_ADMINS, un fournisseur d'auth…
make build          # compile bin/server et les plugins dans bin/plugins/
make CMD='bin/server' run-with-env
```

`make build` enchaîne `build-server`, `build-migrate`, `build-frontend` et la compilation de tous les plugins présents sous `plugins/`. Si vous modifiez des fichiers `.templ` ou le CSS Tailwind, lancez `make generate` avant `make build`.

Pour un rechargement à chaud en développement, `make watch` (nécessite `.env` et l'outil `modd`, récupéré automatiquement dans `tools/`).

## Premier démarrage

1. Renseignez `XOLO_HTTP_AUTHN_DEFAULT_ADMINS` avec l'adresse email du ou des comptes qui doivent être administrateurs dès leur première connexion.
2. Connectez-vous via le fournisseur d'authentification configuré : le compte correspondant à cette adresse email obtient automatiquement les droits d'administration.
3. Créez votre première organisation, puis suivez les [prochaines étapes](../../index.md#prochaines-etapes) : invitations, rôles, fournisseurs, budget.

## Pour aller plus loin

| Page                                         | Contenu                                          |
| -------------------------------------------- | ------------------------------------------------ |
| **[Configuration](./configuration.md)**      | Référence des variables d'environnement `XOLO_*` |
| **[Développement de plugins](./plugins.md)** | Écrire un plugin Go pour étendre Xolo            |
