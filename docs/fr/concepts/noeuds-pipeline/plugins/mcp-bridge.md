# mcp-bridge

> Plugin livré par défaut, famille « Outils et test ». Retour à la [vue d'ensemble des nœuds](../index.md).

**mcp-bridge** connecte un serveur MCP et expose ses outils au modèle.

## Ports

| Port | Sens | Type | Requis |
| --- | --- | --- | --- |
| `request` | entrée | request | oui |
| `request` | sortie | request | — |

## Configuration

Ce plugin ouvre son propre écran de configuration dans le panneau de droite.

| Champ | Rôle | Défaut |
| --- | --- | --- |
| `endpoint` | URL du serveur MCP |  |
| `authHeaderName` | Nom de l'en-tête d'authentification |  |
| `toolFilter` | Outils autorisés, vide pour tous |  |
| `resources` | Ressources MCP lues et injectées avant le premier appel, une par ligne |  |
| `prefetchTool` | Outil appelé une fois avant le premier appel au modèle, à chaque requête |  |
| `prefetchArguments` | Arguments JSON de cet outil, avec jetons | `{}` |
| `prefetchLabel` | Titre du bloc injecté |  |
| `prefetchResultTemplate` | Gabarit du résultat, `{{result}}` remplacé par le texte |  |
| `timeoutSeconds` | Délai maximal en secondes |  |
| `maxConsecutiveToolCalls` | Appels d'outils consécutifs au maximum |  |

## En pratique

Le modèle peut appeler les outils pendant la génération, la passerelle exécute l'appel et renvoie le résultat. Les secrets d'authentification ne sont jamais écrits dans le graphe. Les résultats d'outils sont la surface d'injection indirecte ; [`prompt-guard`](./prompt-guard.md) les inspecte au fil de la boucle quand il est présent dans le pipeline.

Un modèle n'appelle pas un outil parce qu'on le lui demande, il l'appelle quand il estime en avoir besoin. Une recherche documentaire qu'il saute de temps en temps produit une réponse assurée sans rien derrière. Deux champs répondent à ça sans écrire de prompt.

`prefetchTool` nomme un outil que la passerelle appelle elle-même **avant le premier appel au modèle**, à chaque requête. Le résultat est ajouté à la conversation, et le modèle le lit comme un contexte qu'on lui donne. `prefetchArguments` dit avec quoi l'appeler. Ses valeurs peuvent contenir des jetons : `{{last_user_message}}`, `{{user_id}}`, `{{org_id}}`, `{{display_name}}` et `{{session_id}}`. Une requête dont le dernier message utilisateur est vide ne déclenche pas l'appel : un outil de recherche interrogé avec une question vide renvoie du vide, que le modèle doit ensuite interpréter. `prefetchResultTemplate` encadre le résultat, `prefetchLabel` le titre. L'outil pré-appelé doit figurer parmi les outils autorisés : le filtre reste ce que l'administrateur a accordé.

`resources` liste, une par ligne, des ressources MCP désignées par leur URI ou par le nom que le serveur leur donne. Elles sont lues à chaque requête et injectées de la même façon. Une ressource absente ou illisible n'échoue pas la requête : le texte injecté le dit, l'opérateur le voit dans la réponse du modèle. Un serveur qui n'annonce pas la capacité *resources* n'est pas interrogé. Le contenu binaire est signalé par son type plutôt qu'encodé dans la requête.

Les deux passent par les mêmes inspecteurs de résultats d'outils que les appels venant du modèle : un contexte pré-appelé est une surface d'injection indirecte au même titre qu'une page web rapportée par un outil.

Le plugin ouvre **une session MCP par conversation**, et non une par appel. Une session porte l'état que le serveur garde de son côté — une transaction, un jeu de résultats paginé — et le rouvrir à chaque tour d'outil le perd. La conversation est celle que la passerelle reçoit du client ; sans identifiant de session, c'est l'utilisateur et l'organisation qui en tiennent lieu. La session reste ouverte cinq minutes après le dernier appel, puis se referme. Une session que le serveur a terminée est détectée à l'appel suivant, qui la remplace et réessaie une fois.
