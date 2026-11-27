# Membres

![Panneau de gestion des membres](./screenshots/membres/image1.png)

## Qu'est-ce qu'un membre ?

Un membre est un utilisateur qui appartient à votre organisation. Chaque membre reçoit un ou plusieurs rôles, qui déterminent ses permissions.

## Accéder à la gestion des membres

1. Allez dans votre organisation : `/orgs/{slug}/`
2. Cliquez sur **Membres** dans le menu admin

> **Note** : Vous devez disposer de la permission `members:read` pour voir les membres, et `members:write` pour les modifier.

## Inviter un nouveau membre

Pour ajouter un membre, vous devez d'abord créer une invitation. Consultez la page [Invitations](./invitations.md).

## Liste des membres

![Liste des membres](./screenshots/membres/image3.png)

La liste des membres affiche pour chaque utilisateur :

- **Nom d'affichage**
- **Email**
- **Rôle(s)** attribués
- **Statut** : Actif ou Inactif

## Actions disponibles

Pour chaque membre, trois actions sont disponibles :

| Action       | Icône              | Description                           |
| ------------ | ------------------ | ------------------------------------- |
| **Modifier** | Crayon             | Modifier les rôles du membre          |
| **Quota**    | Tirelire           | Définir un budget individuel          |
| **Retirer**  | Utilisateur avec X | Supprimer le membre de l'organisation |

## Modifier les rôles d'un membre

![Formulaire d'édition des rôles](./screenshots/membres/image4.png)

### Accéder au formulaire

Cliquez sur l'icône **Modifier** (crayon) sur la ligne du membre concerné.

### Informations du membre

Le formulaire affiche les informations du membre, en lecture seule :

- Nom d'affichage
- Email
- Date d'adhésion

### Attribuer des rôles

Le formulaire présente tous les rôles disponibles dans l'organisation sous forme de cases à cocher :

| Rôle intégré | Description |
| --- | --- |
| **Propriétaire** | Accès complet à l'organisation |
| **Administrateur** | Accès à toutes les sections d'administration |
| **Membre** | Accès de base : usage des modèles et de l'organisation |

Vous pouvez aussi créer des rôles personnalisés : voir [Rôles et permissions](./roles.md).

### Enregistrer

Cliquez sur **Enregistrer** pour appliquer les modifications, ou **Annuler** pour revenir sans rien modifier.

## Définir un budget individuel

Chaque membre peut disposer d'un budget personnel qui s'ajoute ou se limite au budget de l'organisation.

Cliquez sur l'icône **Quota** (tirelire) pour accéder aux paramètres de budget du membre.

Pour plus de détails, consultez la page [Budget](./budget.md).

## Retirer un membre

Cliquez sur l'icône **Retirer** (utilisateur avec X) pour supprimer un membre de l'organisation.

> **Attention** : Cette action est irréversible. Il faudra réinviter l'utilisateur pour qu'il rejoigne de nouveau l'organisation.

## Permissions

| Action                        | Permission requise |
| ----------------------------- | ------------------ |
| Consulter les membres         | `members:read`     |
| Modifier les rôles ou retirer | `members:write`    |
| Gérer les budgets individuels | `quota:write`      |
