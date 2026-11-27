# Budgets et quotas

Les budgets limitent les dépenses d'une organisation, d'une équipe ou d'un membre. Une fois un plafond atteint, les nouvelles requêtes API sont bloquées.

> Les budgets s'appliquent uniquement aux fournisseurs en mode **Pay-as-you-go**. Les fournisseurs en mode abonnement disposent de leurs propres limites de plan.

## Hiérarchie des budgets

Les budgets fonctionnent à plusieurs niveaux :

| Niveau | Description |
| --- | --- |
| Organisation | Budget global de l'organisation |
| Membre | Budget individuel d'un membre |
| Équipe | Enveloppe d'une [équipe](../organisation/equipes.md), tous ses membres confondus |
| Membre dans une équipe | Budget d'un membre pour les requêtes qu'il adresse à une équipe |

Une requête attribuée à une équipe est vérifiée contre chaque niveau de son chemin : le budget de l'organisation, celui du membre, celui de l'équipe et celui du membre dans l'équipe. Elle est refusée dès que l'un d'eux est atteint. Une requête attribuée à l'organisation seule ne lit pas les niveaux d'équipe.

Chaque niveau peut définir un budget **journalier**, **mensuel** et/ou **annuel**. Un champ laissé vide signifie « illimité ».

## Calcul du budget effectif

Pour chaque période, Xolo prend le **minimum** des budgets définis :

- Si l'organisation a un budget mensuel de 100 € et un membre de 50 €, le membre est limité à 50 €/mois.
- Si le membre n'a pas de budget personnel, il utilise le budget de l'organisation.

## Partage équitable du budget

L'option **Partager le budget équitablement**, dans les [Paramètres](../organisation/parametres.md) de l'organisation, divise le budget de l'organisation entre tous ses membres.

**Exemple** : avec 500 €/mois et 5 membres, chaque membre dispose de 100 €/mois.

Cette règle ne s'applique qu'aux membres sans quota individuel défini :

1. si un membre a un quota personnel, Xolo utilise ce quota ;
2. sinon, Xolo utilise le quota partagé (budget de l'organisation ÷ nombre de membres).

> Xolo recalcule le partage à chaque modification du nombre de membres.

Une équipe a la même option, sur sa page. Son enveloppe est alors divisée entre ses membres sans budget propre dans l'équipe.

La division est entière et arrondie à l'inférieur, au millionième de l'unité monétaire près. Partagé entre 3 membres, un budget de 1 € donne 0,333333 € à chacun. Le reste ne peut pas être dépensé.

Une équipe qui partage son enveloppe mais n'a plus de membres n'a rien à diviser. Une requête qui lui est encore attribuée, par une application rattachée par exemple, est bornée par l'enveloppe entière.

## Répartition entre niveaux

Le budget d'une organisation contraint ceux de ses équipes, et le budget d'une équipe ceux de ses membres, selon la règle de **répartition** choisie sur le niveau parent. La règle s'applique à l'enregistrement d'un budget, d'un enfant comme du parent :

| Règle | Effet |
| --- | --- |
| **Libre** (défaut) | Aucune contrainte. Seules les limites vérifiées à chaque requête s'appliquent. |
| **Plafonnée** | Chaque budget enfant est au plus égal à celui du parent, période par période. Leur somme peut le dépasser. |
| **Partagée** | La somme des budgets enfants ne dépasse pas le budget du parent, période par période. L'enveloppe est répartie entre les enfants. |

Xolo refuse donc de baisser le budget d'un parent si les budgets enfants déjà enregistrés n'y tiennent plus. Il faut d'abord baisser ces derniers.

Un enfant sans budget propre sur une période n'est pas contraint. Il reste borné par le parent à chaque requête.

## Consulter son propre budget

Un utilisateur lit son propre budget dans une organisation avec `GET /api/v1/quota?organization=<orgID>`. Cette route accepte les mêmes authentifications que le proxy : jeton d'utilisateur, ID token OIDC ou session. Une application peut ainsi afficher à ses utilisateurs leur consommation en relayant leur ID token.

La réponse ne porte que sur le niveau de l'utilisateur, c'est-à-dire le budget comparé à son propre compteur :

- son quota personnel s'il en a un (`"source": "personal"`) ;
- sinon sa part du budget de l'organisation, si celle-ci le partage équitablement (`"shared"`) ;
- sinon aucun budget (`"none"`).

Les budgets de l'organisation, des équipes et des membres d'équipe ont leurs propres compteurs et ne sont pas renvoyés. Ils peuvent donc bloquer une requête avant le budget de l'utilisateur.

Quand le budget de l'organisation est inférieur à celui de l'utilisateur, les requêtes s'arrêtent au montant de l'organisation. `remaining` n'est alors qu'une borne supérieure. Le refus peut être signalé au niveau de l'utilisateur (`User daily budget exceeded`, par exemple), car son compteur est comparé au plus petit des deux.

```json
{
  "organization_id": "00000000-0000-0000-0000-000000000102",
  "source": "personal",
  "currency": "EUR",
  "periods": {
    "daily":   {"budget": 1000000, "spent": 120000, "remaining": 880000, "resets_at": "2026-10-14T00:00:00+02:00"},
    "monthly": {"budget": null, "spent": 2400000, "remaining": null, "resets_at": "2026-11-01T00:00:00+01:00"},
    "yearly":  {"budget": null, "spent": 9800000, "remaining": null, "resets_at": "2027-01-01T00:00:00+01:00"}
  }
}
```

Les montants sont en millionièmes de l'unité monétaire. `budget` et `remaining` valent `null` quand la période est illimitée, et `remaining` ne descend pas sous zéro. `spent` compte les dépenses de l'utilisateur dans l'organisation depuis le début de la période, hors usage couvert par un abonnement. Les périodes suivent le fuseau horaire du serveur.

L'organisation est choisie dans cet ordre :

1. celle du jeton, pour un jeton rattaché à une organisation ;
2. sinon le paramètre `organization` ;
3. sinon l'unique organisation de l'utilisateur.

Sans paramètre, un utilisateur de plusieurs organisations reçoit `400`. Une organisation dont il n'est pas membre actif, ou différente de celle du jeton, donne `404`. Un jeton d'application reçoit `403`, car son budget est celui de l'application. Aucune permission n'est requise, car chacun ne lit que son propre budget.

Comme `GET /api/v1/models`, cette route échappe à la limite de débit par adresse IP. Les applications l'appellent souvent depuis une seule adresse pour tous leurs utilisateurs.

## Permissions associées

| Action | Permission requise |
| --- | --- |
| Consulter les budgets | `quota:read` |
| Modifier les budgets | `quota:write` |

Voir la page [Budget](../organisation/budget.md) pour la marche à suivre.
