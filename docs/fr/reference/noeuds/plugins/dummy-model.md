# dummy-model

> Plugin livré par défaut, famille « Outils et test ». Retour à la [vue d'ensemble des nœuds](../index.md).

**dummy-model** remplace le modèle par une réponse écrite à l'avance.

## Ports

| Port | Sens | Type | Requis |
| --- | --- | --- | --- |
| `request` | entrée | request | oui |
| `response` | sortie | response | — |

## Configuration

Ce plugin ouvre son propre écran de configuration dans le panneau de droite.

| Champ | Rôle | Défaut |
| --- | --- | --- |
| `response_template` | Texte Markdown renvoyé à la place du modèle, avec les variables `{{.User}}` et `{{.LastMessage}}` |  |

## En pratique

Il permet de tester un pipeline sans dépenser un token, et d'exécuter des tests de bout en bout reproductibles. Placé seul dans un modèle virtuel, il en fait aussi une réponse fixe qu'un `select` peut choisir, par exemple pour refuser une demande hors périmètre.
