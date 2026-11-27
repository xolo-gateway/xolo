# Organisation, rôles et permissions

## L'organisation

L'**organisation** est l'entité centrale de Xolo. Elle regroupe des membres, des équipes, un ou plusieurs fournisseurs LLM, des budgets, des middlewares et des paramètres (devise, rétention des événements…). Toutes les données et configurations sont cloisonnées par organisation.

Un même utilisateur peut appartenir à plusieurs organisations. Il bascule de l'une à l'autre depuis le menu de l'interface.

## Le tenant

Au-dessus de l'organisation se trouve le **tenant**, la frontière d'isolation la plus externe. Il possède des organisations et des utilisateurs. Aucun compte, aucun jeton d'API et aucune session ne franchit cette frontière.

Par défaut, vous n'avez pas à vous en occuper. Xolo fonctionne avec un tenant unique nommé `default`, qu'il crée automatiquement. Aucun sous-domaine n'est nécessaire, aucune URL ne change et le tenant n'apparaît nulle part dans l'interface.

Il ne devient visible que sur une instance mutualisée, où plusieurs clients cohabitent. Le multi-tenant s'active alors par configuration (`XOLO_MULTITENANCY_ENABLED`), et chaque tenant est identifié par les domaines déclarés pour lui, par exemple `acme.xolo.example.com`. Un hôte qui n'est le domaine actif d'aucun tenant actif renvoie une erreur 404.

Deux tenants peuvent héberger une organisation portant le même slug, et une même identité peut y disposer de deux comptes distincts, sans collision. La gestion des tenants passe par l'[API de provisioning](../exploitation/provisioning.md). Il n'existe pas d'interface dédiée.

## Rôles

Un **rôle** est un ensemble de permissions attribué à un ou plusieurs membres. Xolo distingue deux catégories de rôles.

### Rôles intégrés

| Rôle | Description |
| --- | --- |
| **Propriétaire** | Contrôle total de l'organisation (ne peut pas être modifié ni supprimé) |
| **Administrateur** | Gestion complète de l'organisation sauf suppression |
| **Membre** | Accès de base à l'organisation |

Ces rôles ne peuvent être ni renommés ni supprimés, et leurs permissions ne sont pas modifiables.

### Rôles personnalisés

Une organisation peut créer autant de rôles personnalisés que nécessaire, en combinant les permissions disponibles. La page [Rôles et permissions](../organisation/roles.md) donne la liste complète des permissions et la marche à suivre.

## Modèle de permissions

Les permissions suivent un schéma `<ressource>:<action>`, par exemple `providers:write` ou `events:read:all`.

### Règle implicite lecture/écriture

Attribuer une permission d'écriture (`*:write`) accorde aussi la permission de lecture correspondante (`*:read`). Il n'est donc pas nécessaire d'attribuer les deux.

## Équipes

Un rôle fixe ce qu'un membre peut faire. Une **équipe** fixe les ressources dont il dispose. Elle regroupe des membres, les modèles et les middlewares qu'ils utilisent, et leur budget. Un membre peut appartenir à plusieurs équipes.

- **Accès aux modèles.** Un membre utilise un modèle si l'une de ses équipes y donne accès. Les permissions `model:use:org` et `model:use:virtual` ouvrent tous les modèles de l'organisation sans passer par une équipe. Le rôle intégré **Membre** les porte.
- **Attribution.** Chaque requête est attribuée à une équipe, choisie par le nom du modèle `<organisation>/<équipe>/<modèle>`, ou à l'organisation seule.
- **Rôles d'équipe.** Un rôle peut être attribué à un membre sur une équipe seulement. Ses permissions marquées « équipe » (membres, budget, usage, événements, invitations) ne valent alors que pour cette équipe. Les autres ne s'appliquent pas. L'organisation alloue, l'équipe répartit.

Voir la page [Équipes](../organisation/equipes.md).

## Membres et invitations

Un **membre** est un utilisateur rattaché à une organisation, avec un ou plusieurs rôles. L'entrée d'un nouveau membre dans une organisation passe toujours par une **invitation**. C'est un lien, ciblé (lié à un email) ou ouvert (utilisable par n'importe qui), qui peut porter une expiration, un nombre d'usages, ou les deux.

Pour la marche à suivre, voir les pages [Membres](../organisation/membres.md) et [Invitations](../organisation/invitations.md).
