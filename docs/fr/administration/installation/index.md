# Installation & déploiement

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

Pour un déploiement avec `docker-compose`, ajoutez un service pointant vers cette image, un volume nommé pour `/data`, et les variables d'environnement décrites dans [Configuration](./configuration.md).

## Option 2 : binaire compilé depuis les sources

```bash
git clone https://github.com/xolo-gateway/xolo.git
cd xolo
cp .env.dist .env
# éditez .env : XOLO_SECRET_KEY, XOLO_HTTP_AUTHN_DEFAULT_ADMINS, un fournisseur d'auth…
make build          # compile bin/server et les plugins dans bin/plugins/
make CMD='bin/server' run-with-env
```

`make build` enchaîne `build-server`, `build-frontend` et la compilation de tous les plugins présents sous `plugins/`. Si vous modifiez des fichiers `.templ` ou le CSS Tailwind, lancez `make generate` avant `make build`.

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
