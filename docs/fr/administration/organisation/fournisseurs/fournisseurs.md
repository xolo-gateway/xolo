# Fournisseurs

![Panneau de gestion des fournisseurs](./screenshots/image1.png)

## Qu'est-ce qu'un fournisseur ?

Un fournisseur est une connexion vers un service LLM externe (OpenAI, Mistral, OpenRouter, etc.). Il expose un ou plusieurs modèles que les utilisateurs de l'organisation peuvent consommer.

## Accéder aux fournisseurs

1. Allez dans votre organisation : `/orgs/{slug}/`
2. Cliquez sur **Fournisseurs** dans le menu admin

> **Note** : Vous devez disposer de la permission `providers:write` pour créer ou modifier des fournisseurs.

## Créer un fournisseur

1. Cliquez sur **Ajouter un fournisseur** (bouton en haut à droite)
   ![Ajouter un fournisseur](./screenshots/image2.png)

2. Remplissez les informations du fournisseur :
   ![Formulaire fournisseur](./screenshots/image3.png)

   ### Champs du formulaire

   | Champ                       | Description                                                                                           |
   | --------------------------- | ----------------------------------------------------------------------------------------------------- |
   | **Nom**                     | Nom affiché du fournisseur                                                                            |
   | **Type**                    | Type de connexion : `openai`, `mistral`, `openrouter`, `yzma`                                         |
   | **URL de base**             | URL de l'endpoint API (ex: `https://api.openai.com/v1`)                                               |
   | **Clé API**                 | Clé d'authentification auprès du fournisseur                                                          |
   | **Devise**                  | Devise pour la tarification (USD, EUR, etc.)                                                          |
   | **Niveau d'infrastructure** | Type d'hébergement (Hyperscaler, Major Cloud, Small Provider) — utilisé pour l'estimation énergétique |
   | **Mode de facturation**     | **Pay-as-you-go** : facturation à l'usage · **Abonnement** : plan à tokens                            |

3. Cliquez sur **Enregistrer**.

## Serveurs locaux : Ollama, vLLM et compatibles OpenAI

Xolo n'a pas de type de connexion propre à Ollama ou à vLLM, et n'en a pas besoin : ces serveurs exposent l'API au format OpenAI. Déclarez-les avec le type `openai` :

| Champ           | Ollama                                   | vLLM                                   |
| --------------- | ---------------------------------------- | -------------------------------------- |
| **Type**        | `openai`                                 | `openai`                               |
| **URL de base** | `http://localhost:11434/v1`              | `http://localhost:8000/v1`             |
| **Clé API**     | vide, sauf si vous en avez configuré une | celle passée à `--api-key`, sinon vide |

Adaptez l'hôte et le port à votre déploiement. Pour l'estimation énergétique, choisissez le niveau d'infrastructure **Small Provider** : c'est celui qui correspond le mieux à une machine que vous opérez vous-même. La même recette vaut pour tout serveur qui parle le format OpenAI (LM Studio, llama.cpp en mode serveur, LocalAI…).

## Tester la connexion

Après avoir créé un fournisseur, utilisez le bouton **Tester la connexion** pour vérifier que Xolo peut communiquer avec le fournisseur.

## Fournisseurs en abonnement : plan et répartition

Un fournisseur en mode **Abonnement** ne facture pas à l'usage : il vend un volume
global (des tokens, ou une valeur exprimée dans la devise du fournisseur) pour
toute l'organisation, sur une fenêtre qui se réinitialise. Le plan décrit ces
limites sous forme de **contraintes**.

### Contraintes

| Type                     | Champs                                           | Rôle                                                              |
| ------------------------ | ------------------------------------------------ | ----------------------------------------------------------------- |
| **Fenêtre glissante**    | Durée, budget tokens, budget valeur              | Volume consommable sur les N dernières heures                     |
| **Fenêtre fixe**         | Idem + « Reset dans »                            | Idem, mais alignée sur l'heure de réinitialisation du fournisseur |
| **Concurrence**          | Nombre max de requêtes simultanées               | Requêtes en vol autorisées en même temps                          |

Le champ « Reset dans » se recopie depuis l'écran du fournisseur (par exemple
`4h29m`) : Xolo le convertit en un ancrage absolu, de sorte que ses fenêtres
coïncident avec les réinitialisations réelles du forfait.

### Répartition entre utilisateurs

Le budget est commun, mais un utilisateur ne peut pas le consommer en entier. Sa
part se compose de deux termes :

- un **plancher garanti**, réparti à parts égales entre tous les membres de
  l'organisation, qu'ils consomment ou non : c'est ce qui protège un utilisateur
  occasionnel de ceux qui consomment beaucoup. C'est une réservation, non une
  simple part : la portion commune est plafonnée par ce qui reste réellement
  distribuable une fois les planchers des absents mis de côté, de sorte qu'un
  membre arrivant tard dans la fenêtre y retrouve bien sa part. Ce plancher vaut,
  par défaut, 30 % de l'ancienne part fixe `budget / membres` : il est plus bas
  que ce que l'ancienne règle garantissait, en échange d'une part nominale bien
  plus large dès que des membres restent inactifs. Relever la réserve remonte le
  plancher, mais reprend directement sur la portion commune ;
- le **reste du budget**, réparti entre les seuls utilisateurs **actifs dans la
  fenêtre**. Sur une organisation de vingt personnes dont trois utilisent
  réellement le forfait, chacune de ces trois dispose ainsi d'environ un quart du
  plan, au lieu d'un vingtième.

Deux mécanismes complètent cette répartition :

- le **rythme** : tant que la consommation globale reste en phase avec
  l'écoulement de la fenêtre, la part commune est pleinement ouverte ; si le
  forfait se consomme plus vite que la fenêtre ne s'écoule, les parts se
  resserrent progressivement vers le plancher garanti, puis se rouvrent quand la
  consommation rejoint son rythme ;
