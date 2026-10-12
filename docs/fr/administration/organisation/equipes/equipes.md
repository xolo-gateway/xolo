# Équipes

## Qu'est-ce qu'une équipe ?

Une équipe regroupe des membres de l'organisation, les modèles et les middlewares qu'ils utilisent, et le budget qu'ils consomment. Un membre peut appartenir à plusieurs équipes.

Un rôle dit ce qu'un membre peut faire ; une équipe dit avec quoi. L'accès aux modèles passe par les équipes : un membre utilise un modèle si l'une de ses équipes y donne accès. Les permissions `model:use:org` et `model:use:virtual` restent le seul accès qui ne passe pas par une équipe : elles ouvrent tous les modèles de l'organisation, et le rôle intégré **Membre** les porte.

Les équipes ne s'imbriquent pas.

## Choisir l'équipe débitée

Chaque requête est attribuée à une seule équipe, ou à l'organisation seule. Le nom du modèle porte l'équipe : `<organisation>/<équipe>/<modèle>`, par exemple `acme/juridique/gpt-4o`. Un membre de plusieurs équipes choisit l'équipe débitée en choisissant le modèle, conversation par conversation, dans n'importe quel client (OpenWebUI, les SDK OpenAI et Anthropic, Claude Code).

- `/v1/models` liste un nom par équipe donnant accès au modèle, sous la forme qualifiée. Les membres qui ont `model:use:org` voient aussi la forme courte `acme/gpt-4o`, débitée sur l'organisation seule.
- La forme courte `acme/gpt-4o` reste acceptée quand un seul chemin existe : le jeton est rattaché à une équipe, une seule des équipes du membre donne accès au modèle, ou le membre a `model:use:org` (seule l'organisation est alors débitée). Le même membre qui nomme une de ses équipes, par exemple `acme/legal/gpt-4o`, débite cette équipe, même si elle ne donne pas accès au modèle. Sinon, la passerelle répond `400` avec le code `model_ambiguous` et la liste des noms qualifiés utilisables.
- Une clé d'API ou une application rattachée à une équipe débite toujours cette équipe, même quand son détenteur a `model:use:org`, et ne peut pas nommer une autre équipe.
- Un modèle virtuel personnel (`~/…`) débite l'équipe choisie dans ses réglages. Si son auteur quitte cette équipe, si elle est supprimée ou si elle appartient à une autre organisation que celle de la requête, ses requêtes sont refusées (`400 model_team_required`) jusqu'à ce qu'il en choisisse une autre.

L'identifiant d'une équipe peut être modifié sur sa page, avec les mêmes règles qu'à la création. Les clients enregistrent les noms de modèles dans leur configuration : ceux qui portent l'ancien identifiant ne fonctionnent plus et doivent être mis à jour. Les jetons, applications, modèles virtuels personnels et budgets rattachés à l'équipe la suivent sans changement.

> **Conflit de noms.** Un nom de modèle peut contenir une barre oblique, comme un modèle vLLM déclaré sous son identifiant Hugging Face (`meta/llama-3`). Xolo refuse une équipe dont l'identifiant commencerait un nom de modèle existant (`meta`), et un nom de modèle qui commencerait par l'identifiant d'une équipe suivi de `/`.

## Créer une équipe

1. Allez dans votre organisation et cliquez sur **Équipes** dans le menu **Gouvernance**.
2. Cliquez sur **Nouvelle équipe**.
3. Renseignez le nom, l'identifiant (minuscules, chiffres et tirets) et une description.
4. Cliquez sur **Créer l'équipe**.

> **Note** : Il faut la permission `teams:write` pour créer une équipe ou changer ce qu'elle utilise.

## Donner accès à des modèles et des middlewares

Sur la page de l'équipe, la section **Modèles accessibles** liste les modèles réels et virtuels de l'organisation. Cochez ceux que l'équipe peut utiliser, puis cliquez sur **Enregistrer**.

Un modèle virtuel délègue l'accès aux modèles que son pipeline référence dans la configuration de ses nœuds : une équipe qui a accès au modèle virtuel peut utiliser ces modèles à travers lui, sans y avoir accès directement. Un nom de modèle calculé pendant la requête (par un plugin ou un nœud valeur) est, lui, vérifié contre l'accès de l'équipe. Voir [Modèles virtuels](../virtual_model/virtual_model.md).

La section **Middlewares de l'équipe** rattache des middlewares à l'équipe. Un middleware rattaché à au moins une équipe ne s'applique plus qu'aux requêtes de ces équipes, après les middlewares de l'organisation, qui portent les règles de conformité et s'appliquent à toutes les requêtes. Un middleware qui n'est rattaché qu'à cette équipe ne peut pas en être décoché : sans équipe, il s'appliquerait à toute l'organisation. Rattachez-le d'abord à une autre équipe, ou supprimez-le.

## Gérer les membres

La section **Membres** liste les membres de l'équipe. Choisissez un membre de l'organisation et cliquez sur **Ajouter à l'équipe**. L'icône de retrait retire un membre de l'équipe et supprime son budget dans l'équipe ; il reste membre de l'organisation.

### Rôles dans l'équipe

Un rôle attribué à un membre depuis la page de l'équipe ne vaut que pour cette équipe. Seules ses permissions marquées « équipe » dans l'éditeur de rôles s'appliquent :

| Permission | Effet sur l'équipe |
| --- | --- |
| `members:read`, `members:write` | Voir et gérer les membres de l'équipe et leurs rôles dans l'équipe |
| `quota:read`, `quota:write` | Voir le budget de l'équipe et le répartir entre ses membres |
| `usage:read` | Voir l'usage des requêtes de l'équipe |
| `events:read:all` | Voir les événements des membres de l'équipe |
| `invites:read`, `invites:write` | Inviter des personnes qui rejoignent l'organisation et l'équipe, comme membres |

Un rôle « Responsable d'équipe » portant `members:write`, `quota:write` et `usage:read`, attribué sur l'équipe juridique, permet ainsi de gérer cette équipe seulement.

L'organisation alloue, l'équipe répartit : un responsable d'équipe ne peut jamais élargir ce que l'équipe utilise ni son enveloppe. Il ne peut attribuer que des rôles dont il détient lui-même les permissions sur l'équipe.

## Budget de l'équipe

Le budget d'une organisation, d'une équipe et d'un membre dans une équipe sont vérifiés à chaque requête : une requête de l'équipe juridique est refusée dès que l'un des niveaux est atteint.

- **Enveloppe de l'équipe.** Il faut `quota:write` sur l'organisation pour la fixer.
- **Répartition.** Il faut `quota:write`, sur l'organisation ou sur l'équipe, pour fixer le budget d'un membre dans l'équipe (icône crayon sur la ligne du membre), choisir la règle de répartition et le partage à parts égales.

L'option **Partager l'enveloppe à parts égales** divise le budget de l'équipe entre ses membres qui n'ont pas de budget propre dans l'équipe.

La **répartition** contraint les budgets des membres à leur enregistrement :

| Règle | Effet |
| --- | --- |
| **Libre** | Aucune contrainte : seules les limites vérifiées à chaque requête s'appliquent. |
| **Plafonnée** | Le budget de chaque membre est au plus égal à celui de l'équipe. Leur somme peut le dépasser. |
| **Partagée** | La somme des budgets des membres ne dépasse pas celui de l'équipe. |

La règle vaut dans les deux sens : baisser le budget de l'équipe est refusé si les budgets des membres n'y tiennent plus.

La même règle se règle sur le budget de l'organisation, pour les enveloppes de ses équipes. Voir [Budgets et quotas](../../../concepts/budgets-et-quotas.md).

## Clés d'API et applications

- Une **clé d'API** peut être rattachée à une de vos équipes à sa création, depuis **Profil > Jetons API**. Si vous quittez l'équipe ou si elle est supprimée, la clé est refusée et la liste l'indique (« équipe quittée ou supprimée ») : supprimez-la et créez-en une autre. Une clé ne change jamais d'équipe d'elle-même.
- Une **application** peut être rattachée à une équipe depuis sa page : elle utilise alors les modèles de l'équipe et toutes ses requêtes sont débitées sur l'équipe.
- Un **modèle virtuel personnel** choisit l'équipe qu'il débite dans ses réglages. Il ne délègue rien : chaque modèle qu'il appelle doit être accessible à cette équipe.

Une équipe à laquelle des applications sont rattachées ne peut pas être supprimée : rattachez-les ailleurs d'abord. Il en va de même tant qu'un middleware n'est rattaché qu'à elle : sans équipe, il s'appliquerait à toute l'organisation. Rattachez-le à une autre équipe, ou supprimez-le.

## Permissions

| Action | Permission requise |
| --- | --- |
| Voir les équipes | `teams:read` |
| Créer, modifier, supprimer une équipe, choisir ses modèles et middlewares | `teams:write` |
| Gérer les membres d'une équipe | `members:write`, sur l'organisation ou l'équipe |
| Fixer l'enveloppe d'une équipe | `quota:write` sur l'organisation |
| Répartir le budget d'une équipe | `quota:write`, sur l'organisation ou l'équipe |

## Migration depuis les accès restreints des rôles

Avant les équipes, un rôle personnalisé pouvait autoriser des modèles précis. La mise à jour transforme chaque rôle personnalisé qui autorisait des modèles en une équipe : mêmes modèles, et pour membres les détenteurs du rôle. Une application qui détenait un tel rôle est rattachée à son équipe ; une application qui en détenait plusieurs reçoit une équipe propre réunissant leurs modèles. Personne ne perd d'accès. Les rôles gardent leurs permissions.

Un membre qui détenait plusieurs de ces rôles appartient désormais à plusieurs équipes : si deux d'entre elles donnent accès au même modèle, la forme courte du nom devient ambiguë et il doit nommer l'équipe.

> **Note** : Arrêtez les autres instances de Xolo pendant la mise à jour. La migration supprime la table des accès des rôles, qu'une instance plus ancienne lit encore. Elle ne peut pas être annulée : sauvegardez la base avant la mise à jour pour pouvoir revenir à la version précédente.
