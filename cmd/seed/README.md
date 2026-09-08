# `cmd/seed` — jeu de données E2E

Génère une base SQLite Xolo peuplée de données factices mais cohérentes, destinée
aux tests end-to-end : identifiants stables, jetons d'API en clair, historique
d'usage calculé avec la même formule que le hook de facturation.

## Utilisation

```bash
make seed                              # génère ./e2e.sqlite (30 jours d'historique)
make seed SEED_DSN=/tmp/x.sqlite SEED_DAYS=90

go run ./cmd/seed -dsn e2e.sqlite -force -days 30 -seed 20260731
```

Options : `-dsn`, `-days`, `-seed` (graine du PRNG), `-secret-key`, `-force`
(supprime la base existante et ses fichiers `-wal`/`-shm`), `-verbose`.

Démarrage du serveur sur la base générée :

```bash
XOLO_STORAGE_DATABASE_DSN=e2e.sqlite \
XOLO_SECRET_KEY=0e2ec2e6d5aa74c1b96c65d1b4a0f4d9ad48c6b1f3ff7a1c5f0f5f2ae4c1d3b7 \
XOLO_HTTP_ADDRESS=:3002 XOLO_HTTP_BASE_URL=http://localhost:3002 \
XOLO_HTTP_SESSION_KEYS=abcdefghijklmnopqrstuvwxyz000002 \
bin/server
```

La `XOLO_SECRET_KEY` doit être identique à celle du seed : les clés d'API des
fournisseurs sont chiffrées en AES-GCM avec cette clé.

## Déterminisme

Tous les identifiants sont écrits à la main (`org-acme`, `usr-alice`,
`mdl-acme-gpt4o`, `usg-000001`…) et les jetons d'API sont des constantes, donc
les assertions E2E peuvent viser des valeurs et des URL stables. Le volume et la
répartition de l'historique d'usage dérivent de la graine `-seed`.

Seuls deux éléments varient d'une exécution à l'autre : les horodatages, ancrés
sur l'instant de génération, et les identifiants des rôles *builtin*
(owner/admin/member), créés par `EnsureBuiltinRoles` pour rester alignés sur le
catalogue de permissions courant. Retrouvez-les via `(org_id, builtin_kind)`.

## Contenu

### Organisations

| ID | Slug | Devise | État | Particularité |
|---|---|---|---|---|
| `org-acme` | `acme` | EUR | actif | multi-fournisseurs, quotas, pipelines |
| `org-globex` | `globex` | USD | actif | facturation à l'abonnement, quota partagé équitablement |
| `org-initech` | `initech` | USD | **inactif** | tests de rejet d'accès |

### Utilisateurs

Tous en `provider=local`, `subject` = prénom (pour un IdP de test).

| Email | Rôle | Particularité |
|---|---|---|
| `root@xolo.test` | admin plateforme (`admin`) | propriétaire d'Initech |
| `alice@acme.test` | propriétaire Acme | préférence thème sombre |
| `bob@acme.test` | administrateur Acme | |
| `carol@acme.test` | membre Acme **et** Globex | modèle virtuel personnel |
| `dave@acme.test` | membre Acme + rôle custom « Analyste » | quota journalier volontairement dépassé |
| `erin@globex.test` | propriétaire Globex | |
| `frank@globex.test` | membre Globex | **compte désactivé** |

Le rôle custom `role-acme-analyst` ne porte que `usage:read` + `model:use:org`
et n'accorde l'accès qu'à `gpt-4o-mini` et `mistral-small`.

### Applications (comptes de service)

- `app-acme-ci` — « CI Pipeline », active, rôle `member`.
- `app-globex-bot` — « Support Bot », **désactivée** (toute requête doit être rejetée).

### Jetons d'API

Les valeurs ci-dessous sont celles à envoyer (`Authorization: Bearer …` ou
formulaire `/auth/token/login`). En base, la colonne `value` contient leur
hachage SHA-256, comme pour tout jeton créé par l'application.

