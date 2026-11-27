# Premiers pas

Cette page mène d'une machine vide à une première requête qui traverse Xolo, en cinq étapes. Comptez une vingtaine de minutes, dont l'essentiel pour déclarer Xolo auprès de votre fournisseur d'identité.

À la fin, vous aurez une instance locale, une organisation, un fournisseur de modèles, une clé API, et l'événement de votre première requête sous les yeux.

## Ce qu'il vous faut

- Docker, pour lancer le serveur sans rien compiler.
- Un fournisseur d'identité : Xolo ne gère pas de mots de passe et délègue la connexion à Google, GitHub, Gitea ou tout fournisseur OpenID Connect.
- Une clé API chez un fournisseur de modèles (OpenAI, Mistral, OpenRouter…), ou un serveur local comme Ollama.

## Étape 1 : lancer le serveur

Xolo est un seul binaire, avec une base SQLite par défaut. Il lui faut une clé secrète, qui chiffre les clés API des fournisseurs, l'adresse sous laquelle il est joint, et de quoi connecter un premier administrateur.

L'exemple utilise GitHub. Créez une application OAuth dans les paramètres développeur de GitHub, avec `http://localhost:3002` comme adresse de l'application, puis relevez son identifiant et son secret. L'URL de retour à déclarer et les autres fournisseurs sont décrits dans [Configuration](../exploitation/configuration.md).

```bash
docker run -d \
  --name xolo \
  -p 3002:3002 \
  -e XOLO_SECRET_KEY="$(openssl rand -hex 32)" \
  -e XOLO_HTTP_BASE_URL=http://localhost:3002 \
  -e XOLO_HTTP_AUTHN_DEFAULT_ADMINS=vous@example.com \
  -e XOLO_HTTP_AUTHN_PROVIDERS_GITHUB_KEY=<identifiant> \
  -e XOLO_HTTP_AUTHN_PROVIDERS_GITHUB_SECRET=<secret> \
  -v xolo-data:/data \
  ghcr.io/xolo-gateway/xolo-server:latest
```

Remplacez `vous@example.com` par l'adresse de votre compte GitHub : ce compte devient administrateur de la plateforme à sa première connexion.

Pour un essai, la clé secrète générée à la volée suffit. Sur une instance durable, conservez-la : sans elle, les clés API enregistrées ne se déchiffrent plus. Les autres modes d'installation (paquets RPM et Debian, binaire) sont dans [Installation](../exploitation/installation.md).

## Étape 2 : se connecter et créer une organisation

Une organisation regroupe des membres, des fournisseurs, des budgets et des modèles. Rien ne se configure en dehors d'elle : il en faut une avant d'aller plus loin.

Ouvrez `http://localhost:3002` et connectez-vous. Dans l'administration de la plateforme, créez une organisation avec **Nouvelle organisation**. Son identifiant court, par exemple `acme`, préfixera le nom de tous ses modèles.

## Étape 3 : connecter un fournisseur et ses modèles

Xolo ne produit aucune réponse lui-même : il relaie les requêtes vers un fournisseur dont il garde la clé. Vos applications ne verront jamais cette clé, seulement celle que Xolo leur délivre.

Dans le menu **Fournisseurs** de l'organisation, cliquez sur **Ajouter un fournisseur** et renseignez son type, son URL de base et sa clé API. Ouvrez ensuite ses modèles et cliquez sur **Importer depuis le fournisseur** : Xolo lit la liste des modèles et les déclare pour vous.

Chaque modèle est publié sous le nom `<organisation>/<nom proxy>`, par exemple `acme/gpt-4o-mini`. C'est ce nom que vos applications demanderont. Le détail des champs, les serveurs locaux et les abonnements sont dans [Fournisseurs](../organisation/fournisseurs.md).

## Étape 4 : créer une clé API

Une requête s'authentifie avec une clé délivrée par Xolo. Une clé personnelle agit en votre nom et consomme votre budget ; c'est la plus simple pour un premier essai.

Dans votre profil, ouvrez **Clés API**, cliquez sur **Nouvelle clé** et choisissez l'organisation. La clé ne s'affiche qu'une fois : copiez-la.

Pour un service plutôt qu'une personne, créez une [application](../organisation/applications.md) et générez-lui un jeton.

## Étape 5 : envoyer une requête

Xolo expose une API compatible avec celle d'OpenAI, sous `/api/v1`. Un client prévu pour OpenAI fonctionne en changeant deux réglages : l'URL de base et la clé.

```bash
curl http://localhost:3002/api/v1/chat/completions \
  -H "Authorization: Bearer $XOLO_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "acme/gpt-4o-mini",
    "messages": [{"role": "user", "content": "Bonjour !"}]
  }'
```

`GET /api/v1/models`, avec la même clé, liste les modèles auxquels elle donne accès.

Ouvrez ensuite la page **Événements** de l'organisation : la requête y figure, avec son modèle, ses tokens, son coût et sa latence. Le [tableau de bord](../utilisation/tableau-de-bord.md) agrège ces mêmes données.

## Et ensuite

| Vous voulez | Allez voir |
| --- | --- |
| Inviter vos collègues et régler leurs droits | [Invitations](../organisation/invitations.md), [Rôles](../organisation/roles.md), [Équipes](../organisation/equipes.md) |
| Plafonner les dépenses | [Budget](../organisation/budget.md) |
| Être prévenu quand quelque chose se passe | [Événements et alertes](../utilisation/evenements.md) |
| Router, filtrer ou enrichir les requêtes | [Modèles virtuels](../organisation/modeles-virtuels.md), puis les [guides](../guides/index.md) |
| Comprendre comment tout cela s'articule | [Découvrir](../decouvrir/index.md) |
