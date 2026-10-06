# Passage aux UUID et migrations hors ligne

La migration `202610020001` convertit les identifiants des tenants, organisations
et utilisateurs en UUID. Elle conserve les UUID existants, réécrit les relations
et préserve les rôles plateforme. Les autres identifiants (tokens, modèles,
nœuds des graphes, etc.) gardent leur format actuel.

**Arrêtez tous les anciens serveurs, réplicas, workers et processus écrivant en
base avant cette mise à niveau. Le déploiement progressif est incompatible.** Un
ancien binaire peut encore écrire les anciens identifiants dans les usages et
quotas : ces lignes deviennent orphelines et des dépenses peuvent échapper aux
quotas. Le verrou de migration sérialise les nouvelles migrations, mais ne bloque
pas les anciens binaires. Le démarrage affiche un avertissement explicite.

## Procédure recommandée

1. Sauvegardez la base avec une procédure cohérente adaptée au moteur. Pour
   SQLite, incluez l'état du WAL ; copier uniquement un fichier `.sqlite` actif
   ne suffit pas.
2. Répétez les commandes ci-dessous sur une copie restaurée.
3. Arrêtez tous les processus d'écriture sur la base réelle et faites une dernière sauvegarde.
4. Générez et relisez un plan sur cette base arrêtée, résolvez tous les diagnostics,
   puis appliquez exactement le plan enregistré.
5. Redémarrez uniquement les nouveaux binaires ; vérifiez connexion, organisations,
   tokens API, alertes et totaux des quotas avant de rouvrir le trafic.

Les archives de livraison et l'image Docker contiennent `xolo-migrate`.
Depuis les sources, `make build-migrate` produit `bin/migrate` ; utilisez ce chemin
à la place dans les exemples. Seule `XOLO_STORAGE_DATABASE_DSN` est nécessaire.
La commande ne charge pas `.env` automatiquement. Indiquez le fichier SQLite
existant ou le DSN PostgreSQL dans cette variable.

```bash
export XOLO_STORAGE_DATABASE_DSN=/data/data.sqlite
xolo-migrate diagnose
xolo-migrate plan -out recovery.json
# Relire recovery.json et ajouter les corrections explicites nécessaires.
xolo-migrate diagnose -plan recovery.json
xolo-migrate apply -plan recovery.json -writers-stopped
export XOLO_STORAGE_AUTO_MIGRATE=false
xolo-server
```

`diagnose` et `plan` utilisent une vue cohérente en lecture seule et ne migrent
jamais la base. Un diagnostic bloquant renvoie un code de sortie non nul et un
rapport JSON exploitable. `plan` enregistre son artefact même si des problèmes
restent à résoudre. Le fichier est créé avec les permissions `0600` ; un fichier
existant n'est jamais écrasé. Conservez ce mapping et réutilisez-le lors des reprises.

`apply` exécute toute la chaîne de migrations dans une transaction unique,
marqueurs compris, et revalide le plan sous verrou. Tout échec annule l'ensemble.
Réappliquer le même plan réussi est idempotent ; un artefact différent est refusé
après migration. L'artefact est aussi enregistré dans le point de reprise en base.

Pour une installation neuve ou une migration sans décision manuelle, utilisez
`xolo-migrate apply -writers-stopped` sans plan. Une base neuve n'a pas besoin de
`plan`, qui attend le schéma tenant/utilisateur/organisation de la version précédente.

## Résoudre les diagnostics

