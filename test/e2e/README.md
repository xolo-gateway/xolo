# `test/e2e` — tests de bout en bout

Suite qui pilote un **vrai serveur Xolo** par HTTP, comme le ferait un client,
face à un **fournisseur factice** aux réponses prévisibles. Elle valide les
comportements qui traversent toute la chaîne (authentification, middlewares,
sous-processus plugin, appel amont, événements) et qu'aucun test unitaire ne
peut couvrir seul.

```bash
make test-e2e                 # équivalent : go test -tags e2e ./test/e2e/...
```

Le tag de build `e2e` la tient à l'écart de `go test ./...`. Le job `e2e` du
workflow GitHub `check` l'exécute à chaque push et pull request.

## Fonctionnement

`TestMain` prépare l'environnement une fois pour tout le binaire de test :

1. compile `cmd/server` et `plugins/pseudonymizer` depuis l'arbre de travail,
   dans un répertoire temporaire ;
2. génère la base SQLite avec `cmd/seed` (voir `cmd/seed/README.md` pour le
   catalogue : organisations, jetons, modèles…) ;
3. démarre le fournisseur factice (`httptest`), qui expose
   `POST /v1/chat/completions` et répond `Bien reçu : <dernier message
   utilisateur>` ; chaque requête reçue est conservée pour les assertions ;
4. pointe le fournisseur `prov-acme-openai` de la base sur ce faux serveur et
   ajoute les middlewares sous test ;
5. lance le serveur sur un port libre, avec la clé secrète du seed, et attend
   qu'il réponde sur `/api/v1/models`.

Les tests interrogent ensuite `/api/v1/chat/completions` avec un jeton du seed,
inspectent ce que le fournisseur a reçu, ce que le client a obtenu, et les
événements enregistrés en base (lecture directe de la SQLite).

En cas d'échec de la mise en place, le journal du serveur est imprimé.

## Scénarios couverts

### Nœuds intégrés (`nodes_test.go`)

| Test | Modèle virtuel | Nœuds | Attendu |
|---|---|---|---|
| `TestNodes_LogicPipeline` | `acme/e2e-logic` | `value`, `math`, `compare`, `select`, `model_ref`, `sample`, `context`, `trace`, `model` | `max(0,2 ; 0,9) > 0,5` appelle le modèle fort ; un `sample` à 0 % ne sélectionne jamais ; l'événement `pipeline.trace` porte chaque valeur intermédiaire et l'identifiant de l'appelant |
| `TestNodes_ModelFallback` | `acme/e2e-fallback` | `model_fallback` | le fournisseur reçoit d'abord le modèle cassé (503), puis le modèle de secours ; le client obtient une réponse normale |

### Plugins (`plugins_test.go`)

| Test | Modèle virtuel | Plugins | Attendu |
|---|---|---|---|
| `TestPlugins_ComplexityRouting` | `acme/e2e-router` | `complexity-scorer` + `compare` / `select` / `model_ref` / `trace` | une salutation part vers le modèle rapide, une demande d'analyse contrainte vers le modèle fort ; la trace enregistre le score et le nom retenu |
| `TestPlugins_SystemPrompt` | `acme/e2e-system-prompt` | `system-prompt` | le fournisseur reçoit le message système en tête, le message utilisateur intact |
| `TestPlugins_DummyModel` | `acme/e2e-dummy` | `dummy-model` | réponse forgée depuis le gabarit, aucun appel au fournisseur |
| `TestPlugins_TimeRestriction` | `acme/e2e-open`, `acme/e2e-closed` | `time-restriction` | 200 dans la plage, 403 hors plage avec événement `request.blocked` ; la plage fermée vise un jour qui n'est pas aujourd'hui (UTC) |
| `TestPlugins_PromptGuard` | `acme/e2e-guard` | `prompt-guard` | injection flagrante refusée (403) sans appel amont, question honnête servie |
| `TestPlugins_Telemetry` | `acme/e2e-telemetry` | `request-inspector`, `text-classifier`, `energy-estimator`, `budget-pressure`, `script-processor` → `trace` | nombre de messages, catégorie `code` par règle, énergie > 0, budget présent (jeton de Carol), doublement par script Tengo |

### Pseudonymizer (`pseudonymizer_test.go`)

| Test | Modèle | Attendu |
|---|---|---|
| `TestPseudonymizer_TagStrategy_RoundTrip` | `acme/gpt-4o-mini` | le fournisseur ne voit que des jetons `⟦PERSON_1_…⟧`, la réponse restitue les noms, événement `sensitive-data.detected` avec `types=LOC:1,PER:1` |
| `TestPseudonymizer_NoPersonalData_Passthrough` | `acme/gpt-4o-mini` | message transmis tel quel, aucun événement de détection |
| `TestPseudonymizer_HashWithoutKey_FailsClosed` | `acme/gpt-4o` | 403, le fournisseur ne reçoit rien, événement `request.blocked` avec `reason=hash_key_missing` |

Le pseudonymizer est branché par deux middlewares (`mw-e2e-pseudo-tag`,
`mw-e2e-pseudo-hash`) enveloppant chacun un seul modèle ; les autres scénarios
passent par des modèles virtuels dédiés, tous déclarés dans `fixtures_test.go`
avec un petit constructeur de graphes (`newGraph().generator("gen")…`).

Le harnais ajoute trois modèles au fournisseur factice : `acme/e2e-fast`,
`acme/e2e-strong`, et `acme/e2e-broken` auquel le faux serveur répond toujours
503. Les plugins `mcp-bridge` (serveur MCP requis), `llm-classifier` (réponse
JSON structurée d'un modèle requise) et `fuzzy-evaluator` ne sont pas couverts.

## Ajouter un scénario

- Réutiliser `chat(t, jeton, modèle, texte)`, `env.provider.Requests()`,
  `waitForEvent` et `assertNoEvent` ; les événements étant écrits en
  arrière-plan, toujours passer par ces deux dernières plutôt que lire la
  base immédiatement.
- Un nouveau pipeline se déclare dans `virtualModelGraphs()` sous un nouveau
  nom `acme/e2e-…` ; un comportement de middleware s'ajoute dans
  `createMiddlewares`, ciblé sur un modèle du seed encore libre
  (`acme/mistral-small` par exemple) pour ne pas interférer avec les scénarios
  existants.
- Choisir des phrases de test sans entité nommée quand on veut un passe-plat :
  le modèle NER reconnaît légitimement « France » comme un lieu.
- `XOLO_E2E_KEEP=1` conserve le répertoire de travail (binaires, base,
  `server.log`) et affiche son chemin, pour analyser un échec.

## Prérequis

- Go, et l'accès réseau lors de la première exécution : le plugin télécharge le
  modèle NER français (~70 Mio) dans `~/.cache/go-anon`. Le workflow met ce
  répertoire en cache.
- Aucun Docker, aucune clé d'API réelle : les clés du seed sont factices et le
  fournisseur factice ignore l'en-tête `Authorization`.
