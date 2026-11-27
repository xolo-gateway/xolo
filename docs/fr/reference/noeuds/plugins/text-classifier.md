# text-classifier

> Plugin livré par défaut, famille « Analyse de la requête ». Retour à la [vue d'ensemble des nœuds](../index.md).

**text-classifier** range la demande dans une catégorie sans appeler de modèle.

## Ports

| Port | Sens | Type | Requis |
| --- | --- | --- | --- |
| `request` | entrée | request | oui |
| `category` | sortie | string | — |
| `confidence` | sortie | number | — |
| `source` | sortie | string | — |

## Configuration

| Champ | Rôle | Défaut |
| --- | --- | --- |
| `use_rules` | Règles lexicales avant le modèle | `true` |
| `rule_confidence` | Confiance attribuée à une règle | 0,9 |
| `min_confidence` | Marge minimale du modèle | 0,05 |

## En pratique

Catégories possibles : `analysis`, `code`, `conversation`, `creative`, `factual`, `instruction`, `math`, `rewriting`, `summarization`, `translation`, ou `unknown`.

Des règles lexicales tranchent d'abord les cas explicites, « traduis », « résume », un bloc de code. `source` vaut alors `rule`. Le reste passe par un modèle bayésien embarqué, entraîné sur le corpus du dépôt. Sous la marge minimale configurée, la réponse est `unknown` plutôt qu'une supposition. Le corpus reste petit. Comptez sur les règles pour les catégories nettes, et sur [`llm-classifier`](./llm-classifier.md) quand la précision compte.
