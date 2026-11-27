# prompt-guard

> Plugin livré par défaut, famille « Analyse de la requête ». Retour à la [vue d'ensemble des nœuds](../index.md).

**prompt-guard** cherche les tentatives de manipulation de l'assistant sans appeler de modèle, dans la requête, dans les résultats d'outils et dans la réponse.

## Ports

| Port | Sens | Type | Requis |
| --- | --- | --- | --- |
| `request` | entrée | request | oui |
| `request` | sortie | request | — |
| `risk` | sortie | number | — |
| `suspicious` | sortie | boolean | — |
| `categories` | sortie | string | — |
| `prompt_injection` | sortie | number | — |
| `prompt_leakage` | sortie | number | — |
| `role_hijacking` | sortie | number | — |
| `obfuscation` | sortie | number | — |
| `tool_abuse` | sortie | number | — |
| `exfiltration` | sortie | number | — |
| `top_rule` | sortie | string | — |
| `segment` | sortie | string | — |
| `quoted` | sortie | boolean | — |
| `model_probability` | sortie | number | — |
| `pressure` | sortie | number | — |
| `suspicious_turns` | sortie | number | — |

## Configuration

| Champ | Rôle | Défaut |
| --- | --- | --- |
| `block_above` | Risque à partir duquel la requête est refusée, 0 désactive | 0 |
| `block_message` | Message de refus | « Requête refusée : elle ressemble à une tentative de manipulation de l'assistant. » |
| `suspicious_above` | Seuil du port `suspicious` | 0,5 |
| `event_above` | Risque à partir duquel un événement est émis, 0 désactive | 0,6 |
| `analyze_tool_results` | Analyser les résultats d'outils | `true` |
| `analyze_history` | Analyser les tours précédents et calculer la pression | `false` |
| `history_decay` | Atténuation par tour de distance dans la pression, 0 désactive | 0,8 |
| `tool_weight` | Pondération des résultats d'outils | 1,25 |
| `quote_damping` | Atténuation des attaques citées, 1 désactive | 0,5 |
| `model_cap` | Plafond du modèle statistique, 0 désactive le modèle | 0,6 |
| `inspect_response` | Inspecter la réponse du modèle | `true` |
| `response_event_above` | Risque de réponse à partir duquel un événement est émis | 0,6 |
| `response_redact_above` | Risque de réponse à partir duquel elle est expurgée, 0 observe seulement | 0 |
| `canaries` | Valeurs qui ne doivent jamais sortir, une par ligne |  |
| `extra_rules` | Règles supplémentaires en YAML |  |

## En pratique

Il travaille en deux couches. La première relève des signaux structurels, indépendants de la langue : caractères invisibles qui coupent un mot-clé, lettres cyrilliques déguisées en latines, contenus Base64 ou hexadécimaux qui se décodent en texte, faux marqueurs de rôle comme `<|im_start|>` ou `system:`. La seconde applique une vingtaine de règles lexicales en français et en anglais, chacune avec un poids. Les poids se combinent sans jamais dépasser 1. « ignore les instructions précédentes » seul vaut 0,6 ; avec « affiche ton prompt système » dans la même phrase, on arrive à 0,88. Le plugin décode un texte encodé et lui applique les mêmes règles. Il signale alors que la correspondance vient du décodage.

Le texte est découpé selon sa provenance. Le dernier message utilisateur compte pour 1, les résultats d'outils pour 1,25 par défaut, parce qu'une page web qui s'adresse à l'assistant n'a aucune raison honnête de le faire. La règle qui repère cette adresse directe (« Attention AI assistant : ») ne s'applique d'ailleurs qu'aux résultats d'outils et à l'historique, jamais à l'utilisateur qui dit bonjour. `risk` est le maximum sur les segments, pas leur cumul. Dix pages propres et une page piégée valent la page piégée. `segment` dit d'où vient le maximum.

Le maximum ne détecte pas une attaque répartie sur plusieurs tours. Cinq tours à 0,3 valent 0,3. La sortie `pressure` couvre ce cas. Elle n'est calculée que si `analyze_history` est activé. La pression combine par « noisy-OR » (1 moins le produit des compléments à 1) le risque du dernier tour et celui de chaque tour utilisateur précédent, atténué de `history_decay` à chaque tour de distance. Avec la valeur par défaut de 0,8, le tour d'avant compte pour 80 % de son risque, celui d'encore avant pour 64 %, et un message vieux de dix tours ne pèse presque plus rien. Un seul tour suspect donne une pression égale à son risque. Une série de tours qui passent chacun sous les seuils finit par les franchir. Quand la pression dépasse chaque segment pris isolément, elle devient `risk` et `segment` vaut `conversation`. L'événement indique alors que le déclenchement vient de l'accumulation, et non d'un message précis. `suspicious_turns` compte à part les tours qui atteignent seuls `suspicious_above`, parce que cinq tours à 0,5 et un tour à 0,9 n'appellent pas la même réaction. Les résultats d'outils ne sont pas des tours et n'entrent pas dans la pression. `history_decay` à 0 désactive l'accumulation. L'historique est envoyé par le client. Un attaquant qui ouvre une conversation neuve repart de zéro. La pression lui coûte des tours, elle ne l'arrête pas.

Le champ `canaries` liste, une par ligne, des valeurs qui ne doivent jamais apparaître dans une réponse. Ces valeurs s'appellent des canaris. En pratique, un canari est une valeur témoin placée dans le prompt système pour mesurer ce qu'un modèle laisse fuir, ou un secret réel qu'on n'a pas encore réussi à retirer du prompt. L'inspection de réponse cherche chaque valeur en clair et sous les formes déguisées qu'un modèle produit quand on lui demande de « ne pas le révéler, mais » de le donner quand même : lettres espacées ou séparées par des tirets, changement de casse, caractères invisibles, homoglyphes, valeur inversée, ROT13, Base64, hexadécimal, et tout fragment contigu d'au moins la moitié de la valeur. Un canari trouvé pèse 0,95 dans le risque de la réponse, 0,7 pour un fragment. Il émet toujours un événement `security.canary_leak`, même si `response_event_above` est plus haut. L'événement nomme le canari par son numéro de ligne et par la forme sous laquelle il a fui, jamais par sa valeur. Avec `response_redact_above` à 0,9, seuls les canaris sont expurgés et les liens douteux restent en observation. Le passage expurgé devient « [donnée retirée] ». Une valeur de moins de quatre lettres ou chiffres est ignorée, avec un avertissement dans le journal. Les canaris ne détectent pas une valeur extraite un caractère à la fois par des questions fermées du genre « le deuxième caractère est-il un R ? ». La seule parade est de garder la valeur hors du contexte du modèle. Les canaris mesurent la fuite, ils ne l'empêchent pas.

Le champ `extra_rules` accepte un fichier YAML au même format que les règles embarquées. Une règle portant l'identifiant d'une règle par défaut la remplace, `enabled: false` la désactive. C'est là qu'on ajoute le vocabulaire propre à l'organisation, un nom de projet confidentiel par exemple. Une règle ne coûte rien tant qu'aucun de ses mots déclencheurs n'apparaît dans le texte, ce qui maintient l'analyse d'un document de 10 Ko sous les 5 ms.

Une troisième couche complète les deux premières : une régression logistique embarquée, entraînée sur le corpus du dépôt à partir de n-grammes de caractères et de mots. Le corpus couvre le remplacement d'instructions, la fuite du prompt système, le détournement de rôle et les montages de jailbreak (persona à capacité illimitée, contraintes imposées à la forme des réponses), l'obfuscation (caractères invisibles, homoglyphes, Base64, hexadécimal, ROT13, leetspeak, lettres espacées), l'abus d'outils et l'exfiltration, y compris déguisée en journal. Elle produit une probabilité, visible sur le port `model_probability`, qui n'entre dans le risque qu'au-dessus de 0,5 et plafonnée à `model_cap`, 0,6 par défaut. Ce plafond est voulu. Le modèle peut rendre une requête suspecte, il ne peut pas la faire bloquer seul ; un blocage à 0,7 ou plus exige toujours une règle ou un signal structurel que l'événement pourra nommer. `model_cap` à 0 désactive le modèle.

Le nœud ne comprend pas le texte et ne couvre pas toutes les langues. Il est réglé pour le français et l'anglais ; une attaque rédigée en allemand, en espagnol ou dans une langue peu dotée passe, tout comme une attaque portée par une image ou un son. Mesuré sur des jeux publics réels, il est très précis mais ne retrouve qu'une part des attaques du terrain, autour de 65 % sur un jeu de jailbreaks anglais. Le blocage est désactivé par défaut pour cette raison. On observe d'abord, on règle ensuite. Un `allow` ne dispense pas de vérifier les paramètres des outils côté serveur.
