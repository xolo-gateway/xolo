# Rôles & Permissions

![Panneau de gestion des rôles](./screenshots/image1.png)

## Qu'est-ce qu'un rôle ?

Un rôle est un ensemble de permissions qui définit ce qu'un membre peut faire dans l'organisation. Chaque membre peut se voir attribuer plusieurs rôles.

Un rôle ne donne accès à aucun modèle précis : les modèles se règlent sur les [équipes](../equipes/equipes.md). Un rôle peut aussi être attribué à un membre sur une seule équipe, depuis la page de l'équipe : seules ses permissions marquées « équipe » dans l'éditeur s'appliquent alors, et seulement à cette équipe.

### Rôles intégrés

| Rôle       | Description                                                             |
| ---------- | ----------------------------------------------------------------------- |
| **Owner**  | Contrôle total de l'organisation (ne peut pas être modifié ni supprimé) |
| **Admin**  | Gestion complète de l'organisation sauf suppression                     |
| **Member** | Accès de base à l'organisation                                          |

### Rôles personnalisés

Vous pouvez créer vos propres rôles avec des combinaisons de permissions adaptées à vos besoins.

## Accéder aux rôles

1. Allez dans votre organisation : `/orgs/{slug}/`
2. Cliquez sur **Rôles** dans le menu admin

> **Note** : Vous devez disposer de la permission `roles:write` pour créer ou modifier des rôles.

## Créer un rôle

1. Cliquez sur **Nouveau rôle** (bouton en haut à droite)

2. Remplissez les informations :
   ![Formulaire de création de rôle](./screenshots/image2.png)

### Champs du formulaire

| Champ           | Description                                        |
| --------------- | -------------------------------------------------- |
| **Nom du rôle** | Nom affiché du rôle (ex: "Lecteur", "Développeur") |
| **Description** | Description optionnelle du rôle                    |

3. Cochez les permissions souhaitées dans les différentes catégories

4. Cliquez sur **Enregistrer**.

## Permissions disponibles

Les permissions sont organisées par catégorie :

### Membres

| Permission            | Description                            |
| --------------------- | -------------------------------------- |
| Consulter les membres | Voir la liste des membres              |
| Gérer les membres     | Inviter, modifier, retirer des membres |

### Rôles & permissions

| Permission          | Description                          |
| ------------------- | ------------------------------------ |
| Consulter les rôles | Voir les rôles existants             |
| Gérer les rôles     | Créer, modifier, supprimer des rôles |

### Équipes

| Permission                       | Description                                                       |
| -------------------------------- | ----------------------------------------------------------------- |
| Consulter les équipes            | Voir les équipes de l'organisation                                |
| Gérer les équipes et leurs accès | Créer, modifier, supprimer des équipes, choisir leurs modèles et middlewares |

### Fournisseurs & modèles

| Permission                            | Description                                        |
| ------------------------------------- | -------------------------------------------------- |
| Consulter les fournisseurs et modèles | Voir les configurations LLM                        |
| Gérer les fournisseurs et modèles     | Créer, modifier, supprimer fournisseurs et modèles |

### Modèles virtuels

| Permission                     | Description                                     |
| ------------------------------ | ----------------------------------------------- |
| Consulter les modèles virtuels | Voir les modèles personnalisés                  |
| Gérer les modèles virtuels     | Créer, modifier, supprimer des modèles virtuels |

### Middlewares

| Permission                | Description                                |
| ------------------------- | ------------------------------------------ |
| Consulter les middlewares | Voir les middlewares configurés            |
| Gérer les middlewares     | Créer, modifier, supprimer des middlewares |

### Budget

| Permission          | Description              |
| ------------------- | ------------------------ |
| Consulter le budget | Voir les budgets définis |
| Gérer le budget     | Modifier les budgets     |

### Invitations

| Permission                | Description                                |
| ------------------------- | ------------------------------------------ |
| Consulter les invitations | Voir les invitations en attente            |
| Gérer les invitations     | Créer, révoquer, supprimer des invitations |

### Applications

| Permission                 | Description                                 |
| -------------------------- | ------------------------------------------- |
| Consulter les applications | Voir les applications configurées           |
| Gérer les applications     | Créer, modifier, supprimer des applications |

### Paramètres

| Permission               | Description                           |
| ------------------------ | ------------------------------------- |
| Consulter les paramètres | Voir les paramètres de l'organisation |
| Gérer les paramètres     | Modifier les paramètres               |

### Usage

| Permission        | Description                         |
| ----------------- | ----------------------------------- |
| Consulter l'usage | Voir les statistiques d'utilisation |

### Événements & alertes

| Permission                          | Description                                  |
| ----------------------------------- | -------------------------------------------- |
| Consulter les événements globaux    | Voir tous les événements de l'organisation   |
| Gérer les alertes de l'organisation | Créer, modifier, supprimer des alertes org   |
| Créer des alertes personnelles      | Créer des alertes sur ses propres événements |

### Usage des modèles

| Permission                                  | Description                       |
| ------------------------------------------- | --------------------------------- |
| Utiliser tous les modèles de l'organisation | Accès à tous les modèles LLM, sans passer par une équipe |
| Utiliser tous les modèles virtuels          | Accès à tous les modèles virtuels, sans passer par une équipe |

### Permissions applicables à une équipe

Les permissions suivantes portent la marque « équipe » dans l'éditeur. Attribuées par un rôle sur une équipe, elles ne valent que pour cette équipe : `members:read`, `members:write`, `quota:read`, `quota:write`, `usage:read`, `events:read:all`, `invites:read` et `invites:write`. Les autres permissions d'un rôle attribué sur une équipe ne s'appliquent pas.

### Personnel

| Permission                            | Description                       |
| ------------------------------------- | --------------------------------- |
| Créer des modèles virtuels personnels | Créer des modèles virtuels privés |

## Modifier un rôle

1. Cliquez sur l'icône **Modifier** (crayon) sur la ligne du rôle

> **Attention** : Les rôles intégrés (Owner, Admin, Member) ne peuvent pas être supprimés, et leur nom/description ne peuvent pas être modifiés.

## Supprimer un rôle

1. Cliquez sur l'icône **Supprimer** (poubelle) sur la ligne du rôle personnalisé

> **Attention** : La suppression d'un rôle retire immédiatement les permissions associées à tous les membres qui le détenaient.

## Permissions

| Action                     | Permission requise |
| -------------------------- | ------------------ |
| Consulter les rôles        | `roles:read`       |
| Créer, modifier, supprimer | `roles:write`      |

## Règle implicite

Lorsqu'une permission d'écriture (`*:write`) est attribuée, la permission de lecture correspondante (`*:read`) est automatiquement accordée.

Pour le modèle de permissions dans son ensemble (rôles intégrés, équipes), voir le concept [Organisation, rôles et permissions](../../../concepts/organisation-et-permissions.md).
