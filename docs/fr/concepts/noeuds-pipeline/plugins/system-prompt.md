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

## Comportement

Quel que soit le mode, le modèle reçoit **un seul message système, en tête de la conversation**, même si le client en envoie plusieurs.

- **Remplacement** (`append: false`) : les messages système du client sont supprimés ; seul le prompt configuré est envoyé.
- **Ajout** (`append: true`) : les messages système du client sont regroupés dans l'ordre, séparés par une ligne vide, et le prompt configuré est ajouté à la fin. Il a donc le dernier mot.

Un message système envoyé par le client au milieu de la conversation est remonté en tête. Seul le texte des messages système est conservé : une partie non textuelle (une image, par exemple) est ignorée.

## En pratique

Reliez sa sortie `request` au nœud suivant, sinon la modification est perdue.
