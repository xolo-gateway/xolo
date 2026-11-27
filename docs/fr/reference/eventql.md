# Syntaxe eventql

`eventql` est le langage de requête qui filtre les événements de
Xolo, dans l'explorateur (`/orgs/{slug}/events`) comme dans les règles d'alerte.
Sa syntaxe s'inspire de [LogQL](https://grafana.com/docs/loki/latest/query/) de
Grafana Loki : un **sélecteur d'étiquettes** suivi d'un **pipeline de filtres**
optionnel.

Le paquet de référence est `internal/core/eventql`.

```
{type="auth.login.failed", severity=~"warning|error"} | provider="oidc" |~ "timeout"
└──────────────── sélecteur ────────────────────────┘ └─── filtres du pipeline ───┘
```

---

## Modèle d'un événement

Chaque événement possède un ensemble de champs. Les champs **indexés** sont les
« étiquettes », interrogeables dans le sélecteur. Le reste se compose
d'**attributs** clé/valeur libres et du **message**.

| Champ       | Type        | Interrogeable via                | Description                                               |
| ----------- | ----------- | -------------------------------- | --------------------------------------------------------- |
| `type`      | étiquette   | sélecteur                        | Type pointé, ex. `auth.login.failed`, `proxy.request`     |
| `source`    | étiquette   | sélecteur                        | `platform` ou le nom du plugin émetteur                   |
| `severity`  | étiquette   | sélecteur                        | `info`, `warning` ou `error`                              |
| `org`       | étiquette   | sélecteur                        | ID de l'organisation (souvent déjà contraint par la page) |
| `user`      | étiquette   | sélecteur                        | ID de l'utilisateur concerné (vide = événement global)    |
| _attributs_ | clé/valeur  | filtre `\| clé …`                | Paires arbitraires propres à chaque type d'événement      |
| _message_   | texte libre | filtres de ligne `\|=`, `\|~`, … | Message lisible de l'événement                            |

> **Note.** Seules les cinq étiquettes ci-dessus sont autorisées dans le
> sélecteur. Toute autre clé y provoque une erreur de compilation. Les clés
> libres se filtrent dans le pipeline (`| clé="valeur"`).

---

## Sélecteur d'étiquettes

Le sélecteur est **obligatoire** dès qu'il y a une requête, et se place entre
accolades. Il contient zéro ou plusieurs matchers séparés par des virgules :

```
{type="proxy.request", user="c8p2..."}
```

Un sélecteur vide `{}`, ou une requête entièrement vide, correspond à **tous**
les événements du périmètre de visibilité courant.

### Opérateurs

| Opérateur | Signification                              | Exemple                   |
| --------- | ------------------------------------------ | ------------------------- |
| `=`       | égal                                       | `{type="proxy.request"}`  |
| `!=`      | différent                                  | `{severity!="info"}`      |
| `=~`      | correspond à l'expression régulière        | `{type=~"auth\\..*"}`     |
| `!~`      | ne correspond pas à l'expression régulière | `{source!~"plugin\\..*"}` |

Les valeurs sont **toujours entre guillemets doubles**.

---

## Pipeline de filtres

Après le sélecteur, on enchaîne des étages de filtrage. Il en existe deux
familles : les **filtres d'attributs** et les **filtres de ligne** (sur le
message). Ils se combinent et se répètent librement. Un événement doit les
satisfaire tous (ET logique).

### Filtres d'attributs

Ils commencent par une barre verticale `|`, suivie d'un nom d'attribut, d'un
opérateur et d'une valeur. Les opérateurs sont les mêmes que pour le sélecteur
(`=`, `!=`, `=~`, `!~`).

```
{type="proxy.request"} | model="gpt-4o" | provider=~"open.*"
```

> Un attribut **absent** est traité comme une chaîne vide. Ainsi
> `| model="gpt-4o"` exclut les événements sans attribut `model`, tandis que
> `| model!="gpt-4o"` les inclut (car `"" != "gpt-4o"`).

### Filtres de ligne (sur le message)

Ils s'appliquent au champ `message`. Contrairement aux filtres d'attributs, ils
ne sont **pas** précédés d'un nom.

