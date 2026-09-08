# Concepts

Les sections [Utilisation](../utilisation/index.md) et [Administration](../administration/index.md) expliquent comment faire. Celle-ci explique pourquoi : les principes transversaux qui traversent les fonctionnalités de Xolo.

## Concepts clés

| Concept | Description |
| --- | --- |
| **Organisation** | Entité qui regroupe les membres, budgets et configurations |
| **Fournisseur** | Connexion à un service LLM (OpenAI, Mistral, etc.) |
| **Modèle** | Configuration d'un modèle avec ses coûts et capacités |
| **Modèle virtuel** | Modèle personnalisé avec pipeline de traitement |
| **Middleware** | Traitement appliqué dynamiquement aux requêtes |
| **Application** | Configuration M2M pour intégrer des services externes |

## Pages

| Page | Ce qu'elle explique |
| --- | --- |
| **[Organisation, rôles et permissions](./organisation-et-permissions.md)** | Le modèle d'organisation, les rôles intégrés et la logique des permissions |
| **[Fournisseurs, modèles et pipelines](./fournisseurs-modeles-pipeline.md)** | Comment fournisseurs, modèles, modèles virtuels, middlewares et plugins s'articulent |
| **[Nœuds de pipeline](./noeuds-pipeline.md)** | Chaque nœud intégré et chaque plugin livré : ports, configuration, quand s'en servir |
| **[Budgets et quotas](./budgets-et-quotas.md)** | La hiérarchie des budgets, leur calcul effectif et le partage équitable |
| **[Estimation énergétique et carbone](./estimation-energetique.md)** | La méthodologie utilisée pour estimer l'énergie et le CO2 d'une requête LLM |
| **[Langage de requête eventql](./eventql.md)** | La syntaxe utilisée pour interroger les événements et définir des alertes |
