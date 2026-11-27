# time-restriction

> Plugin livré par défaut, famille « Transformation de la requête ». Retour à la [vue d'ensemble des nœuds](../index.md).

**time-restriction** refuse les requêtes hors des plages horaires hebdomadaires configurées.

## Ports

| Port | Sens | Type | Requis |
| --- | --- | --- | --- |
| `request` | entrée | request | oui |
| `request` | sortie | request | — |

## Configuration

Ce plugin ouvre son propre écran de configuration dans le panneau de droite.

| Champ | Rôle | Défaut |
| --- | --- | --- |
| `timezone` | Fuseau horaire |  |
| `slots` | Créneaux autorisés, par jour de la semaine |  |

## En pratique

La requête refusée reçoit une réponse 403 et le pipeline s'arrête là.
