# Budgets et quotas

Les budgets permettent de limiter les dépenses d'une organisation, d'une équipe ou d'un membre. Une fois un plafond atteint, les nouvelles requêtes API sont bloquées.

> Les budgets s'appliquent uniquement aux fournisseurs en mode **Pay-as-you-go**. Les fournisseurs en mode abonnement disposent de leurs propres limites de plan.

## Hiérarchie des budgets

Les budgets fonctionnent à plusieurs niveaux :

| Niveau | Description |
| --- | --- |
| Organisation | Budget global de l'organisation |
| Membre | Budget individuel d'un membre |
| Équipe | Enveloppe d'une [équipe](../administration/organisation/equipes/equipes.md), tous ses membres confondus |
| Membre dans une équipe | Budget d'un membre pour les requêtes qu'il adresse à une équipe |

Une requête attribuée à une équipe est vérifiée contre chaque niveau de son chemin : le budget de l'organisation, celui du membre, celui de l'équipe et celui du membre dans l'équipe. Elle est refusée dès que l'un d'eux est atteint. Une requête attribuée à l'organisation seule ne lit pas les niveaux d'équipe.

Chaque niveau peut définir un budget **journalier**, **mensuel** et/ou **annuel**. Un champ laissé vide signifie « illimité ».

## Calcul du budget effectif

Pour chaque période, Xolo prend le **minimum** des budgets définis :

- Si l'organisation a un budget mensuel de 100 € et un membre de 50 € → le membre est limité à 50 €/mois.
- Si le membre n'a pas de budget personnel → il utilise le budget de l'organisation.

## Partage équitable du budget

L'option **Partager le budget équitablement** (dans les [Paramètres](../administration/organisation/parametres/parametre.md) de l'organisation) divise automatiquement le budget de l'organisation entre tous ses membres.

**Exemple** : une organisation avec 500 €/mois et 5 membres → chaque membre dispose de 100 €/mois.

Cette règle ne s'applique **qu'aux membres sans quota individuel défini** :

1. si un membre a un quota personnel → son quota est utilisé ;
2. sinon → le quota partagé (budget de l'org ÷ nombre de membres) est utilisé.

> Le partage est recalculé automatiquement à chaque modification du nombre de membres.

Une équipe a la même option, sur sa page : son enveloppe est alors divisée entre ses membres sans budget propre dans l'équipe.

La division est entière et arrondie à l'inférieur, au millionième de l'unité monétaire près : partagé entre 3 membres, un budget de 1 € donne 0,333333 € à chacun. Le reste ne peut pas être dépensé.

Une équipe qui partage son enveloppe mais n'a plus de membres n'a rien à diviser : une requête qui lui est encore attribuée, par une application rattachée par exemple, est bornée par l'enveloppe entière.

## Répartition entre niveaux

Le budget d'une organisation contraint ceux de ses équipes, et le budget d'une équipe ceux de ses membres, selon la règle de **répartition** choisie sur le niveau parent. La règle s'applique à l'enregistrement d'un budget, d'un enfant comme du parent :

| Règle | Effet |
| --- | --- |
| **Libre** (défaut) | Aucune contrainte : seules les limites vérifiées à chaque requête s'appliquent. |
| **Plafonnée** | Chaque budget enfant est au plus égal à celui du parent, période par période. Leur somme peut le dépasser. |
| **Partagée** | La somme des budgets enfants ne dépasse pas celle du parent, période par période : l'enveloppe est vraiment répartie. |

Baisser le budget d'un parent est donc refusé si les budgets enfants déjà enregistrés n'y tiennent plus : il faut d'abord baisser ces derniers.

Un enfant sans budget propre sur une période n'est pas contraint : il reste borné par le parent à chaque requête.

## Permissions associées

| Action | Permission requise |
| --- | --- |
| Consulter les budgets | `quota:read` |
| Modifier les budgets | `quota:write` |

Voir le tutoriel [Budget](../administration/organisation/budget/budget.md) pour la marche à suivre.
