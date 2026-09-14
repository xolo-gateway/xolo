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

Il agit dans les deux sens, ce qui oblige Xolo à attendre la fin de la réponse avant de la renvoyer quand il a effectivement remplacé quelque chose. Il émet un événement quand il détecte une donnée sensible. Au retour, les jetons sont restitués dans le texte de la réponse **et dans les arguments des appels d'outils qu'elle contient** : le modèle dérive ses nouveaux appels de l'historique pseudonymisé qu'il vient de lire, et un jeton laissé dans un `Read(path=…)` ferait exécuter l'appel sur un chemin qui n'existe pas.

Devant un client agentique, il traite aussi les blocs d'outils du format Messages d'Anthropic, mais pas tous de la même façon selon ce que chaque forme autorise. Le contenu d'un `tool_result` et les arguments d'un `tool_use` sont réécrits, tout comme leurs équivalents côté serveur et MCP `server_tool_use`, `mcp_tool_use` (arguments réécrits) et `mcp_tool_result`, `bash_code_execution_tool_result` (contenu réécrit — pour ce dernier, seuls `stdout`/`stderr` le sont, `return_code` et le reste de l'objet restent intacts). Ce qui apparie un appel à son résultat — `id`, `tool_use_id`, `name` — et les noms d'arguments sont laissés intacts ; un contenu qu'il ne sait pas lire, l'image d'une capture d'écran par exemple, est remplacé par une note et non retiré, sous peine de laisser l'appel correspondant orphelin.

Plusieurs formes traversent sans réécriture, chacune pour une raison différente — mais jamais en silence : ce qui part sans être réécrit est scanné en lecture seule (`Detect`, jamais `Anonymize` : pas de session, rien de réécrit) et vient alimenter l'événement `sensitive-data.detected` sous `leak_entities`/`leak_types`, tenus à part de `entities`/`types` qui ne comptent, eux, que ce qui a été effectivement pseudonymisé. `entities` et `leak_entities` dénombrent tous deux des valeurs distinctes : une même adresse citée cinq fois dans un raisonnement compte pour une fuite, pas cinq. Deux réserves sur ce scan : les champs qui ne portent jamais de texte libre — `encrypted_content`, `data`, `signature`, les identifiants — ne sont pas parcourus, car les relire à chaque tour d'une conversation agentique coûte cher pour ne rien trouver ; et une entrée de `content` qui n'est pas un objet JSON (une chaîne nue) est transmise sans être scannée.

Un bloc `thinking` ou `redacted_thinking` (raisonnement étendu) n'est ni retiré ni pseudonymisé : le fournisseur le signe et vérifie la signature au tour suivant, et réécrire le texte du raisonnement invaliderait cette signature aussi sûrement que toucher au champ `signature` lui-même — `redacted_thinking` ne porte de toute façon aucun texte lisible, seulement un blob chiffré opaque. `web_search_tool_result` traverse pareillement intact : la documentation exige que les blocs de contenu de l'assistant reviennent inchangés **dans leur ensemble** pour ce tour, `encrypted_content` inclus, pas seulement ce champ précis — donc même `title`, qui est bien du texte libre, n'est pas isolé pour être réécrit. Pour la même raison, les arguments d'un `server_tool_use` dont le `name` vaut `web_search` (la requête de recherche) ne sont pas réécrits non plus, à la différence des autres outils serveur et MCP. Cette contrainte est lue comme **spécifique à la recherche web**, sur la seule foi de la documentation : si elle s'avérait valoir pour le tour entier, réécrire les arguments d'un `server_tool_use` `code_execution` ou le `stdout` d'un `bash_code_execution_tool_result` rouvrirait le même risque de 400 sur ces tours-là. Trancher demande un test réel contre le fournisseur. Enfin, `text_editor_code_execution_tool_result` (les opérations de fichiers du nœud d'exécution de code) et tout autre type de bloc que le nœud ne reconnaît pas encore ne sont pas traités spécifiquement — leur forme varie trop pour être devinée sans risque ; ce sont des limites connues, pas des oublis silencieux.

Côté texte ordinaire, les trois orthographes sont couvertes : `text` (routes Messages et Chat Completions), `input_text` et `output_text` (OpenAI Responses, respectivement le tour utilisateur et le tour assistant), qui portent leur texte dans le même champ. `search_result`, lui, est du texte libre fourni par le client mais tombe encore dans le fourre-tout ci-dessous : transmis en clair et signalé comme fuite, là où il pourrait être pseudonymisé — c'est un manque identifié, pas un choix.

Plus largement, tout type de bloc que le nœud ne reconnaît pas — un nouveau bloc qu'un fournisseur ajoute, ou `text_editor_code_execution_tool_result` ci-dessus — est transmis tel quel plutôt que refusé ou traité comme une pièce jointe illisible. Seuls les types qui portent effectivement un fichier attaché par un humain (`file`, `input_file`, `input_image`, `input_audio`, `image_url`, `document`, `image`, `container_upload`) suivent la politique `unsupported_attachments`. Ce choix privilégie la continuité de la conversation à la complétude de la pseudonymisation : le coût de transmettre un bloc inconnu est borné, celui de refuser la requête casse toute une classe de clients pour une fonctionnalité que le nœud n'a simplement pas encore apprise.

Le **prompt système de haut niveau** de la route Messages n'est pas pseudonymisé. Il voyage dans le champ `system` de la requête, en dehors du tableau `messages` sur lequel le pipeline travaille, et il est reporté tel quel vers le fournisseur. Des données personnelles placées là par le client partent donc en clair : à traiter comme un texte de configuration, pas comme un endroit où écrire un nom ou une adresse.

Les appels d'outils **au format OpenAI** sont couverts eux aussi : leurs arguments vivent dans `tool_calls[].function.arguments`, en dehors du contenu du message, sous la forme d'un document JSON encodé dans une chaîne ; il est décodé, ses feuilles textuelles sont remplacées, puis il est ré-encodé. L'`id` de l'appel, son `type` et le nom de la fonction sont préservés, comme pour un `tool_use`.

Quatre limites à connaître. Le champ `function_call`, l'ancêtre déprécié de `tool_calls` que quelques clients anciens émettent encore, n'est pas traité : ses arguments partent tels quels. Comme une conversation agentique fait presque toujours remplacer quelque chose, ces échanges perdent le streaming — la réponse n'est renvoyée qu'une fois complète. En mode strict avec `verification_on_leak` à `allow`, une fuite détectée fait passer la requête **entière** en passe-plat : l'événement `sensitive-data.leak` le signale, mais du trafic d'outils transporte un fichier là où un message ordinaire transporte une phrase — `block` refuse plutôt que de transmettre. Enfin, `builtin_secret_patterns` redige les secrets détectés (clé API, jeton…) sans conserver de correspondance réversible : si un agent lit un fichier contenant un secret puis le réécrit, le marqueur de redaction atterrit dans le fichier à la place de la valeur d'origine, perdue sans retour possible — désactivez `builtin_secret_patterns` sur le modèle virtuel concerné si des agents doivent pouvoir modifier des fichiers contenant des identifiants réels.

L'écran du plugin expose d'autres réglages fins (fusion des entités adjacentes, complétion des noms, reclassification des prénoms, SIREN contextuel, limites par entité). Les valeurs par défaut conviennent à un déploiement français.
