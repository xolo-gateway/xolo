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
   | **Type**                    | Type de connexion : `openai`, `openai-responses`, `anthropic`, `gemini`, `mistral`, `openrouter`, `typesafe` |
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

## Anthropic : utilisez le type `anthropic`

Le type `anthropic` parle l'API Messages native d'Anthropic (`https://api.anthropic.com`), via le SDK officiel. C'est le type à choisir pour un compte Anthropic direct : il transmet les points de cache (`cache_control`) posés par les clients, rejoue les blocs de réflexion signés d'un tour à l'autre et remonte séparément les tokens lus et écrits dans le cache.

| Champ           | Anthropic                                                  |
| --------------- | ---------------------------------------------------------- |
| **Type**        | `anthropic`                                                |
| **URL de base** | `https://api.anthropic.com` (un suffixe `/v1` est toléré)  |
| **Clé API**     | votre clé `sk-ant-…`                                       |

Un fournisseur Anthropic déclaré avec le type `openai` sur l'endpoint compatible OpenAI continue de fonctionner, mais ce chemin ne transmet pas `cache_control` : les préfixes de prompt ne sont jamais mis en cache et sont facturés au tarif plein à chaque appel. Aucune migration automatique n'est faite ; modifiez le type du fournisseur à la main.

Pensez à renseigner le **coût du prompt en cache** sur les modèles concernés (chez Anthropic, environ 10 % du tarif d'entrée). Laissé vide, ce champ vaut le tarif plein : le coût affiché surestime alors la facture dès que le cache est actif. Les écritures de cache, facturées plus cher que le tarif d'entrée par Anthropic, sont comptées au tarif plein.

Le type `anthropic` envoie `max_tokens` à chaque requête, l'API l'exigeant : la valeur est celle du client, sinon la **fenêtre de sortie** du modèle telle que déclarée dans Xolo, plafonnée à 16 384 tokens. Ce plafond existe parce que, sur cette API, `max_tokens` dimensionne aussi le budget de réflexion (une fraction de `max_tokens` quand le client demande un niveau d'effort) et compte dans la fenêtre de contexte avec l'entrée. Un client qui veut davantage fixe lui-même `max_tokens`, et un client dont le prompt approche la fenêtre de contexte doit le faire aussi : sur cette API, entrée et `max_tokens` doivent tenir ensemble dans la fenêtre. Fenêtre de sortie non renseignée, le provider retombe sur 4096 tokens.

Vérifiez aussi l'**extra_body** des modèles migrés : ses entrées partent en champs de premier niveau de la requête, et l'API Messages n'accepte que les siens. Une clé propre au format OpenAI comme `reasoning_effort` fait échouer toutes les requêtes du modèle.

### Relais des requêtes Messages en streaming

Une requête `POST /api/v1/messages` avec `"stream": true` vers un modèle de type `anthropic` est **relayée** telle quelle au lieu d'être traduite : Xolo ne réécrit que le modèle, la clé et les champs **extra_body**, transmet les en-têtes `anthropic-*` du client (`anthropic-beta` compris) et renvoie les événements de l'amont sans les reconstruire. Les champs que Xolo ne connaît pas passent donc aussi, dans les deux sens. Un client peut ainsi activer toute fonctionnalité de l'API sur la clé du fournisseur, outils serveur compris (recherche web, serveurs MCP distants). Xolo ne comptabilise que les tokens : les frais facturés à la requête par Anthropic, comme la recherche web, n'apparaissent ni dans l'usage ni dans les quotas. C'est ce que demande Claude Code pour la vérification côté serveur du mode auto : sans le champ `safeguards` à l'aller et `safeguard_results` au retour, il refait lui-même cette vérification et la facture en tokens.

Authentification, quotas, suivi de l'usage et pseudonymisation s'appliquent comme sur une requête traduite. Les instructions système qu'un nœud ajoute en tête de conversation (pseudonymiseur, `system-prompt`) rejoignent le champ `system`, après celles du client. Le jeton Xolo du client n'est jamais transmis à l'amont. Un rejet de l'amont (statut, en-têtes comme `retry-after`, corps) revient au client sans modification.

La requête repasse par la traduction dans ces cas :

- requête sans `"stream": true` ;
- modèle virtuel dont le nœud terminal combine plusieurs modèles, comme un repli entre modèles ;
- modèle avec une **limite de tokens** : le limiteur de tokens ne sait pas compter une réponse relayée.

## Modèles de décision : TypeSafe

Un modèle de décision ne génère pas de texte. Il évalue un **état** (un texte, un objet JSON) au regard de **questions typées** et renvoie des probabilités calibrées :

- `noul` : une question fermée, dont la réponse est la probabilité du oui ;
- `choice` : un choix parmi les options que la question définit, avec la probabilité de chacune ;
- `score` : une position sur une échelle ordonnée de 2 à 10 niveaux.

Les modèles Jev de TypeSafe sont de ce genre. Déclarez-les avec le type `typesafe` et l'URL `https://api.typesafe.ai/v1`, ou par OpenRouter (type `openrouter`, modèle réel `~typesafe/jev-latest`), puis créez le modèle avec la nature **Décision**.

Les clients l'appellent sur `POST /api/v1/decisions`, au format de TypeSafe et du endpoint de décisions d'OpenRouter. Un client de l'un ou l'autre garde son format et pointe son URL d'endpoint vers cette route : le corps est le même, le chemin non.

```json
{
  "model": "{org-slug}/jev",
  "state": "Mes virements échouent depuis trois jours.",
  "questions": {
    "is_urgent": { "type": "noul", "instructions": "Ce ticket est-il urgent ?" },
    "team": {
      "type": "choice",
      "instructions": "Quelle équipe doit le traiter ?",
      "criteria": { "paiements": null, "recherche": "Recherche et catalogue" }
    }
  }
}
```

La réponse porte une réponse par question, sous le même identifiant, et l'usage en tokens (`input_tokens`, `output_tokens`). Xolo facture une décision comme une complétion : les tokens d'entrée au coût du prompt, ceux de sortie au coût de complétion, ou le coût que rapporte OpenRouter. Les quotas, l'abonnement, la limitation de débit et les nouvelles tentatives du fournisseur s'y appliquent aussi. L'**extra_body** et la limite de tokens d'un modèle ne visent que la complétion : sur un modèle de décision, ils sont ignorés.

Ni modèle virtuel ni middleware ne s'appliquent à une décision (voir [Nature du modèle](#nature-du-modele)).

## Tester la connexion

Après avoir créé un fournisseur, utilisez le bouton **Tester la connexion** pour vérifier que Xolo peut communiquer avec le fournisseur.

## Fournisseurs en abonnement : plan et répartition

Un fournisseur en mode **Abonnement** ne facture pas à l'usage : il vend un volume
global (des tokens, ou une valeur exprimée dans la devise du fournisseur) pour
toute l'organisation, sur une fenêtre qui se réinitialise. Le plan décrit ces
limites sous forme de **contraintes**.

La valeur d'une requête est son coût au tarif du modèle déclaré dans Xolo, ou le
coût que rapporte le fournisseur. Un fournisseur en abonnement rapporte souvent
un coût nul pour une requête couverte par le forfait : Xolo ne retient pas ce
zéro, qui laisserait les budgets valeur immobiles, et calcule la valeur au tarif
du modèle.

### Contraintes

| Type                     | Champs                                           | Rôle                                                              |
| ------------------------ | ------------------------------------------------ | ----------------------------------------------------------------- |
| **Fenêtre glissante**    | Durée, budget tokens, budget valeur              | Volume consommable sur les N dernières heures                     |
| **Fenêtre fixe**         | Idem + « Reset dans »                            | Idem, mais alignée sur l'heure de réinitialisation du fournisseur |
| **Concurrence**          | Nombre max de requêtes simultanées               | Requêtes en vol autorisées en même temps                          |

Le champ « Reset dans » se recopie depuis l'écran du fournisseur (par exemple
`4h29m`) : Xolo le convertit en un ancrage absolu, de sorte que ses fenêtres
coïncident avec les réinitialisations réelles du forfait.

### Décompte par modèle

Une fenêtre se décompte de trois façons, au choix dans son champ **Décompte** :

| Décompte                       | Ce que compte la fenêtre                                                         |
| ------------------------------ | -------------------------------------------------------------------------------- |
| **Fournisseur entier**         | Tous les modèles consomment les mêmes budgets, chacun pour ce qu'il coûte        |
| **Par modèle**                 | Chaque modèle a sa propre fenêtre et ses propres budgets                         |
| **Commun, pondéré par modèle** | Tous les modèles consomment les mêmes budgets, chacun à son propre rythme        |

Le champ **Budgets propres à certains modèles** donne un budget à part aux
modèles qu'il liste, un par ligne, sous leur nom amont (le « modèle réel ») :

```
kimi-k3 = 3
deepseek-v4.1-flash = 16, 50000000 tokens
longcat-2.5-preview-free = illimité
```

Une valeur s'exprime dans la devise du fournisseur, un budget tokens porte le
suffixe `tokens`, et `illimité` lève toute limite pour ce modèle. Un modèle listé
remplace les deux budgets de la contrainte : `kimi-k3 = 3` ne garde pas le
budget tokens de la contrainte pour `kimi-k3`. Un modèle absent de la liste
prend les budgets de la contrainte. Ce champ n'a de sens qu'avec un décompte
par modèle ou commun.

#### Par modèle

Pour les forfaits qui limitent chaque modèle séparément : chaque modèle amont a
sa propre fenêtre et ses propres budgets, et épuiser un modèle ne bloque pas les
autres. La répartition entre utilisateurs s'applique modèle par modèle : un
utilisateur actif sur un modèle n'entame pas la part des autres modèles.

Quand le fournisseur répond `429`, seul le modèle demandé est mis en pause.

#### Commun, pondéré par modèle

Pour les forfaits qui publient une limite par modèle mais ne décomptent qu'une
seule fenêtre. C'est le cas d'OpenCode Go : sa page de limites indique, pour
chaque modèle, l'usage mensuel que permet le forfait s'il n'est consommé
qu'avec ce modèle (160 $ pour DeepSeek V4.1 Flash, 60 $ pour MiMo-V2.6-Pro en
Go Plus), mais son tableau de bord n'affiche qu'une jauge par fenêtre, tous
modèles confondus.