| Cas | Résolution |
| --- | --- |
| Anciens identifiants, y compris non-xid comme `org-acme` | Conversion automatique. Conservez le mapping `ids` généré ; les UUID valides restent inchangés. |
| Emails ne différant que par la casse ou les espaces dans un tenant | Conservés exactement, casse et espaces compris. Cette migration ne normalise pas les emails et ne fusionne aucun compte. |
| Comparaisons EventQL exactes sur `user`, `org`, `actor_id`, `user_id`, `org_id` et les autres attributs d'identifiant reconnus | Réécriture automatique avec la bonne famille. Les sélecteurs indexés acceptent `user`/`org` ; `user_id`/`actor_id` sont des filtres d'attributs. Les attributs historiques des événements sont également réécrits. |
| Références de graphe dans des champs d'identifiant reconnus ou des valeurs `value` exactes | Réécriture automatique ; les identifiants de nœuds et les arêtes sont conservés. |
| Expression régulière EventQL contenant un ancien ID, script/configuration opaque ou ID ambigu | Le rapport indique table, ligne, colonne et chemin JSON du graphe. Ajoutez une entrée `serialized_overrides` comme ci-dessous. |
| Requête ou JSON invalide | Fournissez une correction sérialisée valide. Un sélecteur inconnu comme `{user_id="..."}` doit devenir un sélecteur reconnu ou un filtre d'attribut. |
| Mapping incomplet, périmé, UUID invalide ou dupliqué | Régénérez un plan non appliqué depuis la base définitivement arrêtée et relisez-le. Ne modifiez jamais un mapping déjà appliqué. |
| ID primaire vide ou relation orpheline | Réparez les données source après répétition sur sauvegarde, puis régénérez le plan. Le diagnostic ne supprime aucune ligne. |

Les plans utilisent la **version 2** et contiennent uniquement les mappings
`ids` et les corrections facultatives `serialized_overrides`. Les plans version 1
sont refusés : régénérez-les avec `xolo-migrate plan -out recovery-v2.json` sur la
base arrêtée, antérieure à la migration, puis relisez chaque correction.
Ne changez pas le numéro de version à la main. Les champs inconnus sont refusés.

Les emails, identités provider/subject et toutes les affectations de rôles
plateforme et d'adhésion sont conservés, même avec plusieurs rôles intégrés.
Les domaines, nouvelles notions de rôles/statuts et compteurs de publication
relèvent d'une migration ultérieure.

Les mappings sont chargés par lots dans une table temporaire indexée. Chaque
référence relationnelle déclarée est réécrite par une opération SQL ; les champs
sérialisés sont lus par pages de 1 000 lignes et mis à jour par lots. Tous les lots
restent dans la même transaction atomique. Le verrou est réservé aux migrations ;
les écritures ordinaires gardent leurs transactions et le cache configuré.

Pour une correction sérialisée, ajoutez ce tableau au plan existant. Remplacez le
nouvel UUID par celui de `ids.users` ou de la famille concernée :

```json
"serialized_overrides": [{
  "table": "alerts",
  "id": "id-alerte-existante",
  "column": "query",
  "before": "{user=~\"ancien-id-utilisateur\"}",
  "after": "{user=\"UUID-DU-PLAN\"}"
}]
```

`before` doit correspondre exactement au champ en base. Une modification
intermédiaire rend le plan périmé et bloque son application. `after` remplace la
totalité du champ : incluez toutes les corrections et préservez le reste de la
configuration. Des valeurs `before`/`after` identiques confirment explicitement un
texte volontairement littéral. Seuls les requêtes d'alertes, graphes de modèles
virtuels/personnels ou middlewares et attributs d'événements sont acceptés. Les
cibles inconnues et corrections dupliquées sont refusées.

## Politique de démarrage et reprise

`XOLO_STORAGE_AUTO_MIGRATE` vaut `true` par défaut. La migration automatique
ne permet **pas** un déploiement progressif. Avec `false`, le démarrage et les
accès ultérieurs au store vérifient uniquement l'historique : une migration en
attente ou inconnue empêche le démarrage sans modifier le schéma.

Si la migration automatique bloque, maintenez les processus arrêtés et utilisez
la commande pour préparer, corriger, diagnostiquer et appliquer un plan. Un échec
transactionnel laisse l'ancienne base intacte. Après conversion réussie, revenir
à un ancien binaire nécessite de restaurer la sauvegarde précédente ; aucune
migration inverse des UUID n'est fournie. Les consommateurs externes stockant des
IDs Xolo doivent utiliser le mapping pour les actualiser. Les routes de provisioning
restent identiques dans cette étape ; leur remplacement fera l'objet d'une autre évolution.
