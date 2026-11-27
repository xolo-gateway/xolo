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
| `engine` | Moteur de détection : `crf` ou `indecis` | `crf` |
| `strategy` | Stratégie d'anonymisation : `tag`, `hash` ou `redact` (`consistent` produit les mêmes jetons que `tag`) | `tag` |
| `language` | Langue | `auto` |
| `fallback_language` | Langue de repli | `fr` |
| `bias` | Prudence du moteur `indecis`, de 0 à 10 | 3 |
| `min_confidence` | Confiance minimale | 0,3 |
| `skip_types` | Types d'entités à ignorer, ceux que l'écran laisse décochés |  |
| `blocklist` | Liste de blocage |  |
| `builtin_regex_patterns` | Détection des identifiants par expressions régulières ; l'écran l'active tant qu'un identifiant est coché | `true` |
| `builtin_secret_patterns` | Détection des secrets ; l'écran l'active tant qu'un secret est coché | `true` |
| `process_attachments` | Traiter les pièces jointes documentaires | `true` |
| `unsupported_attachments` | Pièces jointes non traitables | `block` |
| `max_attachment_bytes` | Taille maximale par pièce jointe, en octets | 10 485 760 |
| `inject_instruction` | Instruction de préservation des jetons | `true` |
| `verification` | Observer les fuites sans bloquer | `true` |
| `verification_strict` | Mode strict : avec `verification_on_leak` à `block`, refuse une requête sur fuite | `false` |
| `verification_on_leak` | `block` refuse la requête ; `allow` ramène le mode strict à l'observation | `allow` |
| `cache_dir` | Répertoire de cache des modèles |  |
| `manifest_url` | URL du manifest des modèles `crf` |  |
| `model_url` | URL d'un miroir du modèle `indecis` |  |
| `model_dir` | Répertoire d'un modèle `indecis` déjà présent |  |
| `offline` | Mode hors-ligne | `false` |

## Moteur de détection

Deux moteurs trouvent les données à remplacer ; les expressions régulières (adresses électroniques, téléphones, IBAN, SIRET, SIREN, adresses IP, secrets) s'ajoutent à l'un comme à l'autre.

- `crf`, par défaut, est le modèle d'entités nommées de go-anon. Il compte un modèle par langue (français, anglais, espagnol) et trouve les personnes (`PER`), les lieux (`LOC`), les organisations (`ORG`) et le reste (`MISC`). Il reste précis sur les demandes courtes, celles qu'on adresse d'ordinaire à un LLM.
- `indecis` est le modèle de masquage d'[indecis-ner](https://github.com/Bornholm/indecis-ner), le même que celui d'[indecis-caviardage](https://github.com/beewii/indecis-caviardage). Un seul modèle lit toutes les langues, et `language` ne choisit plus que la langue de l'instruction donnée au LLM. Il masque aussi les identifiants indirects (âge, métier, proches, événements de vie) et les catégories particulières de données de l'article 9 du RGPD (santé, convictions, origine…), que le moteur `crf` laisse lisibles. Ses types sont `PERSON`, `CODE` (numéros de dossier, de compte…), `LOC`, `ORG`, `DEM` (caractéristiques d'une personne), `DATETIME`, `QUANTITY` et `MISC`.

Le moteur `indecis` masque largement. Dans « Explique la différence entre TCP et UDP », il masque `TCP` et `UDP`, et dans une demande de code, des mots du code. Il convient aux contenus riches en données personnelles (comptes rendus, courriers, dossiers, pièces jointes), pas à un assistant de code ou à des questions techniques. La prudence (`bias`) règle ce compromis. Plus elle est haute, moins il reste de données lisibles, et plus il y a de passages masqués inutilement. Sur les décisions de justice du jeu de test TAB, avec une prudence de 3, aucun identifiant direct n'est resté lisible en anglais ni en espagnol, 2 sur 366 en français, pour environ deux tiers de passages masqués à raison. La confiance minimale s'applique aussi aux passages détectés par le modèle. À 0,3, elle écarte la plupart des masques superflus d'une demande courte.

Le modèle `indecis` (environ 310 Mo) est téléchargé depuis la release d'indecis-ner au premier appel, vérifié par son empreinte SHA-256, puis gardé dans `cache_dir` (par défaut, le dossier `indecis-ner` du répertoire de cache de l'utilisateur). Un miroir peut le servir (`model_url`), avec les mêmes empreintes, et `model_dir` désigne un modèle déjà présent sur la machine, utilisé tel quel. Le premier appel attend la fin du téléchargement ; s'il est interrompu, la requête passe sans pseudonymisation et l'événement `passthrough` le signale, comme pour un modèle `crf` absent. Le modèle lit au plus quatre textes à la fois, toutes requêtes confondues, car chaque lecture occupe déjà tous les cœurs. Un texte qui attend sa place plus de 30 secondes n'est pas pseudonymisé ; il part tel quel et l'événement `passthrough` donne l'erreur (« le modèle indecis est occupé »), comme pour tout échec d'anonymisation. Une requête abandonnée par son client arrête son attente et sa lecture.