| Opérateur | Signification                                         | Exemple           |
| --------- | ----------------------------------------------------- | ----------------- |
| `\|=`     | le message contient la sous-chaîne                    | `\|= "timeout"`   |
| `!=`      | le message ne contient pas la sous-chaîne             | `!= "success"`    |
| `\|~`     | le message correspond à l'expression régulière        | `\|~ "time.?out"` |
| `!~`      | le message ne correspond pas à l'expression régulière | `!~ "^ok"`        |

```
{type="auth.login.failed"} |~ "timeout|refused" != "retrying"
```

---

## Chaînes et échappements

Les valeurs sont entre guillemets doubles. Les séquences d'échappement
suivantes sont reconnues :

| Séquence | Résultat                  |
| -------- | ------------------------- |
| `\"`     | guillemet double littéral |
| `\\`     | antislash littéral        |
| `\n`     | saut de ligne             |
| `\t`     | tabulation                |

```
{type="proxy.request"} | prompt=~"say \"hello\""
```

---

## Expressions régulières

Les opérateurs `=~` / `!~` (étiquettes, attributs et lignes) utilisent le moteur
d'expressions régulières standard de Go
([RE2](https://github.com/google/re2/wiki/Syntax)).

- La correspondance est **non ancrée** : `=~"error"` correspond à toute valeur
  **contenant** `error`. Utilisez `^…$` pour une correspondance exacte.
- Une expression invalide fait échouer la compilation de la requête.
  L'explorateur affiche alors un message d'erreur.

```
{type=~"^proxy\\.request(\\.failed)?$"}
```

---

## Grammaire (EBNF simplifiée)

```
query        := selector pipeline?
selector     := '{' ( matcher (',' matcher)* )? '}'
matcher      := IDENT op STRING
op           := '=' | '!=' | '=~' | '!~'
pipeline     := stage*
stage        := lineFilter | labelFilter
lineFilter   := ('|=' | '|~' | '!=' | '!~') STRING
labelFilter  := '|' IDENT op STRING
```

- `IDENT` commence par une lettre ou `_`, suivi de lettres, chiffres, `_`, `.`,
  `-` ou `:`.
- `STRING` est une chaîne entre guillemets doubles (voir « Chaînes et
  échappements »).
- Une entrée vide compile vers une requête qui correspond à tout.

---

## Comment la requête est évaluée

Comme dans Loki, **les étiquettes sont indexées et le reste est scanné**.

- Les matchers d'**étiquettes** en `=` / `!=` et la **plage temporelle** sont
  traduits dans la requête SQL, sur des colonnes indexées, ce qui borne le
  jeu de lignes.
- Les **filtres d'attributs**, les **filtres de ligne** et **toute expression
  régulière** sont évalués en mémoire sur les lignes ainsi présélectionnées.

Cette même requête compilée sert à deux usages : le filtrage dans l'explorateur
et l'évaluation périodique des règles d'alerte (`count(fenêtre)` sur les
événements correspondants).

---

## Événements de base

### Événements de la plateforme

Émis par Xolo lui-même, avec `source="platform"`.

`proxy.request`, `proxy.request.failed`, `proxy.stream.interrupted` et
`auth.login.failed` sont émis dans le chemin de requête. Les
événements de **configuration** (CRUD sur les entités) sont émis par des
**décorateurs de stores**. Toute création, modification ou suppression produit
donc l'événement correspondant, quel que soit l'appelant (UI ou API), sans
code dupliqué dans les handlers.

**Activité et authentification**

| `type` | `severity` | Portée | Émis quand | Attributs |
|--------|-----------|--------|------------|-----------|
| `proxy.request` | `info` | utilisateur | Une requête proxy aboutit, y compris quand son flux est interrompu | `model`, `auth_token_id`, `prompt_tokens`, `completion_tokens`, `total_tokens` |
| `proxy.request.failed` | `warning` | utilisateur | Une requête proxy échoue (fournisseur, timeout, refus d'un hook). Un client qui annule sa requête n'en produit pas | `model`, `auth_token_id`, `status`, `error` |
| `proxy.stream.interrupted` | `warning` ou `info` selon la cause | utilisateur | Une réponse streamée s'arrête avant que le fournisseur signale la fin. Émis en plus de `proxy.request` | ceux de `proxy.request`, plus `cause`, `chunks_emitted`, `error` |
| `auth.login.failed` | `warning` | globale | Une connexion échoue (ex. compte déjà existant avec un autre fournisseur) | `email`, `provider`, `reason` |
| `pipeline.trace` | `info` (configurable) | utilisateur | Un nœud `trace` d'un pipeline s'exécute | `node_id`, `label`, `port.<nom>` pour chaque port connecté |
| `request.blocked` | `warning` | utilisateur | Un nœud `block` d'un pipeline refuse la requête | `node_id`, `label`, `reason` |

**Configuration (CRUD).** Ces événements sont tous globaux à l'organisation
(`user` vide). Ils portent les attributs `actor` et `actor_id` (l'utilisateur
qui a effectué l'action) en plus de ceux listés :

| Entité | `type` (created / updated / deleted) | Attributs |
|--------|--------------------------------------|-----------|
| Fournisseur | `provider.created` · `provider.updated` · `provider.deleted` | `provider_id`, `provider_name` |
| Modèle | `model.created` · `model.updated` · `model.deleted` | `model_id`, `model_name` |
| Modèle virtuel | `virtual-model.created` · `virtual-model.updated` · `virtual-model.deleted` | `virtual_model_id`, `virtual_model_name` |
| Middleware | `middleware.created` · `middleware.updated` · `middleware.deleted` | `middleware_id`, `middleware_name` |
| Rôle | `role.created` · `role.updated` · `role.deleted` | `role_id`, `role_name` |
| Application | `application.created` · `application.updated` · `application.deleted` | `application_id`, `application_name` |
| Jeton d'application | `application-token.created` · `application-token.deleted` | `token_id`, `label`, `application_id`, `application_name` |
| Invitation | `invite.created` · `invite.deleted` | `invite_id`, `role`, `email` |
| Membre | `member.added` · `member.updated` · `member.removed` | `membership_id`, `member_user_id` (+ `role_count` pour `updated`) |

> Sévérité : `info` pour `created`, `updated` et `added`, `warning` pour
> `deleted` et `removed`. Les rôles _builtin_ (owner/admin/member) ne produisent pas
> d'événement.

> Les événements de configuration étant globaux (`user` vide), ils ne sont
> visibles qu'avec la portée « Tous » ou « Globaux » de l'explorateur (permission
> `events:read:all`). Xolo ne les émet que pour des actions **lancées par un
> utilisateur**. Le seeding système, les migrations et les tests n'en génèrent
> pas.

### Événements des plugins

Émis par les plugins. L'hôte force `source` au nom du plugin et préfixe le `type`
par `plugin.<nom>.` (voir le tutoriel plugin, section _Emitting Events_).

| `type` | `severity` | Émis quand | Attributs |
|--------|-----------|------------|-----------|
| `plugin.pseudonymizer.sensitive-data.detected` | `warning` | Le plugin `pseudonymizer` détecte/pseudonymise des données sensibles | `entities`, `types` (ex. `EMAIL:2,PER:1`), `removed_attachments`, `language` |
| `plugin.pseudonymizer.sensitive-data.leak` | `error` | La vérification du plugin `pseudonymizer` détecte une fuite résiduelle après anonymisation (un événement par requête) | `leak_count`, `leak_<type>`, `action` : `reported` (la requête est partie pseudonymisée), `blocked` (refusée à cause de la fuite), `refused` (refusée pour un autre motif, rien n'a atteint le modèle) ou `passthrough` (partie en clair après une erreur). `leak_count` compte les valeurs distinctes de la requête, sauf avec `blocked`, où il compte les occurrences du contenu refusé |
| `plugin.pseudonymizer.attachment.blocked` | `warning` | Le plugin `pseudonymizer` refuse une requête portant une pièce jointe non pseudonymisable | `attachments`, `reasons` |
| `plugin.pseudonymizer.request.blocked` | `error` | Le plugin `pseudonymizer` refuse une requête en stratégie `hash` faute de clé HMAC exploitable sur le nœud (fail-closed) | `reason`, `error`, `strategy` |
| `plugin.pseudonymizer.index-key.unavailable` | `warning` | En stratégie `tag` ou `consistent`, le plugin `pseudonymizer` n'a pas pu obtenir la clé d'index du nœud (pas de magasin de secrets, lecture ou écriture du secret en échec, clé invalide). La requête est servie, mais ses jetons sont numérotés dans l'ordre d'apparition : un jeton repris d'un tour précédent, comme `⟦PERSON_1_…⟧`, peut alors être remplacé par la valeur d'une autre entité de la requête courante | `error`, `strategy` |
| `plugin.pseudonymizer.placeholders.neutralized` | `warning` / `info` | Le plugin `pseudonymizer` transforme des jetons de son nœud en texte inerte (`⟦X⟧` devient `[[X]]`), que rien ne remplacera par une valeur d'origine. En réponse (`warning`), ce sont des jetons inventés par le modèle, absents de la correspondance de la requête ; en requête (`info`), des jetons déjà présents dans le contenu envoyé, le plus souvent l'historique d'un tour précédent, neutralisés avant la pseudonymisation | `direction` (`response` ou `request`), `placeholders` |
| `plugin.pseudonymizer.passthrough` | `error` | Le plugin `pseudonymizer` laisse passer une requête **sans** la pseudonymiser : configuration invalide, modèle NER indisponible (hors-ligne, cache vide), échec d'anonymisation d'un contenu | `reason`, `error`, `contents` |
| `plugin.time-restriction.request.blocked` | `warning` | Le plugin `time-restriction` bloque une requête hors des plages autorisées | `reason` |

Pour cibler l'ensemble des événements de plugins : `{type=~"plugin\\..*"}` (ou
`{source!="platform"}`).

---

## Exemples

| Requête                                                    | Intention                                                          |
| ---------------------------------------------------------- | ------------------------------------------------------------------ |
| `{}`                                                       | Tous les événements visibles                                       |
| `{type="proxy.request"}`                                   | Toutes les requêtes proxy                                          |
| `{severity=~"warning\|error"}`                             | Tout ce qui n'est pas informatif                                   |
| `{type="auth.login.failed"} \| provider="oidc"`            | Échecs de connexion via OIDC                                       |
| `{source!="platform"}`                                     | Uniquement les événements émis par des plugins                     |
| `{type=~"provider\\..*"}`                                  | Créations, modifications et suppressions de fournisseurs           |
| `{type="proxy.request"} \| model=~"gpt-4.*" \|~ "timeout"` | Requêtes vers un modèle GPT‑4 dont le message mentionne un timeout |
| `{} \| actor_id="c8p2..."`                                 | Événements de configuration déclenchés par un acteur donné         |

### Portée des alertes

Une alerte a une **portée** qui détermine les événements qu'elle évalue :

| Portée | Évalue | Permission requise |
|--------|--------|--------------------|
| `org` | Tous les événements de l'organisation | `events:write` |
| `personal` | Uniquement les **propres** événements du créateur | `events:alerts:own` |

`org` et `personal` sont les valeurs enregistrées. La liste des alertes les
affiche sous les badges « org » et « perso ».

Xolo déduit la portée des droits de l'utilisateur à la création. Un utilisateur
qui dispose de `events:write` crée des alertes d'organisation. Sinon, avec
`events:alerts:own`, il crée des alertes personnelles sur ses propres
événements. Chacun ne voit et ne gère que les alertes et incidents qui le
concernent, et seul leur propriétaire voit les alertes personnelles. Consulter ses propres événements dans
l'explorateur ne requiert aucune permission particulière.

### Exemple d'usage dans une alerte

Règle « brute-force » : plus de 5 échecs de connexion sur 5 minutes.

```
Requête     : {type="auth.login.failed"}
Agrégation  : count
Fenêtre     : 5m
Condition   : > 5
Portée      : org (nécessite events:write)
```

Exemple d'alerte **personnelle** : un utilisateur veut être alerté au-delà de
100 requêtes proxy sur une heure (`count(1h) > 100` sur `{type="proxy.request"}`,
portée `personal`). L'alerte n'évalue que ses propres requêtes.
