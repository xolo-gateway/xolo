# Organisation, rôles et permissions

## L'organisation

L'**organisation** est l'entité centrale de Xolo : elle regroupe des **membres**, un ou plusieurs **fournisseurs** LLM, des **budgets**, des **middlewares** et des **paramètres** (devise, rétention des événements…). Toutes les données et configurations sont cloisonnées par organisation.

Un même utilisateur peut appartenir à plusieurs organisations ; il bascule de l'une à l'autre depuis le menu de l'interface.

## Le tenant

Au-dessus de l'organisation se trouve le **tenant**, la frontière d'isolation la plus externe : il possède des organisations et des utilisateurs. Rien ne franchit cette frontière — ni un compte, ni un jeton d'API, ni une session.

**Vous n'avez normalement pas à vous en soucier.** Par défaut, Xolo fonctionne avec un tenant unique nommé `default`, créé automatiquement : aucun sous-domaine n'est nécessaire, aucune URL ne change, et le tenant n'apparaît nulle part dans l'interface.

Il ne devient visible que sur une instance mutualisée, où plusieurs clients cohabitent. Le multi-tenant s'active alors par configuration (`XOLO_MULTITENANCY_ENABLED`), et chaque tenant est identifié par son sous-domaine selon un modèle configurable, par exemple `acme.xolo.example.com`. Un sous-domaine qui ne correspond à aucun tenant actif renvoie une erreur 404.

Deux tenants peuvent héberger une organisation portant le même slug, et une même identité peut y disposer de deux comptes distincts, sans aucune collision. La gestion des tenants passe exclusivement par l'[API de provisioning](../administration/provisioning/provisioning.md) ; il n'existe pas d'interface dédiée.

## Rôles

Un **rôle** est un ensemble de permissions attribué à un ou plusieurs membres. Xolo distingue deux catégories de rôles :

### Rôles intégrés

| Rôle | Description |
| --- | --- |
| **Owner** | Contrôle total de l'organisation (ne peut pas être modifié ni supprimé) |
| **Admin** | Gestion complète de l'organisation sauf suppression |
| **Member** | Accès de base à l'organisation |

Ces rôles ne peuvent être ni renommés, ni supprimés, ni voir leurs permissions modifiées.

### Rôles personnalisés

Une organisation peut créer autant de rôles personnalisés que nécessaire, en combinant librement les permissions disponibles (voir le tutoriel [Rôles](../administration/organisation/roles/roles.md) pour la liste complète des permissions et la marche à suivre).

## Modèle de permissions

Les permissions suivent un schéma `<ressource>:<action>`, par exemple `providers:write` ou `events:read:all`.

### Règle implicite lecture/écriture

Lorsqu'une permission d'écriture (`*:write`) est attribuée, la permission de lecture correspondante (`*:read`) est automatiquement accordée. Il n'est donc jamais nécessaire d'attribuer les deux permissions séparément.

### Accès restreint aux modèles

Au-delà des permissions globales (`model:use:org` par exemple), un rôle peut se voir accorder l'accès à une liste explicite de modèles. Ce mécanisme permet de limiter l'usage à certains modèles même sans permission globale d'usage.

## Membres et invitations

Un **membre** est un utilisateur rattaché à une organisation, avec un ou plusieurs rôles. L'entrée d'un nouveau membre dans une organisation passe toujours par une **invitation** : un lien, ciblé (lié à un email) ou ouvert (utilisable par n'importe qui), avec une expiration et/ou un nombre d'usages optionnels.

Pour la marche à suivre, voir les tutoriels [Membres](../administration/organisation/membre/membre.md) et [Invitations](../administration/organisation/invitation/invitation.md).
