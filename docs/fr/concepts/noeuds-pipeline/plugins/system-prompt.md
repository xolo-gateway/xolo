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

Quel que soit le mode, la conversation réécrite par le plugin contient **un seul message système, en tête**, même si le client en envoie plusieurs.

- **Remplacement** (`append: false`) : les messages système du client sont supprimés ; seul le prompt configuré est envoyé.
- **Ajout** (`append: true`) : les messages système du client sont regroupés dans l'ordre, séparés par une ligne vide, et le prompt configuré est ajouté à la fin. Il a donc le dernier mot.

Un message système envoyé par le client au milieu de la conversation est remonté en tête. Seul le texte des messages système est conservé, lu comme le proxy le lit : les chaînes, les parties textuelles (`text`, `input_text`, `output_text`), et le champ `text` d'une partie sans type déclaré ou d'un type inconnu. Une partie non textuelle (une image, un fichier, par exemple) est ignorée.

> **Attention** : sur l'endpoint au format Anthropic `/v1/messages`, le champ `system` de premier niveau n'appartient pas à la conversation que le plugin réécrit : il est rajouté devant elle par l'adaptateur. Un client qui le renseigne fait donc encore parvenir deux prompts système au fournisseur. Ce défaut est suivi par le ticket [#114](https://github.com/xolo-gateway/xolo/issues/114).

## En pratique

Reliez sa sortie `request` au nœud suivant, sinon la modification est perdue.
