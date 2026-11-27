# fuzzy-evaluator

> Plugin livré par défaut, famille « Décision ». Retour à la [vue d'ensemble des nœuds](../index.md).

**fuzzy-evaluator** applique des règles de logique floue à des nombres.

## Ports

Les ports d'entrée et de sortie se déclarent dans la configuration, avec les règles. Par défaut : `complexity`, `budget_pressure` et `energy_cost` en entrée, `power_level` en sortie, tous de type number.

## Configuration

Ce plugin ouvre son propre écran de configuration dans le panneau de droite.
Les règles s'écrivent dans un langage dédié, dans l'écran du plugin.

## En pratique

Le pipeline « auto » l'utilise pour combiner complexité, pression budgétaire, coût et sensibilité énergétiques en un `power_level`.

La logique floue donne des transitions douces là où des seuils créent des sauts. En contrepartie, il faut écrire des règles. Pour deux ou trois entrées, `math` puis `compare` suffisent souvent.