Le budget de la contrainte est celui de la fenêtre, et le budget propre d'un
modèle fixe son rythme : un modèle consomme `coût × budget de la contrainte /
budget du modèle`. Avec une fenêtre de 60 $ et DeepSeek à 160 $, 16 $ de
DeepSeek remplissent 6 $ de la fenêtre. Un modèle déclaré `illimité` ne la
remplit pas. Un budget propre doit être positif, et d'un type que la contrainte
possède : un budget tokens n'a pas de rythme à fixer sur une contrainte sans
budget tokens.

La fenêtre, sa jauge et la répartition entre utilisateurs sont communes à tous
les modèles : un `429` du fournisseur met la fenêtre en pause pour tous, sauf
si le plan décompte aussi une fenêtre par modèle (voir
[Mise en pause et jauges](#mise-en-pause-et-jauges)). Pour
OpenCode Go, déclarez les trois fenêtres en décompte commun, avec les limites de
la page du forfait :

| Fenêtre | Budget de la contrainte | Budget d'un modèle         |
| ------- | ----------------------- | -------------------------- |
| 5h      | 20 % de l'usage mensuel | 20 % de sa limite mensuelle |
| Semaine | 50 % de l'usage mensuel | 50 % de sa limite mensuelle |
| Mois    | usage mensuel           | sa limite mensuelle        |

La semaine d'OpenCode Go se réinitialise le lundi à 00:00 UTC : recopiez le
« Reset dans » de son tableau de bord pour en faire une fenêtre fixe.

#### Modèles publiés sous plusieurs noms

Deux modèles Xolo qui pointent vers le même modèle amont, par exemple deux noms
publiés avec des réglages différents, sont décomptés ensemble, comme le fait le
fournisseur, et prennent le même rythme. Xolo garde la liste des modèles 30
secondes : un modèle créé ou modifié compte avec ses homonymes après ce délai.
Si la liste ne peut pas être lue, une fenêtre par modèle décompte le seul
modèle demandé, une fenêtre commune compte chaque modèle au rythme le plus
élevé de la contrainte, ce qui la ferme plus tôt, et le compteur
`xolo_plan_model_grouping_failures_total` s'incrémente. L'usage d'un modèle
supprimé depuis compte lui aussi à ce rythme.

#### Mise en pause et jauges

Un plan peut mêler les portées : une fenêtre décomptée par modèle et une
autre sur l'ensemble du fournisseur s'appliquent toutes les deux. Quand un plan
décompte par modèle et que le fournisseur répond `429`, seul le modèle demandé
est mis en pause, pour la durée indiquée par l'en-tête `Retry-After`, ou cinq
minutes à défaut : ses fenêtres sur l'ensemble du fournisseur et ses fenêtres
communes restent ouvertes, le `429` ne pouvant pas leur être attribué.
Un `429` dont l'erreur est de type `server_error`, par exemple un fournisseur
saturé qui demande de réessayer dans quelques secondes, ne dit rien du forfait :
il est transmis au client sans mise en pause.

Sur la page d'usage et le tableau de bord, une contrainte par modèle affiche une
jauge par modèle consommé dans la fenêtre en cours ; une contrainte commune
affiche une seule jauge, sa consommation pondérée.

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

Un **seuil de partage** peut enfin suspendre ces parts tant que le forfait a de
la place pour tous : sous ce seuil de remplissage, chacun consomme librement,
même au-delà de sa part, jusqu'à ce que le forfait l'atteigne ; au-delà, les
parts s'appliquent comme décrit plus haut. Avec un seuil de 70 %, un forfait
rempli à 40 % reste ouvert à tous ; un utilisateur qui a déjà dépassé sa part
peut consommer jusqu'à ce que le forfait atteigne 70 %, puis il est arrêté et
la marge restante revient aux autres. Laissé vide, ou à 0, les parts
s'appliquent dès la première requête.

Ces cinq réglages sont facultatifs et se trouvent sous « Répartition entre
utilisateurs » dans le formulaire de la contrainte. Laissés vides, ils prennent
leurs valeurs par défaut.

| Réglage                            | Défaut | Effet                                                                                        |
| ---------------------------------- | ------ | -------------------------------------------------------------------------------------------- |
| **Réserve garantie**               | 30 %   | Part du budget réservée à parts égales entre tous les membres                                 |
| **Tolérance de rythme**            | 15 %   | Avance de consommation tolérée avant que les parts ne se resserrent                           |
| **Ouverture de fin de fenêtre**    | 90 %   | Fraction de la fenêtre au-delà de laquelle le reliquat est réparti entre les actifs ; 100 désactive |
| **Avance maximale de l'ouverture** | 5 % de la fenêtre, entre 30 min et 4 h | Borne absolue de cette ouverture : 30 min avant le reset d'une fenêtre de 5 h, 4 h avant celui d'une fenêtre hebdomadaire |
| **Seuil de partage**               | 0 %    | Remplissage du forfait sous lequel les parts ne s'appliquent pas ; 0 les applique dès la première requête |

Une valeur illisible ou hors bornes est refusée à l'enregistrement, avec un
message nommant la contrainte concernée : un réglage affiché doit être celui que
le moteur applique. L'ouverture de fin de fenêtre doit être strictement
supérieure à 0. C'est 100 qui la désactive.

La part d'un utilisateur dépend du nombre d'utilisateurs actifs au moment de la
requête : elle peut donc varier au cours d'une même fenêtre, à la hausse comme à
la baisse. Le tableau de bord affiche, sous chaque jauge d'abonnement, le nombre
d'utilisateurs actifs sur lequel la part a été calculée, ainsi qu'une mention
lorsque le rythme resserre la part, lorsque le forfait déjà consommé par les
autres la plafonne, lorsque la fin de fenêtre l'élargit, ou lorsque le forfait,
sous son seuil de partage, est en accès libre.

### Plusieurs clés pour un même abonnement

Un forfait d'équipe, comme OpenCode Go Teams, donne à chaque membre sa propre
clé et ses propres limites. Pour en faire profiter plus d'utilisateurs Xolo qu'il
n'y a de membres, ajoutez les clés des membres sous **Clés API supplémentaires**,
dans le formulaire d'un fournisseur en abonnement. La clé du fournisseur reste la
première du lot.

Le plan décrit les limites d'**une** clé. Xolo l'applique à chacune, avec
ses éventuels ajustements (voir plus bas) :

- **les parts des utilisateurs** se calculent sur l'ensemble des clés. Avec
  trois clés et une fenêtre de 30 $, la répartition entre utilisateurs porte
  sur 90 $ ; c'est ce que montrent les jauges ;
- **chaque requête** passe par une seule clé, qui doit avoir de la marge sur
  toutes ses fenêtres. Un utilisateur reste sur la clé de sa requête précédente,
  où vit le cache de ses conversations, tant qu'elle a de la marge. Sinon, il
  passe sur la clé la moins remplie ; sur un forfait encore vide, les nouveaux
  arrivants se répartissent entre les clés ;
- **un refus d'OpenCode** (429 de limite atteinte) met la clé en pause et la
  requête repart aussitôt, sans erreur, sur une autre clé qui a de la marge.
  Une saturation momentanée du fournisseur (`server_error`) ne change pas de
  clé.

Les clés restent invisibles pour les utilisateurs : ni leur tableau de bord, ni
les messages de refus ne les mentionnent. La page d'usage de l'organisation
détaille la consommation clé par clé, sous la carte du fournisseur, pour les
personnes autorisées à consulter les fournisseurs.

#### Ajuster le plan pour une clé

Une clé peut s'écarter du plan, fenêtre par fenêtre, sous **Ajuster le plan
pour cette clé** : un membre dont l'abonnement a d'autres budgets, ou qui se
renouvelle un autre jour. Chaque champ laissé vide garde la valeur du plan.

- **Budget** (en valeur ou en tokens) : remplace celui de la fenêtre pour
  cette clé ;
- **Budgets par modèle** : remplacent, modèle par modèle, ceux de la fenêtre,
  les modèles non cités gardant ceux du plan ;
- **Reset dans** : cale la fenêtre de la clé sur son propre renouvellement,
  par exemple le 29 quand le plan se renouvelle le 19.

Chaque clé est alors mesurée sur ses propres budgets et dans sa propre
fenêtre : le choix de clé ne retient que celles qui ont de la marge selon
leur propre plan. Les parts des utilisateurs portent sur la somme des budgets
des clés, l'usage de chaque clé compté dans sa fenêtre. Le rythme, la fin de
fenêtre et le « réinitialisation dans » des jauges personnelles suivent la
fenêtre du plan. Sur une fenêtre commune pondérée, les budgets par modèle
s'additionnent aussi : une clé qui ne cite pas un modèle y compte pour le
budget de sa fenêtre, et un modèle illimité sur une clé n'ajoute rien au
budget commun.

Désactivez par ailleurs le dépassement payant du forfait (« Extra Usage » chez
OpenCode) : une clé qui continue de répondre au-delà de ses limites n'est
jamais vue saturée, et le dépassement est facturé.

Les requêtes passées par la clé du fournisseur avant l'ajout d'autres clés
restent comptées sur celle-ci. Retirer une clé efface ses jauges, pas son
historique d'usage.

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

## En-têtes de requête

Certaines passerelles exigent des en-têtes que les SDK n'envoient pas. La section **En-têtes de requête** du formulaire du fournisseur, à la création comme à la modification, les configure :

- **En-tête de session** : nom de l'en-tête qui porte l'identifiant de la conversation. Xolo y place l'identifiant envoyé par le client : `X-Session-Id`, ou l'en-tête de Claude Code, de Codex ou d'OpenCode. À défaut, il calcule une empreinte de l'appelant (utilisateur, organisation, jeton) et du premier message de la conversation : elle reste la même d'un tour à l'autre et change d'une conversation à l'autre. Vide, aucun en-tête de session n'est envoyé.
- **En-têtes fixes** : ajoutés à chaque requête, ils remplacent ceux du client HTTP, `User-Agent` compris. Ce ne sont pas des secrets : seule la clé API est chiffrée. Une valeur vide est refusée, comme les en-têtes d'authentification (`Authorization`, `X-Api-Key`, `X-Goog-Api-Key`, `Cookie`), de transport (`Host`, `Content-Type`, `Content-Length`…) et de transfert (`Forwarded`, `X-Forwarded-For`…).

Ces en-têtes valent pour tous les modèles du fournisseur, quel que soit leur type d'API.

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
| **Nature**      | Ce que le modèle sert : complétion, embeddings ou décision           |
| **Contexte**    | Taille de la fenêtre de contexte en tokens                           |
| **Sortie**      | Taille max de la réponse en tokens                                   |

#### Nature du modèle

Un modèle ne répond qu'aux routes de sa nature. Appelé sur une autre route, il répond `400` en indiquant la bonne.

| Nature         | Routes                                    | Types d'API qui la servent                                     |
| -------------- | ----------------------------------------- | -------------------------------------------------------------- |
| **Complétion** | `/chat/completions`, `/messages`          | tous sauf `typesafe`                                           |
| **Embeddings** | `/embeddings`                             | `openai`, `openai-responses`, `mistral`, `openrouter`          |
| **Décision**   | `/decisions`                              | `typesafe`, `openrouter`                                       |

Le formulaire refuse une nature que le type d'API du modèle ne sert pas. Un nouveau modèle est par défaut un modèle de complétion, sauf sur `typesafe`, qui ne sert que la décision.

Les modèles virtuels et les middlewares ne servent pour l'instant que la complétion. Un middleware « appliqué à tous les modèles » ne couvre que les modèles de complétion. Une requête d'embeddings ou de décision n'en traverse donc aucun, et son contenu n'est ni pseudonymisé ni filtré. Les sélecteurs de modèles des pipelines ne proposent que des modèles de complétion, et les plugins ne reçoivent que ceux-là dans leur liste de modèles. Un pipeline qui désigne malgré tout un modèle d'une autre nature répond `400` (code `model_kind_mismatch`) en nommant ce modèle.

Jusqu'à cette version, les embeddings étaient une capacité cochée sur un modèle de complétion. La mise à jour donne la nature embeddings à chaque modèle qui l'avait, si son type d'API sert les embeddings, puis retire la capacité. Sur un autre type d'API (`anthropic`, `gemini`), la capacité n'avait aucun effet : le modèle reste un modèle de complétion. Un modèle converti ne répond plus à `/chat/completions`, ni aux pipelines et plugins qui l'appelaient. S'il servait aussi la complétion, déclarez-le une seconde fois, sous un autre nom proxy, avec la nature complétion, et faites pointer ces pipelines vers ce nom. Les nœuds des pipelines existants ne sont pas migrés : un nœud `model_fallback` dont le premier candidat est devenu un modèle d'embeddings répond `400` sans essayer les suivants. Les modèles d'une organisation en attente de purge ne sont pas convertis : ils restent des modèles de complétion.

#### Champs de capacités

| Capacité         | Description                                 |
| ---------------- | ------------------------------------------- |
| **Outils**       | Le modèle peut utiliser des tools/functions |
| **Vision**       | Le modèle peut analyser des images          |
| **Raisonnement** | Le modèle utilise du chain-of-thought       |
| **Audio**        | Le modèle supporte l'audio                  |

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

#### Accès à l'API

| Champ           | Description                                                                         |
| --------------- | ----------------------------------------------------------------------------------- |
| **Type d'API**  | Type de connexion du modèle. Par défaut, celui du fournisseur                       |
| **URL de base** | URL de l'endpoint API du modèle. Vide, Xolo utilise l'URL de base du fournisseur    |

L'URL de base suit les mêmes règles que celle du fournisseur : URL `http` ou `https` absolue, sans identifiants, paramètres (`?…`) ni fragment (`#…`).

Ces deux champs servent aux fournisseurs qui exposent leurs modèles par plusieurs API. OpenCode Go en est un exemple : la plupart de ses modèles passent par l'API OpenAI, mais Qwen, MiniMax et Claude passent par l'API Messages d'Anthropic, sur une autre URL. Déclarez le fournisseur en `openai` avec l'URL `https://opencode.ai/inference/go/v1`. Elle sert aussi la liste des modèles (`/models`) dont l'import a besoin, contrairement à `https://opencode.ai/inference/go/openai/v1`. Réglez ensuite ces modèles ainsi :

| Champ           | Valeur                                         |
| --------------- | ---------------------------------------------- |
| **Type d'API**  | `anthropic`                                    |
| **URL de base** | `https://opencode.ai/inference/go/anthropic/v1` |

Le reste du fournisseur s'applique au modèle sans changement : clé API, devise, plan d'abonnement, retry et rate limit. Ce que la page dit du type `anthropic` (`max_tokens`, `extra_body`, relais en streaming) vaut pour un tel modèle comme pour un fournisseur de ce type.

Les modèles qu'OpenCode Go ne sert que par l'API Responses d'OpenAI (GPT, Grok) prennent le type `openai-responses`, sur l'URL du fournisseur. Ceux qu'il sert par l'API de Google (Gemini) prennent le type `gemini` :

| Champ           | Valeur                                          |
| --------------- | ----------------------------------------------- |
| **Type d'API**  | `gemini`                                        |
| **URL de base** | `https://opencode.ai/inference/go/google/v1beta` |

OpenCode Go refuse toute requête sans en-tête `x-opencode-session` et demande aux clients de s'identifier par leur `User-Agent`. Dans la section **En-têtes de requête** du fournisseur, renseignez `x-opencode-session` comme **En-tête de session** et ajoutez un en-tête fixe `User-Agent`, par exemple `xolo/1.0`. Les modèles marqués `contributor`, comme Gemini, exigent en plus d'accepter l'entraînement sur les requêtes dans les réglages de confidentialité du compte OpenCode.

> **Astuce** : Cliquez sur **Pré-remplir depuis models.dev** pour importer automatiquement les informations d'un modèle depuis le catalogue.

### Importer les modèles du fournisseur

Plutôt que de déclarer les modèles un par un, cliquez sur **Importer depuis le fournisseur** sur la page des modèles. Xolo interroge la liste des modèles du fournisseur avec sa clé API : `GET <URL de base>/models` pour les types `openai`, `openai-responses`, `gemini`, `mistral` et `openrouter`, `GET /v1/models` pour le type `anthropic`. La page affiche chaque modèle avec une case à cocher. Un modèle que le fournisseur sert déjà sous un autre nom proxy est grisé.

Le champ **Préfixe du nom proxy** est facultatif et ne peut pas contenir de `/`. Chaque modèle importé prend le nom `<préfixe><identifiant>` : avec le préfixe `go-`, le modèle `glm-5.3` s'appelle `{org-slug}/go-glm-5.3`. Un préfixe évite les conflits avec les modèles d'autres fournisseurs. La colonne **Nom proxy** suit le préfixe pendant la saisie et signale un nom déjà pris dans l'organisation, par un modèle ou par un modèle virtuel, ou lu comme le nom d'une équipe.

Les modèles importés :

- sont créés désactivés : saisissez leurs coûts avant de les activer, un modèle sans coût n'étant ni valorisé ni limité par les quotas ;
- ont un coût à zéro, la liste du fournisseur ne donnant pas de prix ;
- reprennent la fenêtre de contexte et la sortie maximale quand le fournisseur les indique (Anthropic, OpenRouter) ;
- utilisent le type d'API et l'URL du fournisseur. Un modèle servi par une autre API, comme Qwen chez OpenCode Go, se règle ensuite dans son formulaire (voir [Accès à l'API](#acces-a-lapi)).

Xolo relit la liste du fournisseur au moment de l'import. Un modèle qu'il ne liste plus, ou dont le nom proxy est déjà pris dans l'organisation (modèles désactivés et modèles virtuels compris), est laissé de côté, et la page indique pourquoi, comme pour un modèle que l'enregistrement n'a pas pu créer.

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
