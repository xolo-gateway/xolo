# system-prompt

> Plugin livré par défaut, famille « Transformation de la requête ». Retour à la [vue d'ensemble des nœuds](../index.md).

**system-prompt** ajoute un prompt système, ou remplace celui de la requête.

## Ports

| Port | Sens | Type | Requis |
| --- | --- | --- | --- |
| `request` | entrée | request | non |
| `request` | sortie | request | — |

## Configuration

Ce plugin ouvre son propre écran de configuration dans le panneau de droite.

| Champ | Rôle | Défaut |
| --- | --- | --- |
| `system_prompt` | Le prompt système |  |
| `append` | Ajouter au prompt existant plutôt que le remplacer | `false` |

## En pratique

Reliez sa sortie `request` au nœud suivant, sinon la modification est perdue.