`PER` et `PERSON` désignent le même type dans `skip_types` et `blocklist`. Une configuration écrite pour un moteur ignore et bloque les mêmes personnes avec l'autre.

## En pratique

Il agit dans les deux sens, sur la requête puis sur la réponse. Xolo doit donc attendre la fin de la réponse avant de la renvoyer quand le plugin a remplacé quelque chose, ou quand la conversation montre au modèle un de ses jetons, même rendu inerte. Un jeton est le pseudonyme que le plugin écrit à la place d'une valeur. Le plugin émet un événement quand il détecte une donnée sensible. Au retour, les jetons sont restitués dans le texte de la réponse et dans les arguments des appels d'outils qu'elle contient. Le modèle dérive ses nouveaux appels de l'historique pseudonymisé qu'il vient de lire, et un jeton laissé dans un `Read(path=…)` ferait exécuter l'appel sur un chemin qui n'existe pas.

Il arrive que le modèle invente des jetons. Après avoir vu beaucoup de noms communs remplacés, il en écrit lui-même de nouveaux, avec le bon format et le bon nonce, mais qu'aucune correspondance ne permet de restituer. Ces jetons sont rendus inertes dans la réponse (`⟦LOCATION_15_95881f⟧` devient `[[LOCATION_15_95881f]]`), texte et arguments d'appels d'outils compris, et l'événement `placeholders.neutralized` le signale. Au tour suivant, un jeton du nœud trouvé dans la requête reçoit le même traitement avant la pseudonymisation. Le client ne voit jamais que du texte restitué, donc un tel jeton dans son historique est un reste de ce genre, ou une tentative d'en faire restituer un. Sans cette étape, l'anonymiseur refusait le contenu qui le portait, et ce contenu partait en clair pour le reste de la conversation.

Avec les stratégies `tag` et `consistent`, le numéro d'un jeton ne suit pas l'ordre d'apparition. Il est dérivé de la valeur remplacée à l'aide d'une clé. Cette clé est tirée au hasard pour le nœud au premier usage, conservée dans ses secrets (`index_key`), puis déclinée pour chaque utilisateur. Une même personne garde ainsi son jeton d'un tour à l'autre, ce qui stabilise aussi le préfixe envoyé au fournisseur pour son cache. Le modèle peut relire des jetons d'un tour précédent dans un bloc de raisonnement, que le nœud transmet sans le modifier. Un tel jeton, si la valeur qu'il remplaçait n'est plus dans la requête, n'est restitué à personne et devient inerte. Avec une numérotation dans l'ordre d'apparition, il aurait désigné la première personne de la requête en cours. Deux valeurs d'un même type peuvent tomber sur le même numéro, une chance sur un million environ. La seconde prend alors le numéro libre suivant, et son jeton peut changer d'un tour à l'autre. Aucune correspondance n'est conservée au-delà de la requête. Sans magasin de secrets, ou s'il est en erreur, la numérotation revient à l'ordre d'apparition et l'événement `index-key.unavailable` le signale. La clé est lue une fois par processus. Remplacer le secret `index_key` ne prend effet qu'au redémarrage de Xolo, et d'ici là deux réplicas peuvent numéroter différemment.

