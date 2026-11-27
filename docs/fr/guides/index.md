# Guides : construire un modèle virtuel

Chaque guide construit, pas à pas, un modèle virtuel autour d'un cas d'usage, et fournit son pipeline à importer. Les quatre pipelines sont vérifiés par la suite de tests de bout en bout de Xolo.

Ils supposent une organisation en service, avec au moins un fournisseur et un modèle : voir [Premiers pas](../premiers-pas/index.md). Vous devez avoir la permission d'écrire les modèles virtuels.

| Guide | Modèle virtuel construit |
| --- | --- |
| **[Protéger les données personnelles](./donnees-personnelles.md)** | Un assistant qui pseudonymise noms, lieux et identifiants avant l'appel au fournisseur, et les restitue dans la réponse |
| **[Un agent outillé, protégé des injections de prompt](./agent-outille.md)** | Un agent documentaire relié à un serveur MCP, qui refuse les tentatives de manipulation venues de l'utilisateur comme des outils |
| **[Choisir le modèle automatiquement](./selection-automatique.md)** | Un modèle qui oriente chaque demande vers le modèle le moins cher capable d'y répondre |
| **[Lever des événements personnalisés](./evenements-personnalises.md)** | Un modèle qui signale l'utilisateur proche de la fin de son budget, et l'alerte qui va avec |

Le détail de chaque nœud utilisé est dans la [référence des nœuds de pipeline](../reference/noeuds/index.md).
