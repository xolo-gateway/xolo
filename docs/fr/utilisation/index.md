# Utilisation

Cette section couvre les fonctionnalités que vous utilisez au quotidien en tant que membre d'une organisation Xolo : suivre votre activité, comprendre vos coûts et consulter ou configurer des alertes.

| Page | Description |
| --- | --- |
| **[Tableau de bord](./dashboard/dashboard.md)** | Visualiser votre activité : requêtes, tokens, coûts, énergie et CO2 |
| **[Événements](./evenements/evenement.md)** | Historique des événements et gestion des alertes |

## Guides : construire un modèle virtuel

Chaque guide construit, pas à pas, un modèle virtuel autour d'un cas d'usage, et fournit son pipeline à importer. Les quatre pipelines sont vérifiés par la suite de tests de bout en bout de Xolo.

| Guide | Modèle virtuel construit |
| --- | --- |
| **[Protéger les données personnelles](./guides/donnees-personnelles/donnees-personnelles.md)** | Un assistant qui pseudonymise noms, lieux et identifiants avant l'appel au fournisseur, et les restitue dans la réponse |
| **[Un agent outillé, protégé des injections de prompt](./guides/agent-outille/agent-outille.md)** | Un agent documentaire relié à un serveur MCP, qui refuse les tentatives de manipulation venues de l'utilisateur comme des outils |
| **[Choisir le modèle automatiquement](./guides/selection-automatique/selection-automatique.md)** | Un modèle qui oriente chaque demande vers le modèle le moins cher capable d'y répondre |
| **[Lever des événements personnalisés](./guides/evenements-personnalises/evenements-personnalises.md)** | Un modèle qui signale l'utilisateur proche de la fin de son budget, et l'alerte qui va avec |

> Vous cherchez comment configurer un fournisseur LLM, gérer les membres ou les budgets de votre organisation ? Direction la section **[Administration](../administration/index.md)**.
