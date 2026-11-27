# energy-estimator

> Plugin livré par défaut, famille « Analyse de la requête ». Retour à la [vue d'ensemble des nœuds](../index.md).

**energy-estimator** estime l'énergie d'une inférence.

## Ports

| Port | Sens | Type | Requis |
| --- | --- | --- | --- |
| `input_tokens` | entrée | number | oui |
| `output_tokens` | entrée | number | non |
| `model_params_b` | entrée | number | non |
| `energy_kwh` | sortie | number | — |
| `energy_wh` | sortie | number | — |
| `duration_ms` | sortie | number | — |
| `energy_cost` | sortie | number | — |

## Configuration

| Champ | Rôle | Défaut |
| --- | --- | --- |
| `tier` | Infrastructure d'hébergement | `major_cloud` |
| `default_model_params_b` | Taille du modèle par défaut, en milliards de paramètres actifs | 70 |
| `default_output_tokens` | Tokens de sortie par défaut | 512 |
| `reference_kwh` | Énergie de référence en kWh, celle qui vaut un coût de 0,5 | 0,001 |

## En pratique

`energy_cost` normalise l'énergie entre 0 et 1 sur une échelle logarithmique, avec l'énergie de référence à 0,5. La taille du modèle vient du port ou de la configuration. Branchez `input_tokens` du [`request-inspector`](./request-inspector.md) et `estimated_output_tokens` du [`complexity-scorer`](./complexity-scorer.md). La méthode de calcul est décrite dans [Estimation énergétique](../../../decouvrir/estimation-energetique.md).
