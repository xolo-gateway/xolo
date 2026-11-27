# Budget

![Panneau de gestion du budget](./screenshots/budget/budget-define.png)

## Qu'est-ce que le budget ?

Le budget vous permet de limiter les dépenses de l'organisation ou d'un membre en particulier (voir [la gestion du quota par utilisateur](./membres.md)). Une fois le plafond atteint, les nouvelles requêtes API sont bloquées.

Les budgets s'appliquent uniquement aux fournisseurs en mode **Pay-as-you-go**. Les fournisseurs en mode abonnement disposent de leurs propres limites de plan.

## Accéder aux paramètres du budget

1. Rendez-vous dans votre organisation : `/orgs/{slug}/`
2. Cliquez sur **Budget** dans le menu latéral admin

> **Note** : Vous devez disposer de la permission `quota:write` pour modifier les budgets.

## Définir un budget pour l'organisation

![Formulaire de budget](./screenshots/budget/image1.png)

1. Sur la page Budget, remplissez un ou plusieurs champs :
   - **Budget journalier** : limite quotidienne
   - **Budget mensuel** : limite mensuelle
   - **Budget annuel** : limite annuelle

2. Laissez vide un champ dont la période doit rester illimitée.

3. Cliquez sur **Enregistrer**.

Les montants sont exprimés dans la devise de l'organisation, qui se choisit dans les Paramètres.

Le réglage **Répartition entre les équipes** contrôle les enveloppes des équipes au moment où elles sont enregistrées. Il prend trois valeurs : libre, plafonnée (l'enveloppe de chaque équipe est au plus égale au budget de l'organisation) ou partagée (la somme des enveloppes des équipes est au plus égale au budget de l'organisation). Xolo refuse un budget qui ne respecte pas la règle, avec un message qui indique la période en cause.

## Hiérarchie des budgets

Les budgets fonctionnent à quatre niveaux :

| Niveau       | URL                                     | Description                     |
| ------------ | ---------------------------------------- | -------------------------------- |
| Organisation | `/orgs/{slug}/admin/quota`              | Budget global de l'organisation |
| Membre       | `/orgs/{slug}/admin/members/{id}/quota` | Budget individuel d'un membre   |
| Équipe       | `/orgs/{slug}/admin/teams/{id}`         | Enveloppe d'une équipe          |
| Membre dans une équipe | `/orgs/{slug}/admin/teams/{id}/members/{membershipID}` | Budget d'un membre dans une équipe |

Voir la page [Équipes](./equipes.md) pour les budgets d'équipe.

Xolo peut aussi répartir automatiquement le budget de l'organisation entre ses membres (option **Répartir le budget de l'organisation équitablement entre les membres**, dans les [Paramètres](./parametres.md)).

Pour le détail du calcul du budget effectif et du partage équitable, voir le concept [Budgets et quotas](../decouvrir/budgets-et-quotas.md).

## Suivre la consommation

La page Budget affiche la consommation actuelle avec des barres de progression :

- le montant dépensé par rapport au budget maximum ;
- le pourcentage d'utilisation.

![Suivi de consommation](./screenshots/budget/budget-define.png)

## Permissions

| Action                | Permission requise |
| --------------------- | ------------------ |
| Consulter les budgets | `quota:read`       |
| Modifier les budgets  | `quota:write`      |

Ces permissions s'attribuent par les **Rôles** de l'organisation.
