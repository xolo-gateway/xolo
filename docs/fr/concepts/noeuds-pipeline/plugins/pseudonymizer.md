# pseudonymizer

> Plugin livré par défaut, famille « Transformation de la requête ». Retour à la [vue d'ensemble des nœuds](../index.md).

**pseudonymizer** remplace les données personnelles par des pseudonymes avant l'appel au modèle, puis rétablit les valeurs d'origine dans la réponse.

## Ports

| Port | Sens | Type | Requis |
| --- | --- | --- | --- |
| `request` | entrée | request | oui |
| `request` | sortie | request | — |

## Configuration

Ce plugin ouvre son propre écran de configuration dans le panneau de droite.

| Champ | Rôle | Défaut |
| --- | --- | --- |
| `strategy` | Stratégie d'anonymisation | `tag` |
| `language` | Langue | `auto` |
| `fallback_language` | Langue de repli | `fr` |
| `min_confidence` | Confiance minimale | 0,3 |
| `skip_types` | Types d'entités à ignorer |  |
| `blocklist` | Liste de blocage |  |
| `builtin_regex_patterns` | Patterns regex intégrés | `true` |
| `builtin_secret_patterns` | Patterns secrets intégrés | `true` |
| `process_attachments` | Traiter les pièces jointes documentaires | `true` |
| `unsupported_attachments` | Pièces jointes non traitables | `block` |
| `max_attachment_bytes` | Taille maximale par pièce jointe, en octets | 10 485 760 |
| `inject_instruction` | Instruction de préservation des jetons | `true` |
| `verification` | Observer les fuites sans bloquer | `true` |
| `verification_strict` | Bloquer les requêtes sur fuite | `false` |
| `cache_dir` | Répertoire de cache des modèles |  |
| `manifest_url` | URL du manifest des modèles |  |
| `offline` | Mode hors-ligne | `false` |

## En pratique

Il agit dans les deux sens, ce qui oblige Xolo à attendre la fin de la réponse avant de la renvoyer quand il a effectivement remplacé quelque chose. Il émet un événement quand il détecte une donnée sensible.

Devant un client agentique, il traite aussi les blocs d'outils du format Messages d'Anthropic : le contenu d'un `tool_result` et les arguments d'un `tool_use`. Ce qui apparie un appel à son résultat — `id`, `tool_use_id`, `name` — et les noms d'arguments sont laissés intacts ; un contenu qu'il ne sait pas lire, l'image d'une capture d'écran par exemple, est remplacé par une note et non retiré, sous peine de laisser l'appel correspondant orphelin.

Le **prompt système de haut niveau** de la route Messages n'est pas pseudonymisé. Il voyage dans le champ `system` de la requête, en dehors du tableau `messages` sur lequel le pipeline travaille, et il est reporté tel quel vers le fournisseur. Des données personnelles placées là par le client partent donc en clair : à traiter comme un texte de configuration, pas comme un endroit où écrire un nom ou une adresse.

Les appels d'outils **au format OpenAI** sont couverts eux aussi : leurs arguments vivent dans `tool_calls[].function.arguments`, en dehors du contenu du message, sous la forme d'un document JSON encodé dans une chaîne ; il est décodé, ses feuilles textuelles sont remplacées, puis il est ré-encodé. L'`id` de l'appel, son `type` et le nom de la fonction sont préservés, comme pour un `tool_use`.

Le champ `function_call`, l'ancêtre déprécié de `tool_calls` que quelques clients anciens émettent encore, n'est pas traité : ses arguments partent tels quels.

Deux limites à connaître. Comme une conversation agentique fait presque toujours remplacer quelque chose, ces échanges perdent le streaming — la réponse n'est renvoyée qu'une fois complète. Et en mode strict avec `verification_on_leak` à `allow`, une fuite détectée fait passer la requête **entière** en passe-plat : l'événement `sensitive-data.leak` le signale, mais du trafic d'outils transporte un fichier là où un message ordinaire transporte une phrase. `block` refuse plutôt que de transmettre.

L'écran du plugin expose d'autres réglages fins (fusion des entités adjacentes, complétion des noms, reclassification des prénoms, SIREN contextuel, limites par entité). Les valeurs par défaut conviennent à un déploiement français.
