# Paramètres

![Page des paramètres](./screenshots/parametres/image.png)

## Qu'est-ce que les paramètres ?

Les paramètres regroupent les réglages généraux de l'organisation : la devise utilisée pour les budgets et l'affichage des coûts, la répartition équitable du budget et la rétention des événements.

## Accéder aux paramètres

1. Allez dans votre organisation : `/orgs/{slug}/`
2. Cliquez sur **Paramètres** dans le menu admin

> **Note** : Vous devez disposer de la permission `settings:write` pour modifier les paramètres.

## Devise préférée

La devise sert aux budgets et à l'affichage des coûts dans l'interface.

### Devises disponibles

USD, EUR, GBP, JPY, CHF, CAD, AUD

### Impact sur les budgets existants

> **Attention** : Changer la devise ne convertit aucun montant. Les budgets existants gardent leur valeur numérique, qui s'exprime désormais dans la nouvelle devise : un budget de 100 $ devient un budget de 100 €. La nouvelle devise s'applique aussi aux budgets créés ensuite et à l'affichage des coûts dans l'interface.

Pour éviter toute confusion, définissez la devise dès la création de l'organisation.

## Répartir le budget équitablement

Activez l'option **Répartir le budget de l'organisation équitablement entre les membres** pour diviser automatiquement le budget de l'organisation par le nombre de membres.

**Exemple** : Acme Corp compte 5 membres et dispose de 500 € de budget mensuel, soit 100 € par membre.

La page affiche un aperçu du budget partagé (journalier, mensuel ou annuel, selon les budgets définis pour l'organisation) et le nombre actuel de membres.

> Cette option ne s'applique qu'aux membres sans quota individuel défini, et le partage est recalculé à chaque changement du nombre de membres. Pour la règle de priorité complète entre quota personnel et quota partagé, voir le concept [Budgets et quotas](../decouvrir/budgets-et-quotas.md).

## Rétention des événements

La rétention définit le nombre maximum d'événements conservés dans la fenêtre glissante.

### Champs

| Champ | Description |
|-------|-------------|
| **Rétention des événements** | Nombre max d'événements conservés (fenêtre glissante) |

### Comportement détaillé

| Situation | Comportement |
|-----------|-------------|
| **Champ vide** | Utilise la valeur par défaut de la plateforme |
| **Valeur positive** | Conserve les N événements les plus récents |
| **Événements épinglés** | Conservés indéfiniment, hors de la fenêtre glissante |
| **Plafond global** | Limite maximale imposée par la plateforme, que la rétention ne peut pas dépasser |

### Fenêtre glissante

La fenêtre glissante conserve les événements les plus récents et supprime les plus anciens :

```
┌─────────────────────────────────────────────────┐
│ Fenêtre glissante (ex: 1000 événements)        │
├─────────────────────────────────────────────────┤
│ [événement 1] [événement 2] ... [événement 1000] │
└─────────────────────────────────────────────────┘
     ↑ anciens              ↑ récents
            ↑ supprimés automatiquement
```

**Exemple** : avec 1 500 événements et une rétention de 1 000, Xolo supprime les 500 événements les plus anciens.

### Épingler des événements

Certains événements peuvent être « épinglés », par exemple ceux liés à une alerte déclenchée. Xolo les conserve indéfiniment, hors de la fenêtre glissante.

## Permissions

| Action | Permission requise |
|--------|-------------------|
| Consulter les paramètres | `settings:read` |
| Modifier les paramètres | `settings:write` |
