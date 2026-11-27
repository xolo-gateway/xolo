# mcp-bridge

> Plugin livré par défaut, famille « Outils et test ». Retour à la [vue d'ensemble des nœuds](../index.md).

**mcp-bridge** connecte un serveur MCP (Model Context Protocol) et expose ses outils au modèle.

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

Le modèle peut appeler les outils pendant la génération. La passerelle exécute l'appel et lui renvoie le résultat. Les secrets d'authentification ne sont jamais écrits dans le graphe. Un résultat d'outil peut contenir des instructions adressées au modèle, ce qu'on appelle une injection indirecte. [`prompt-guard`](./prompt-guard.md) inspecte ces résultats à chaque tour de la boucle d'outils quand il est présent dans le pipeline.

Un modèle n'appelle pas un outil parce qu'on le lui demande, il l'appelle quand il estime en avoir besoin. Quand il saute une recherche documentaire, il répond avec assurance sans s'appuyer sur aucun document. Deux champs règlent ce problème sans écrire de prompt.

`prefetchTool` nomme un outil que la passerelle appelle elle-même avant le premier appel au modèle, à chaque requête. Le résultat est ajouté à la conversation, et le modèle le lit comme un contexte qu'on lui donne. `prefetchArguments` dit avec quoi l'appeler. Ses valeurs peuvent contenir des jetons : `{{last_user_message}}`, `{{user_id}}`, `{{org_id}}`, `{{display_name}}` et `{{session_id}}`. Une requête dont le dernier message utilisateur est vide ne déclenche pas l'appel. Un outil de recherche interrogé avec une question vide renvoie un résultat vide, que le modèle devrait ensuite interpréter. `prefetchResultTemplate` encadre le résultat et `prefetchLabel` lui donne un titre. L'outil pré-appelé doit figurer parmi les outils autorisés. Le pré-appel ne contourne pas le filtre fixé par l'administrateur.

`resources` liste, une par ligne, des ressources MCP désignées par leur URI ou par le nom que le serveur leur donne. Elles sont lues à chaque requête et injectées de la même façon. Une ressource absente ou illisible ne fait pas échouer la requête. Le texte injecté le signale, et l'opérateur le voit dans la réponse du modèle. Un serveur qui n'annonce pas la capacité *resources* n'est pas interrogé. Le contenu binaire est signalé par son type plutôt qu'encodé dans la requête.

Le pré-appel et les ressources passent par les mêmes inspecteurs de résultats d'outils que les appels venant du modèle. Un contexte pré-appelé peut porter une injection indirecte, au même titre qu'une page web rapportée par un outil.

Le contexte injecté est ajouté au dernier message de l'utilisateur, pas dans un message à part. Deux messages utilisateur consécutifs sont refusés par les fournisseurs qui imposent l'alternance des rôles, et fusionnés sans avertissement par les autres. Sans message utilisateur à compléter, le bloc forme le message à lui seul.

Un contenu bloqué par un inspecteur fait refuser la requête, comme un résultat d'outil que `prompt-guard` rejette. Un appel qui échoue, lui, ne fait pas échouer la requête. Le texte injecté dit « the tool failed » et le modèle répond avec ce qu'il a. La différence est voulue. Un serveur injoignable est un incident passager, alors que le rejet d'un contenu jugé hostile est une décision.

Le plugin ouvre **une session MCP par conversation**, et non une par appel. Une session porte l'état que le serveur garde de son côté, une transaction ou un jeu de résultats paginé par exemple. La rouvrir à chaque tour d'outil perdrait cet état. La conversation est celle que la passerelle reçoit du client. Quand le client n'en envoie pas, la passerelle en dérive une de l'utilisateur, de l'organisation, du jeton et du **premier message** de la conversation. Deux conversations qui commencent par le même message partagent alors la même session, et donc l'état que le serveur garde de son côté. Une requête pour laquelle aucune clé n'est dérivable (pas d'organisation ni d'utilisateur résolus) n'est pas mutualisée. Elle ouvre une session pour elle seule et la referme. La session reste ouverte cinq minutes après le dernier appel, puis se referme. Une session que le serveur a terminée est détectée à l'appel suivant, qui la remplace et réessaie une fois.
