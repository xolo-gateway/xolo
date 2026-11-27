# complexity-scorer

> Plugin livré par défaut, famille « Analyse de la requête ». Retour à la [vue d'ensemble des nœuds](../index.md).

**complexity-scorer** évalue la difficulté de la demande courante.

## Ports

| Port | Sens | Type | Requis |
| --- | --- | --- | --- |
| `request` | entrée | request | oui |
| `complexity` | sortie | number | — |
| `level` | sortie | string | — |
| `has_code` | sortie | boolean | — |
| `constraint_count` | sortie | number | — |
| `word_count` | sortie | number | — |
| `context_tokens` | sortie | number | — |
| `estimated_output_tokens` | sortie | number | — |

## Configuration

Aucune configuration. `level` va de `trivial` à `very_complex`.

## En pratique

![Ports du plugin complexity-scorer](../screenshots/inspector-scorer.png)

Le score porte sur le dernier message utilisateur, pas sur tout l'historique. Une conversation longue et banale n'est pas une demande difficile. La taille de l'historique est donnée à part, sur `context_tokens`. Le score monte avec les contraintes explicites (format, longueur, ton, sources), les demandes de raisonnement (prouver, comparer, concevoir), la présence de code et la structure du texte. Une salutation vaut moins de 0,05, une analyse comparative avec tableau et recommandation environ 0,8.
