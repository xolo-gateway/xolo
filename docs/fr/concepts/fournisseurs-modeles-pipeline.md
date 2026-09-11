# Fournisseurs, modèles et pipelines

Xolo s'intercale entre les utilisateurs et les services LLM. Cette page explique comment les briques suivantes s'articulent : **fournisseur**, **modèle**, **modèle virtuel**, **middleware** et **plugin**.

## Fournisseur → modèle

Un **fournisseur** est une connexion vers un service LLM externe (OpenAI, Mistral, OpenRouter, ou un serveur que vous opérez comme Ollama ou vLLM…). Il porte les informations de connexion (URL, clé API), le mode de facturation (pay-as-you-go ou abonnement) et un **niveau d'infrastructure** (Hyperscaler, Major Cloud, Small Provider) utilisé pour l'[estimation énergétique](./estimation-energetique.md).

Un fournisseur expose un ou plusieurs **modèles**. Chaque modèle est décrit par :

- son identité (nom proxy exposé aux utilisateurs, nom réel côté fournisseur, taille de contexte) ;
- ses capacités (outils, vision, raisonnement, audio, embeddings) ;
- sa tarification (coût par million de tokens, en prompt, en cache, en complétion) ;
- ses caractéristiques physiques (paramètres actifs, débit min/max) utilisées pour l'estimation énergétique.

Les utilisateurs consomment un modèle sous la forme `{org-slug}/{nom-proxy}`.

## Modèle virtuel : un pipeline de traitement

Un **modèle virtuel** est un modèle personnalisé, exposé exactement comme un modèle classique, mais qui applique un **pipeline de traitement** aux requêtes/réponses avant/après l'appel au modèle réel. L'utilisateur final ne voit jamais ces traitements : toute la personnalisation reste transparente.

Le pipeline est un graphe de nœuds connectés entre eux par des **ports** typés (`request`, `response`, `string`, `number`, `boolean`). Le moteur de pipeline trie le graphe topologiquement et fait circuler les valeurs d'un nœud à l'autre. Composer plusieurs traitements ne demande aucune ligne de code, juste du câblage dans l'interface.

Les nœuds sont de deux sortes. Les nœuds **intégrés** font partie du serveur : requête entrante, réponse, appel de modèle, modèle avec repli, référence de modèle, valeur fixe, comparaison, sélection, calcul, échantillonnage, contexte de la requête, trace et note. Les **plugins** sont des binaires séparés chargés au démarrage. La page [Nœuds de pipeline](./noeuds-pipeline.md) décrit chacun d'eux, ports et configuration compris.

Plugins livrés avec Xolo :

| Plugin | Capacité | Description |
| --- | --- | --- |
| `system-prompt` | PRE_REQUEST | Injecte un prompt système personnalisé |
| `pseudonymizer` | PRE_REQUEST | Anonymisation des données sensibles |
| `time-restriction` | PRE_REQUEST | Restreint l'accès selon des plages horaires |
| `request-inspector` | PRE_REQUEST | Détecte la structure de la requête : images, raisonnement, outils, taille du contexte |
| `complexity-scorer` | PRE_REQUEST | Évalue la complexité lexicale et structurelle de la requête |
| `text-classifier` | PRE_REQUEST | Classe la requête dans une catégorie thématique par règles lexicales et modèle bayésien embarqué, sans appel LLM |
| `llm-classifier` | PRE_REQUEST | Classe la requête en interrogeant un modèle de l'organisation, selon des catégories décrites dans la configuration |
| `prompt-guard` | PRE_REQUEST, POST_RESPONSE, TOOL_RESULT_INSPECTOR | Risque d'injection de prompt sans appel LLM, pression accumulée sur la conversation, inspection des résultats d'outils et de la réponse, canaris |
| `energy-estimator` | PRE_REQUEST | Estime l'énergie consommée par l'inférence à partir des tokens et de la taille du modèle |
| `budget-pressure` | PRE_REQUEST | Mesure la part du budget de l'utilisateur déjà consommée |
| `fuzzy-evaluator` | PRE_REQUEST | Inférence par logique floue sur des valeurs numériques |
| `script-processor` | PRE_REQUEST | Exécute un script [Tengo](https://github.com/d5/tengo) avec des ports d'entrée/sortie personnalisés |
| `mcp-bridge` | — | Pont vers des serveurs MCP |
| `dummy-model` | RESOLVE_MODEL | Retourne des réponses synthétiques pour les modèles virtuels de test |

Pour créer un modèle virtuel, voir le tutoriel [Modèles virtuels](../administration/organisation/virtual_model/virtual_model.md). Pour développer un plugin sur mesure, voir le guide [Développement de plugins](../administration/installation/plugins.md).

## Middleware : traitement transversal

Un **middleware** applique lui aussi un pipeline de traitement, mais à un niveau différent des modèles virtuels : il s'attache à un ou plusieurs modèles existants (ou à tous les modèles de l'organisation) plutôt que d'en créer un nouveau. Plusieurs middlewares peuvent être chaînés ; ils s'exécutent par **priorité croissante** (le plus petit nombre en premier).

**Exemple.** Trois middlewares enchaînés sur un même modèle :

1. Journalisation (priorité 1)
2. Contrôle horaire (priorité 10)
3. Filtrage de contenu (priorité 20)

Voir le tutoriel [Middlewares](../administration/organisation/middleware/middleware.md) pour la configuration.

## Vue d'ensemble

```
Fournisseur ──▶ Modèle ──▶ [Middlewares, par priorité] ──▶ Utilisateur
                   ▲
Modèle virtuel ────┘  (pipeline de plugins pré/post-requête, résolution de modèle)
```
