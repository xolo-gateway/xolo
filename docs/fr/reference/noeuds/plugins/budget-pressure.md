# budget-pressure

> Plugin livré par défaut, famille « Analyse de la requête ». Retour à la [vue d'ensemble des nœuds](../index.md).

**budget-pressure** mesure la part du budget déjà consommée par l'utilisateur.

## Ports

| Port | Sens | Type | Requis |
| --- | --- | --- | --- |
| `request` | entrée | request | oui |
| `budget_pressure` | sortie | number | — |
| `daily_pressure` | sortie | number | — |
| `monthly_pressure` | sortie | number | — |
| `yearly_pressure` | sortie | number | — |
| `has_budget` | sortie | boolean | — |

## Configuration

Aucune configuration.

## En pratique

`budget_pressure` reprend la plus élevée des trois pressions, journalière, mensuelle et annuelle. Sans budget configuré, tout vaut 0. Une pression élevée justifie de basculer vers un modèle moins cher avant que le quota ne bloque la requête.