- l'**ouverture de fin de fenêtre** : dans les derniers instants avant la
  réinitialisation, le reliquat serait détruit ; il est alors réparti entre les
  utilisateurs actifs plutôt que réservé aux membres absents. Cette ouverture ne
  peut qu'élargir une part : si la répartition du reliquat donnait moins que la
  part courante, c'est la part courante qui s'applique.

Ces quatre réglages sont facultatifs et se trouvent sous « Répartition entre
utilisateurs » dans le formulaire de la contrainte. Laissés vides, ils prennent
leurs valeurs par défaut.

| Réglage                            | Défaut | Effet                                                                                        |
| ---------------------------------- | ------ | -------------------------------------------------------------------------------------------- |
| **Réserve garantie**               | 30 %   | Part du budget réservée à parts égales entre tous les membres                                 |
| **Tolérance de rythme**            | 15 %   | Avance de consommation tolérée avant que les parts ne se resserrent                           |
| **Ouverture de fin de fenêtre**    | 90 %   | Fraction de la fenêtre au-delà de laquelle le reliquat est réparti entre les actifs ; 100 désactive |
| **Avance maximale de l'ouverture** | 5 % de la fenêtre, entre 30 min et 4 h | Borne absolue de cette ouverture : 30 min avant le reset d'une fenêtre de 5 h, 4 h avant celui d'une fenêtre hebdomadaire |

Une valeur illisible ou hors bornes est refusée à l'enregistrement, avec un
message nommant la contrainte concernée : un réglage affiché doit être celui que
le moteur applique. L'ouverture de fin de fenêtre doit être strictement
supérieure à 0 — c'est 100 qui la désactive.

La part d'un utilisateur dépend du nombre d'utilisateurs actifs au moment de la
requête : elle peut donc varier au cours d'une même fenêtre, à la hausse comme à
la baisse. Le tableau de bord affiche, sous chaque jauge d'abonnement, le nombre
d'utilisateurs actifs sur lequel la part a été calculée, ainsi qu'une mention
lorsque le rythme resserre la part, lorsque le forfait déjà consommé par les
autres la plafonne, ou lorsque la fin de fenêtre l'élargit.

## Configurer la résilience

Dans les paramètres avancés du fournisseur :

### Retry (nouvelles tentatives)

Permet de retenter automatiquement les requêtes échouées :

- **Nombre de tentatives** : combien de fois réessayer
- **Délai entre tentatives** : temps d'attente entre chaque essai (ms, s, min)

### Rate limit (limitation de débit)

Protection contre les surextensions :

- **Intervalle minimum** : temps minimal entre deux requêtes
- **Capacité de burst** : nombre max de requêtes simultanées autorisées

## Gestion des modèles

Chaque fournisseur peut exposer un ou plusieurs modèles.

### Accéder aux modèles

1. Depuis la liste des fournisseurs, cliquez sur **Modèles**
   ![Liste des modèles](./screenshots/image4.png)

### Créer un modèle

1. Cliquez sur **Ajouter un modèle**
2. Remplissez les informations :
   ![Formulaire modèle](./screenshots/image5.png)

#### Champs d'identité

| Champ           | Description                                                          |
| --------------- | -------------------------------------------------------------------- |
| **Nom proxy**   | Nom visible par les utilisateurs (format : `{org-slug}/{nom-proxy}`) |
| **Modèle réel** | Nom exact du modèle chez le fournisseur                              |
| **Description** | Description optionnelle                                              |
| **Contexte**    | Taille de la fenêtre de contexte en tokens                           |
| **Sortie**      | Taille max de la réponse en tokens                                   |

#### Champs de capacités

| Capacité         | Description                                 |
| ---------------- | ------------------------------------------- |
| **Outils**       | Le modèle peut utiliser des tools/functions |
| **Vision**       | Le modèle peut analyser des images          |
| **Raisonnement** | Le modèle utilise du chain-of-thought       |
| **Audio**        | Le modèle supporte l'audio                  |
| **Embeddings**   | Le modèle peut produire des embeddings      |

#### Champs de tarification

| Champ                    | Description                                 |
| ------------------------ | ------------------------------------------- |
| **Coût prompt/1M**       | Prix par million de tokens en entrée        |
| **Coût prompt cache/1M** | Prix pour les tokens servis depuis le cache |
| **Coût complétion/1M**   | Prix par million de tokens en sortie        |

#### Champs d'estimation écologique

| Champ                  | Description                                 |
| ---------------------- | ------------------------------------------- |
| **Params actifs (Md)** | Nombre de paramètres du modèle en milliards |
| **Débit min/max**      | Vitesse de génération estimée (tokens/s)    |

> **Astuce** : Cliquez sur **Pré-remplir depuis models.dev** pour importer automatiquement les informations d'un modèle depuis le catalogue.

### Activer ou désactiver un modèle

Lors de l'édition d'un modèle, utilisez le commutateur **Activé** pour rendre le modèle accessible ou indisponible sans le supprimer.

### Supprimer un modèle

1. Ouvrez le modèle en modification
   ![Modifier un modèle](./screenshots/image6.png)
2. Cliquez sur **Supprimer**
   ![Supprimer un modèle](./screenshots/image7.png)

## Supprimer un fournisseur

1. Allez dans la configuration du fournisseur
2. Cliquez sur **Supprimer le fournisseur**

> **Attention** : Cette action supprime également tous les modèles associés.

## Permissions

| Action                            | Permission requise |
| --------------------------------- | ------------------ |
| Consulter les fournisseurs        | `providers:read`   |
| Gérer les fournisseurs et modèles | `providers:write`  |