Chaque nœud ne traite ainsi que ses propres jetons, reconnus à leur nonce, qui dépend du nœud. Un middleware qui enveloppe un modèle virtuel peut placer deux pseudonymiseurs sur la même requête. Le nœud intérieur reçoit les jetons du nœud extérieur, les laisse intacts, pseudonymise le reste, et laisse au nœud extérieur le soin de les restituer au retour. Les jetons sans nonce (stratégie `hash`, marqueur `⟦…_REDACTED⟧` d'un secret caviardé) sont toujours laissés intacts. Ils ne font plus refuser le contenu, mais un jeton haché inventé par le modèle n'est pas neutralisé.

Devant un client agentique, il traite aussi les blocs d'outils du format Messages d'Anthropic. Le traitement varie selon ce que chaque forme de bloc autorise. Le nœud réécrit le contenu d'un `tool_result` et les arguments d'un `tool_use`. Il fait de même pour leurs équivalents côté serveur et MCP : les arguments de `server_tool_use` et de `mcp_tool_use`, le contenu de `mcp_tool_result` et de `bash_code_execution_tool_result`. Pour ce dernier, seuls `stdout` et `stderr` sont réécrits ; `return_code` et le reste de l'objet restent intacts. Les champs `id`, `tool_use_id` et `name`, qui apparient un appel à son résultat, restent intacts, de même que les noms d'arguments. Un contenu que le nœud ne sait pas lire, l'image d'une capture d'écran par exemple, est remplacé par une note. Le retirer laisserait l'appel correspondant orphelin.

Plusieurs formes traversent le nœud sans réécriture, chacune pour une raison différente, mais jamais sans signalement. Ce qui part sans être réécrit est scanné en lecture seule (`Detect`, jamais `Anonymize` : pas de session, rien de réécrit). Le résultat alimente l'événement `sensitive-data.detected` sous `leak_entities`/`leak_types`. Ces attributs sont tenus à part de `entities`/`types`, qui ne comptent que ce qui a été pseudonymisé. `entities` et `leak_entities` dénombrent tous deux des valeurs distinctes, et `leak_types` les répartit sur la même base. Une même adresse citée cinq fois dans un raisonnement compte pour une fuite, pas cinq, et la même valeur écrite de deux façons n'en fait qu'une. Ce scan a deux limites. Les champs qui ne portent jamais de texte libre, `encrypted_content`, `data`, `signature` et les identifiants, ne sont pas lus quand ils portent une chaîne, car les relire à chaque tour d'une conversation agentique coûte cher pour ne rien trouver. Une entrée de `content` qui n'est pas un objet JSON (une chaîne nue) est transmise sans être scannée.

Un bloc `thinking` ou `redacted_thinking` (raisonnement étendu) n'est ni retiré ni pseudonymisé. Le fournisseur le signe et vérifie la signature au tour suivant. Réécrire le texte du raisonnement invaliderait cette signature, tout comme modifier le champ `signature` lui-même. `redacted_thinking` ne porte de toute façon aucun texte lisible, seulement un bloc chiffré opaque. `web_search_tool_result` traverse lui aussi sans modification. La documentation exige que les blocs de contenu de l'assistant reviennent inchangés dans leur ensemble pour ce tour, `encrypted_content` inclus, et pas seulement ce champ. Même `title`, qui est bien du texte libre, n'est donc pas isolé pour être réécrit. Pour la même raison, les arguments d'un `server_tool_use` dont le `name` vaut `web_search` (la requête de recherche) ne sont pas réécrits non plus, à la différence des autres outils serveur et MCP. Le nœud tient cette contrainte pour spécifique à la recherche web, sur la seule foi de la documentation. Si elle valait pour le tour entier, réécrire les arguments d'un `server_tool_use` `code_execution` ou le `stdout` d'un `bash_code_execution_tool_result` exposerait ces tours-là au même risque d'erreur 400. Seul un test réel contre le fournisseur permettrait de trancher. Enfin, `text_editor_code_execution_tool_result` (les opérations de fichiers du nœud d'exécution de code) et tout autre type de bloc que le nœud ne reconnaît pas encore ne reçoivent aucun traitement particulier, car leur forme varie trop pour être devinée sans risque. Ce sont des limites connues, pas des oublis.

