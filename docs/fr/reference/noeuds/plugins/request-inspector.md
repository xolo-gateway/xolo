# request-inspector

> Plugin livré par défaut, famille « Analyse de la requête ». Retour à la [vue d'ensemble des nœuds](../index.md).

**request-inspector** relève les faits structurels de la requête.

## Ports

| Port | Sens | Type | Requis |
| --- | --- | --- | --- |
| `request` | entrée | request | oui |
| `has_vision` | sortie | boolean | — |
| `has_reasoning` | sortie | boolean | — |
| `has_tools` | sortie | boolean | — |
| `is_streaming` | sortie | boolean | — |
| `message_count` | sortie | number | — |
| `input_tokens` | sortie | number | — |
| `max_tokens` | sortie | number | — |

## Configuration

Aucune configuration.

## En pratique

`has_vision` est le premier test d'un routage. Une requête avec image doit aller vers un modèle capable de lire les images.