| Valeur | Porteur | Organisation | État |
|---|---|---|---|
| `xolo-e2e-alice-acme` | alice | acme | valide, sans expiration |
| `xolo-e2e-carol-acme` | carol | acme | valide, expire dans 1 an |
| `xolo-e2e-carol-globex` | carol | globex | valide |
| `xolo-e2e-dave-expired` | dave | acme | **expiré** (→ 401) |
| `xolo-e2e-erin-globex` | erin | globex | valide |
| `xolo-e2e-app-acme-ci` | application CI | acme | valide |
| `xolo-e2e-app-globex-bot` | application bot | globex | application désactivée (→ 401) |

### Fournisseurs et modèles

| Fournisseur | Type | Devise | Facturation | État |
|---|---|---|---|---|
| `prov-acme-openai` | openai | USD | payg (retry + rate limit configurés) | actif |
| `prov-acme-mistral` | mistral | EUR | payg | actif |
| `prov-acme-local` | openai (Ollama) | EUR | payg | **désactivé** |
| `prov-globex-plan` | openai | USD | **abonnement** (fenêtre 5 h + concurrence) | actif |

Modèles : `acme/gpt-4o`, `acme/gpt-4o-mini`, `acme/mistral-small` (avec
`extra_body`), `acme/text-embedding-3-small` (embeddings), `globex/claude-sonnet`,
`globex/claude-haiku` (**désactivé**). Les tarifs sont exprimés en microcents par
1 000 tokens et reflètent les grilles publiques.

### Pipelines

- `vm-acme-smart-router` — modèle virtuel `acme/smart-router` (générateur → modèle → sortie).
- `mw-acme-guardrails` — middleware actif, priorité 10, appliqué à **tous** les modèles (nœud modèle en *passthrough*).
- `mw-acme-translate` — middleware désactivé ciblant uniquement `gpt-4o`.
- `pvm-carol-notes` — modèle virtuel personnel de Carol (`~/notes-summarizer`).

### Quotas

Calibrés sur l'historique généré pour que les jauges affichent ~30 % de
consommation, sauf celui de Dave, volontairement saturé.

| Portée | Cible | Journalier | Mensuel |
|---|---|---|---|
| org | acme | 0,40 € | 12 € |
| org | globex | — | 30 $ |
| user | carol | — | 0,60 € |
| user | dave | **0,001 €** (dépassé) | 2 € |
| application | app-acme-ci | 0,06 € | 1,50 € |

### Invitations

- `inv-acme-open` — utilisable : `/join/inv-acme-open` (10 usages, 2 consommés).
- `inv-acme-grace` — liée à `grace@acme.test`, rôle admin.
- `inv-acme-expired` — expirée.
- `inv-globex-revoked` — révoquée.
- `inv-globex-used` — épuisée (`max_uses` atteint).

### Usage

Un enregistrement par requête simulée, réparti sur `-days` jours et sur les
heures ouvrées, avec une part de trafic week-end pour les comptes de service.
Le coût est calculé exactement comme dans `internal/adapter/proxy/usage_tracker.go`
(tokens non cachés / cachés / complétion), puis converti dans la devise de
l'organisation via les taux figés insérés dans `exchange_rates` (USD→EUR 0,92).
Les requêtes servies par le fournisseur à l'abonnement portent `plan_covered=1`
et conservent leur coût PAYG équivalent dans `provider_cost`.

### Événements et alertes

Événements de plateforme (création de fournisseur/modèle/invitation), une rafale
de 25 échecs d'authentification sur les dernières heures, du trafic proxy
courant, un événement épinglé rattaché à un incident.

Alertes : `alr-acme-auth-failures` (**firing**, avec un incident ouvert et un
incident résolu), `alr-acme-errors` (OK), `alr-carol-personal` (portée
personnelle, désactivée). L'organisation Acme limite sa rétention à 5 000
événements.