Pour le texte ordinaire, les trois types de bloc sont couverts : `text` (routes Messages et Chat Completions), `input_text` et `output_text` (OpenAI Responses, respectivement le tour utilisateur et le tour assistant), qui portent leur texte dans le même champ. `search_result`, lui, est du texte libre fourni par le client, mais il relève encore du traitement par défaut décrit ci-dessous. Il est transmis en clair et signalé comme fuite, alors qu'il pourrait être pseudonymisé. C'est un manque identifié, pas un choix.

Plus largement, le nœud transmet tel quel tout type de bloc qu'il ne reconnaît pas, qu'il s'agisse d'un nouveau bloc ajouté par un fournisseur ou de `text_editor_code_execution_tool_result` cité plus haut. Il ne le refuse pas et ne le traite pas comme une pièce jointe illisible. Seuls les types qui portent effectivement un fichier attaché par un humain (`file`, `input_file`, `input_image`, `input_audio`, `image_url`, `document`, `image`, `container_upload`) suivent la politique `unsupported_attachments`. Ce choix fait passer la continuité de la conversation avant l'exhaustivité de la pseudonymisation. Transmettre un bloc inconnu a un coût borné. Refuser la requête rendrait inutilisable toute une catégorie de clients, pour un type de bloc que le nœud ne sait pas encore traiter.

Le **prompt système de haut niveau** de la route Messages n'est pas pseudonymisé. Il voyage dans le champ `system` de la requête, en dehors du tableau `messages` sur lequel le pipeline travaille, et il est reporté tel quel vers le fournisseur. Des données personnelles placées là par le client partent donc en clair. Traitez ce champ comme un texte de configuration, pas comme un endroit où écrire un nom ou une adresse.

Les appels d'outils au format OpenAI sont couverts eux aussi. Leurs arguments se trouvent dans `tool_calls[].function.arguments`, en dehors du contenu du message, sous la forme d'un document JSON encodé dans une chaîne. Le nœud le décode, remplace ses valeurs textuelles, puis le réencode. L'`id` de l'appel, son `type` et le nom de la fonction sont préservés, comme pour un `tool_use`.

Le mode strict ne sert qu'à refuser. Avec `verification_on_leak` à `block`, une fuite détectée après pseudonymisation fait refuser la requête, et l'événement `sensitive-data.leak` le signale. Avec `allow`, il équivaut à l'observation, c'est-à-dire que la fuite est signalée et la requête part pseudonymisée. Dans les deux cas, l'événement `sensitive-data.leak` est émis une fois par requête, et son attribut `action` dit ce qu'il est advenu de la requête. En observation, il compte une fuite par valeur distincte ; quand la requête est bloquée, il compte les occurrences du contenu refusé, go-anon ne rendant pas ce contenu avec l'erreur. Il faisait auparavant partir la requête **entière** en clair, moins protégée qu'en observation.

Trois limites à connaître. Le champ `function_call`, l'ancêtre déprécié de `tool_calls` que quelques clients anciens émettent encore, n'est pas traité, et ses arguments partent tels quels. Comme une conversation agentique fait presque toujours remplacer quelque chose, ces échanges perdent le streaming. La réponse n'est renvoyée qu'une fois complète. Enfin, `builtin_secret_patterns` caviarde les secrets détectés (clé API, jeton…) sans conserver de correspondance réversible. Si un agent lit un fichier contenant un secret puis le réécrit, le marqueur de caviardage se retrouve dans le fichier à la place de la valeur d'origine, qui est définitivement perdue. Désactivez `builtin_secret_patterns` sur le modèle virtuel concerné si des agents doivent pouvoir modifier des fichiers contenant des identifiants réels.

L'écran du plugin suit l'ordre des décisions : le moteur et la langue des messages, ce qui est masqué (chaque type a sa case, ce qui permet par exemple de masquer les personnes et de laisser les lieux lisibles), le remplacement (avec la clé HMAC de la stratégie `hash`, enregistrée à part dans les secrets du nœud), la conduite en cas de fuite, puis les pièces jointes. Les réglages fins (confiance minimale, limites par passage, SIREN contextuel, prénoms et fusion du moteur `crf`, stockage des modèles) sont repliés sous « Réglages avancés ». Les champs propres à un moteur ou à une stratégie suivent le choix fait sur la page, avant même l'enregistrement. Les valeurs par défaut conviennent à un déploiement français.
