# script-processor

> Plugin livré par défaut, famille « Décision ». Retour à la [vue d'ensemble des nœuds](../index.md).

**script-processor** exécute un script [Tengo](https://github.com/d5/tengo) avec des ports libres.

## Ports

Les ports d'entrée et de sortie se déclarent dans la configuration, un nom et un type chacun. Les entrées acceptent aussi `request` et `response`.

## Configuration

Ce plugin ouvre son propre écran de configuration dans le panneau de droite.

| Champ | Rôle | Défaut |
| --- | --- | --- |
| `script` | Script Tengo, un module qui exporte `func(ctx)` |  |
| `inputs` | Ports d'entrée |  |
| `outputs` | Ports de sortie |  |

## En pratique

Le script reçoit `ctx.inputs` et `ctx.request`, renvoie `{ outputs: {...} }` et peut réécrire les messages en renvoyant aussi `messages`. Il sert de recours pour tout ce que les nœuds déclaratifs ne couvrent pas. Un script qu'un `compare` et un `select` pourraient remplacer mérite d'être remplacé.
