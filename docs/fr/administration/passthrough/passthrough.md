# Relais de credential (*passthrough*)

Le relais permet à un client qui **détient déjà son propre credential amont** de faire transiter son trafic par Xolo pour en obtenir la mesure, sans confier de clé de provider à la passerelle.

C'est l'inverse du proxy LLM habituel :

| | Proxy `/api/v1/` | Relais `/passthrough/` |
|---|---|---|
| Credential amont | détenu par Xolo (provider chiffré) | fourni par le client à chaque requête |
| Choix du modèle | routé par Xolo (modèles virtuels, pipelines) | décidé par le client |
| Corps de requête | transformé par les hooks | relayé verbatim |
| Apport de Xolo | routage, transformation, quotas, mesure | identité, autorisation, mesure |

Le cas qui a motivé la fonctionnalité : un client type CLI agentique place son credential dans l'en-tête `Authorization` et n'offre aucun moyen de le déplacer. Sans relais, il faut donner à Xolo une clé API distincte ; avec le relais, l'utilisateur garde son credential et le trafic apparaît quand même dans les tableaux de bord.

> **Désactivé par défaut.** L'activer signifie que l'instance transmet à un tiers des credentials qu'elle ne possède pas. C'est une décision explicite d'exploitant.

## Configuration

| Variable | Défaut | Rôle |
|---|---|---|
| `XOLO_PASSTHROUGH_ENABLED` | `false` | Active la surface |
| `XOLO_PASSTHROUGH_MOUNT_PREFIX` | `/passthrough/` | Chemin servi ; c'est l'URL de base côté client |
| `XOLO_PASSTHROUGH_UPSTREAM_BASE_URL` | `https://api.anthropic.com` | Origine de destination. `https` obligatoire hors boucle locale |
| `XOLO_PASSTHROUGH_CREDENTIAL_HEADER` | `X-Xolo-Key` | En-tête portant le jeton Xolo |
| `XOLO_PASSTHROUGH_PROVIDER_ID` | — | Provider auquel la consommation est imputée. **Requis** |
| `XOLO_PASSTHROUGH_ALLOWED_PATHS` | `/v1/messages` | Chemins amont autorisés, séparés par des virgules |

`ALLOWED_PATHS` est une liste blanche : activer le relais pour un endpoint n'ouvre pas le reste de l'API amont.

Le relais n'a pas de réglage de timeout propre : il réutilise `XOLO_PROXY_UPSTREAM_TIMEOUT`, qui borne déjà les appels amont du chemin proxifié. Sur cette surface il plafonne l'attente des en-têtes de réponse, jamais la réponse elle-même — un appel agentique streame aussi longtemps que l'amont parle.

## Les deux credentials

Une requête relayée en porte deux, et ils se disputent le même en-tête :

- le **credential amont**, dans `Authorization`, que le client veut voir relayé ;
- le **jeton Xolo**, qui identifie l'appelant auprès de la passerelle.

Le client ne pouvant pas déplacer le sien, c'est le jeton Xolo qui voyage dans `XOLO_PASSTHROUGH_CREDENTIAL_HEADER`. Un middleware les intervertit avant la chaîne d'authentification, qui fonctionne donc sans modification, et le credential amont est remis en place juste avant l'émission vers l'amont. Le jeton Xolo est retiré de la requête sortante : il n'a aucun sens en amont, et le transmettre reviendrait à donner à un tiers un credential valide sur l'instance.

Une requête sans jeton Xolo est rejetée en 401, même si elle porte un credential amont valide : relayer un appel non identifié n'aurait pas de sens puisque personne ne pourrait en être débité.

## Sonde de connectivité

Le client teste `GET|HEAD <base>/api/hello` **sans credential** avant tout appel, et ne va pas plus loin tant qu'elle échoue. Cette route est donc montée hors de la chaîne d'authentification et répond `200` sans rien divulguer d'autre que la présence d'un relais.

## Mesure

La consommation est extraite de la réponse au fil de l'eau, sans mise en tampon : rien n'est retardé côté client. Les deux formes de réponse sont traitées — le flux SSE et la réponse JSON unique que les clients réémettent après un échec de streaming.

Un enregistrement d'usage est écrit avec les mêmes champs que le trafic proxifié, ce qui le fait apparaître dans les mêmes tableaux de bord, quotas et exports. Le coût est calculé depuis le tarif du modèle **tel qu'il est enregistré sous le provider configuré** : si le modèle amont n'y est pas déclaré, l'appel est compté en jetons mais avec un coût nul — visiblement non tarifé plutôt que silencieusement faux. Déclarer le modèle sous le provider suffit à activer la tarification.

Les appels en échec (statut ≥ 400) ne sont pas enregistrés : ils ne sont pas facturés en amont, et les compter gonflerait le registre d'une consommation jamais débitée.

## Configuration client

```bash
export ANTHROPIC_BASE_URL="https://xolo.example.com/passthrough"
export ANTHROPIC_CUSTOM_HEADERS="X-Xolo-Key: <clé-api-xolo>"
```

Le credential amont reste celui que le client gère lui-même ; Xolo n'y touche pas.

## Limites connues

- **La sonde suppose un chemin relatif à l'URL de base.** Si un client la demande à la racine de l'origine, monter le relais sur un sous-domaine dédié plutôt que sur un préfixe de chemin.
- **Le credential amont transite par la passerelle.** Il est relayé sans être journalisé ni stocké, mais il traverse le processus : le relais n'a de sens que sur une instance dont l'exploitant est de confiance pour ses utilisateurs.
- **Aucun hook de pipeline ne s'exécute.** Le corps étant relayé verbatim, il n'y a ni modèle virtuel ni nœud de pipeline — donc **pas de pseudonymisation**. Une instance qui route via des modèles virtuels précisément pour ce nœud ne doit pas exposer cette surface à ses utilisateurs.
- **Aucun quota n'est appliqué.** La consommation est mesurée après coup ; l'enforcement du proxy ne s'y applique pas, faute de décision de routage sur laquelle s'accrocher.
- **Le relais dépend d'un comportement client non documenté** — la transmission du credential vers une URL de base arbitraire. Rien ne garantit qu'il survive à une version amont.
