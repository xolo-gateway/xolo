# Règles d'inscription

## Qu'est-ce qu'une règle d'inscription ?

Une règle d'inscription fait d'une identité un membre de votre organisation dès sa connexion, sans invitation ni action d'un administrateur. Elle associe une condition, sur le domaine de l'email ou sur un claim OIDC, à un rôle.

Une règle s'applique aussi aux jetons OIDC présentés directement à l'API : une application de discussion qui transmet le jeton de chaque personne n'a rien d'autre à faire.

## Accéder aux règles

1. Allez dans votre organisation : `/orgs/{slug}/`
2. Cliquez sur **Règles d'inscription** dans le menu admin

> **Note** : Vous devez disposer de la permission `invites:read` pour consulter les règles, et `invites:write` pour les gérer, sur l'organisation entière.

## Créer une règle

| Champ | Description |
|-------|-------------|
| **Nom** | Pour vous repérer dans la liste |
| **Condition** | Domaine de l'email ou claim OIDC |
| **Nom du claim** | Pour une condition sur un claim, par exemple `groups` |
| **Domaine ou valeur** | `example.org`, ou la valeur attendue du claim |
| **Rôle** | Rôle donné au nouveau membre : tout rôle de l'organisation, sauf Propriétaire |
| **Activée** | Une règle désactivée ne s'applique plus |

### Domaine de l'email

La règle ne s'applique qu'à un email que le fournisseur d'identité affirme vérifié (`email_verified`). Le domaine est comparé tel quel : `example.org` ne couvre pas `sub.example.org`.

### Claim OIDC

La valeur doit être égale à celle du claim, ou à l'une de ses valeurs pour une liste comme `groups`. Un booléen se compare à `true` ou `false`.

Les claims ne sont lus que pour un fournisseur qui prouve son émetteur : sur GitHub, le titulaire du compte écrit lui-même son profil. Les règles n'en tiennent compte que pour les fournisseurs OIDC et Gitea avec document de découverte, et Google. À une connexion interactive, les claims sont conservés dans le cookie de session : ceux qui n'y tiennent pas (1 Kio en tout, 32 valeurs par claim) sont ignorés, et un avertissement dans les journaux du serveur les nomme. Un jeton d'identité présenté à l'API n'est pas conservé et garde des claims plus grands (16 Kio, 256 valeurs par claim) ; au-delà, un claim que des règles comparent est réduit aux valeurs qu'elles comparent, et un avertissement nomme celui qui ne tient toujours pas.

Pour inscrire tous les comptes d'un fournisseur d'identité, utilisez le claim `iss` avec l'URL de son émetteur.

## Ce que fait une règle

Quand une identité correspond à une règle activée :

- elle devient membre de l'organisation, avec le rôle de la règle ;
- le compte que sa connexion crée est activé, même avec `XOLO_HTTP_AUTHN_ACTIVE_BY_DEFAULT=false` ; un compte inactif qui existait déjà le reste, car un administrateur a pu le désactiver ;
- l'événement `member.enrolled` est enregistré dans l'explorateur d'événements.

## Une règle agit une fois par compte

**Ce qu'un administrateur modifie ensuite prévaut sur la règle.** Un membre retiré de l'organisation n'y revient pas à sa connexion suivante, même par une autre règle de l'organisation, et un compte désactivé le reste.

Un membre existant garde son rôle et son statut, y compris suspendu : la règle ne le modifie pas et ne le réactive pas.

Une règle créée après coup s'applique aux comptes existants à leur prochaine connexion. Modifier une règle ne la rejoue pas pour les comptes déjà traités. Supprimer une règle conserve les membres qu'elle a inscrits, et l'organisation s'en souvient : une règle recréée ne fait pas revenir ceux qu'un administrateur a retirés.

## Limites

- Une règle modifiée peut mettre jusqu'à 30 secondes à s'appliquer sur les autres répliques.
- Un rôle personnalisé ne peut pas être supprimé tant qu'une règle l'attribue : modifiez ou supprimez d'abord la règle.
- Quand les membres ou les adhésions appartiennent au système de pilotage (`XOLO_OWNERSHIP`), les règles ne s'appliquent pas et leurs pages sont masquées.
- Avec plusieurs tenants, une règle ne concerne que le tenant de son organisation.
- Une règle qui échoue lors de la connexion qui crée un compte est retentée aux requêtes suivantes. Elle n'active plus ce compte passé une heure : un administrateur doit alors l'activer.

Les règles se gèrent aussi par l'[API de provisioning](../../provisioning/provisioning.md#ressources-metier).
