# API de provisioning

L'API utilise un listener HTTPS dédié, distinct du proxy public `/v1/`.
Elle expose le manifeste et les cinq PUT du contrat App Covenant
`0.1.0-draft.1`, ainsi que les lectures communes, ETags, préconditions, listes
et flux de synchronisation. Elle propose aussi des webhooks durables
facultatifs, la gestion des identités, la révocation de sessions, la propriété
et l'adoption. Cette version ne revendique pas une conformité complète au contrat.

## Configuration et certificats

| Variable | Défaut | Description |
| --- | --- | --- |
| `XOLO_PROVISIONNING_API_ENABLED` | `false` | Active le listener dédié |
| `XOLO_PROVISIONNING_API_ADDRESS` | `:3003` | Adresse d'écoute |
| `XOLO_PROVISIONNING_API_TLS_CERT_FILE` | — | Certificat serveur PEM |
| `XOLO_PROVISIONNING_API_TLS_KEY_FILE` | — | Clé privée serveur PEM |
| `XOLO_PROVISIONNING_API_TLS_CLIENT_CA_FILE` | — | Autorité de certification des clients |
| `XOLO_PROVISIONNING_API_AUTHORIZED_URIS` | — | Liste obligatoire d'URI absolues, séparées par des virgules |
| `XOLO_PROVISIONNING_API_RATE_LIMIT` | `10` | Requêtes par seconde et par URI autorisée |
| `XOLO_PROVISIONNING_API_RATE_BURST` | `20` | Rafale maximale par URI |
| `XOLO_PROVISIONNING_API_SHUTDOWN_TIMEOUT` | `10s` | Délai d'arrêt gracieux |

TLS 1.3 est obligatoire. Le certificat client doit être valide, signé par une
CA configurée et contenir exactement une URI SAN correspondant exactement à la
liste d'autorisation. Le CN, les DNS SAN, cookies, tokens utilisateur et
certificats transmis par en-têtes ne donnent aucun droit. Une URI autorisée
administre toute l'instance, y compris les ressources suspendues.

Exemple d'extension de certificat :

```ini
extendedKeyUsage=clientAuth
subjectAltName=URI:urn:example:console
```

Configurer alors `XOLO_PROVISIONNING_API_AUTHORIZED_URIS=urn:example:console`.
Les erreurs de certificat interrompent la négociation TLS ; le contrôle HTTP
de défense renvoie `403 client_certificate_rejected`.

Les budgets sont locaux à chaque réplique et remis à zéro au redémarrage.
Une limite atteinte renvoie `429 rate_limited`, sans mutation, avec un
`Retry-After` entier positif en secondes. Les clients doivent espacer leurs
réessais avec une part aléatoire.

`X-Request-ID` accepte une seule valeur de 32 caractères hexadécimaux minuscules.
Sinon, le serveur en génère une sans journaliser la valeur rejetée. La valeur
retenue accompagne toutes les réponses HTTP, les logs et les audits. Elle ne
constitue pas une clé d'idempotence.

## Routes communes

`GET /v1/manifest` renvoie uniquement `name`, `version` (version de Xolo) et
`contract_version`. Le client doit le relire avant les écritures et après une
mise à jour ou une reconnexion.

| PUT | Champs |
| --- | --- |
| `/v1/tenants/{tenantID}` | `slug`, `name`, `status` |
| `/v1/tenants/{tenantID}/domains/{hostname}` | `status` |
| `/v1/tenants/{tenantID}/organizations/{organizationID}` | `slug`, `name`, `status` |
| `/v1/tenants/{tenantID}/members/{memberID}` | `email`, `display_name` facultatif, `tenant_role`, `status` |
| `/v1/tenants/{tenantID}/organizations/{organizationID}/members/{memberID}` | `role`, `status` |

Les UUID sont fournis par le client, sous forme canonique en minuscules. Le rôle
de tenant est `owner` ou `member` ; celui d'adhésion est `owner`, `admin` ou
`member`. Le statut est `active` ou `suspended`. Tous les champs sauf
`display_name` et l’extension objet `identity` sont obligatoires. Omettre `display_name` le vide.

Les PUT attendent un seul objet JSON, avec `Content-Type: application/json`
(paramètres valides acceptés). Les champs inconnus, types incorrects, JSON
supplémentaire, UTF-8 invalide et corps dépassant 1 048 576 octets, espaces finaux
compris, sont refusés par `400 invalid_json`. Les champs absents, nulls ou valeurs
invalides relèvent de `400 invalid_representation`. Un média type incorrect
renvoie `415 unsupported_media_type`.

Les slugs, e-mails et hostnames sont nettoyés aux extrémités et passés en
minuscules. Les noms sont nettoyés. Les rôles et statuts restent sensibles à la
casse. Le statut d'un membre n'est pas nettoyé : `" active "` est invalide.
Les limites après normalisation sont de 63 octets pour le slug, 200 pour les
noms, 320 pour l'e-mail et 253 pour le hostname. L'e-mail exige `@` et aucun
contrôle ASCII ; les noms refusent également les contrôles ASCII. Les domaines
sont des noms DNS ASCII, sans IP, port, chemin ou point final.

Chaque succès renvoie `200` avec la représentation normalisée seule, même à la
création. Un PUT identique ne modifie ni lignes, ni dates, ni audits, ni
publications. Le PUT membre ne crée aucun lien authentifié, invitation ou
adhésion. Les droits de plateforme existants sont conservés ; aucun nouveau
droit de plateforme ne peut être attribué par cette API.

Les erreurs ont la forme `{"error":{"code":"...","message":"..."}}`.
Une collision ou réattribution d'UUID renvoie `409 conflict` sans révéler le
tenant propriétaire ; un parent absent ou étranger renvoie
`404 parent_not_found`. La dernière rétrogradation ou suspension d'un propriétaire
actif est refusée par `409 last_owner`, y compris en concurrence. Les rôles
personnalisés sont conservés. Suspendre un parent ne réécrit pas ses enfants.
Une route ou méthode non prise en charge renvoie `404 not_found`.

## Domaines et mise à jour

Le routage public utilise un domaine persistant actif et un tenant actif.
L'hôte partagé de `XOLO_HTTP_BASE_URL` est réservé et reste disponible en mode
mono-tenant, même après renommage du slug du tenant. Les URL de base et callbacks
OIDC utilisent le domaine validé et le schéma, port et préfixe configurés.
Autoriser ces callbacks dans le fournisseur d'identité.

La migration reste automatique au démarrage. Lors de la première bascule, les
hôtes existants issus de `XOLO_MULTITENANCY_HOST_PATTERN` deviennent des domaines
persistants. Ce paramètre devient facultatif et ne sert plus au routage.
Les nouveaux tenants et les changements de slug ne créent pas automatiquement
de domaines. Déclarer ceux-ci par PUT.

Sauvegarder la base, conserver `XOLO_SECRET_KEY`, arrêter les anciennes répliques,
puis démarrer la nouvelle version. Les clés API et secrets restent utilisables.
Les anciennes sessions peuvent nécessiter une reconnexion.

Les clients de provisioning doivent adopter les nouveaux chemins et corps PUT ;
les anciennes routes sont retirées, sans alias ni `/v2`. Les certificats sans
URI SAN autorisée doivent être renouvelés et la liste d'URI configurée.

## Extensions Xolo

Les fonctions propres à Xolo sont disponibles sous `/v1/xolo` :

- `GET /healthz` et `GET /permissions` ;
- `GET`/`PATCH /tenants/{tenantID}` pour les métadonnées Xolo ;
- `GET`/`PATCH /tenants/{tenantID}/organizations/{orgID}` pour les paramètres
  d'organisation, notamment description, devise et partage des quotas ;
- CRUD des rôles sous `/tenants/{tenantID}/organizations/{orgID}/roles` ;
- `PUT /tenants/{tenantID}/organizations/{orgID}/members/{membershipID}/roles`
  pour les attributions Xolo, avec l'identifiant interne d'adhésion ;
- `GET`/`PUT /tenants/{tenantID}/users` pour les identités fournisseur/sujet.

Ces extensions gardent leurs représentations spécifiques et leurs réponses de
création/suppression `201`/`204`. Elles ne font pas partie du manifeste minimal.

## Lectures, préconditions et synchronisation

Chaque chemin PUT accepte aussi GET, avec la même représentation et un en-tête
`ETag: W/"u-<unix-microseconds>"`. Ce timestamp appartient à la représentation
commune : connexion, rattachement d'identité et métadonnées propres à Xolo ne
le modifient pas. Deux changements peuvent partager la même microseconde ;
l'ETag est opaque et ne constitue pas un compteur de révision.

PUT accepte `If-Match: *` pour exiger une ressource existante, ou une liste de
tags séparés par des virgules. La comparaison ignore `W/`, conformément à la
règle particulière du contrat. Validation, comparaison et mutation partagent
le verrou transactionnel. Une condition périmée renvoie
`412 precondition_failed`, même pour un corps identique ; une syntaxe incorrecte
renvoie `400 invalid_precondition`.

Retirer la dernière clé d'un chemin unitaire donne sa collection, y compris les
ressources suspendues. Les adhésions se listent dans
`/v1/tenants/{tenantID}/organizations/{organizationID}/members`.
`limit` vaut 100 par défaut, entre 1 et 1000. La réponse contient `items`
(`key`, `representation`, `etag`) et `next_cursor`, nul à la fin.
Continuer avec `cursor` et la même limite. Le tri utilise les clés immuables
canoniques, comparées octet par octet ; aucun total ni instantané global des
pages n'est promis.

Les curseurs de liste sont authentifiés par HMAC-SHA256 et liés à l'instance,
la collection, ses parents et la taille de page. Ils expirent **24 heures après
la première page**, sans renouvellement à la continuation. Un curseur altéré
ou utilisé dans un autre périmètre renvoie `400 invalid_cursor` ; un curseur
reconnu mais expiré renvoie `410 cursor_expired`. Ils sont opaques, non chiffrés.

`GET /v1/events/cursor` renvoie `{"cursor":"..."}`, même sur un flux vide.
`GET /v1/events?cursor=...&limit=100` renvoie `items`, `next_cursor` toujours
non vide et `has_more`. Le curseur d'événements est lié à l'instance et au flux,
mais pas à la limite. `has_more=false` signifie que l'horizon de cette réponse
est rattrapé : continuer à interroger le flux pour les changements suivants.

Le profil CloudEvents 1.0 est fermé : UUID d'événement, source persistante
`urn:uuid:...`, séquence décimale sous forme de chaîne, date, request ID et
`data` contenant seulement `resource_type`, `key`, `etag`. Aucun nom, e-mail,
acteur ou attribut interne n'est publié. L'audit conserve séparément l'acteur
et les états avant/après. Un PUT effectif émet un fait ; répétition et rollback
n'en émettent aucun. Les changements locaux de rôle et de statut conservent
leurs faits distincts. Une modification propre à Xolo n'émet pas de fait commun.

Capturer C0 **avant** de lister les cinq familles, puis rejouer depuis C0 en
relisant chaque clé avec GET. Découvrir les enfants de tout nouveau tenant ou
organisation. Sérialiser lecture et application par clé, persister les données
avant le checkpoint et dédupliquer par `(source, id)`. Une lecture peut être
plus récente que son événement. Une erreur sur une référence connue, y compris
un 404 inattendu, retient le checkpoint. Une extension inconnue et indépendante
peut être signalée puis dépassée. Sur 410, reconstruire toutes les collections
dans une nouvelle génération depuis un nouveau C0 ; remplacer la génération
précédente seulement après rattrapage du flux.

### Stockage, horizon et rétention

La migration automatique `202610020002` crée `common_records` et `common_feeds`,
reprend les ressources existantes et retire les anciens snapshots internes de
la table de publication. Les audits internes restent conservés. Aucune action
manuelle ni événement de création artificiel n'est nécessaire. La source du
flux et la clé de signature des curseurs persistent dans la base, indépendamment
des redémarrages et changements d'URL ; elles font partie de la sauvegarde.

Tous les writers d'identité verrouillent `publication_clocks` avant de lire ou
modifier les parents : verrou de ligne PostgreSQL, verrou d'écriture SQLite.
Le verrou est conservé jusqu'au commit ou rollback. Le writer suivant ne peut
pas allouer ou valider une position supérieure pendant ce temps. Lecture du
flux, capture et purge utilisent ce même verrou. Le compteur validé forme donc
un horizon sûr, y compris les trous liés à l'audit interne. Ce choix limite le
débit des écritures d'identité ; aucun publisher asynchrone ni calcul fondé sur
`MAX(sequence)` n'est nécessaire.

La rétention est **illimitée par défaut**, sans purge automatique ni variable
d'environnement dédiée. La méthode de maintenance du store
`PurgeCommonEvents(ctx, before)` retire seulement un préfixe résolu antérieur
au seuil UTC. Elle persiste la position du dernier événement supprimé, même
après purge complète. Un retour en arrière de l'horloge ne permet pas de
supprimer un événement récent situé plus tôt dans la séquence. Un ancien
curseur dont l'historique est perdu renvoie 410, sans renouvellement implicite.
Toute future politique planifiée doit laisser assez de temps pour reconstruire.

Le store valide les parents immuables et les références publiées dans la même
transaction, y compris pour une CLI. La publication n'accepte aucun payload
fourni par l'appelant. Xolo utilise un identifiant SQL applicatif de confiance,
sans RLS PostgreSQL par tenant ni rôle SQL séparé pour la publication. SQLite
ne dispose pas de cette frontière de rôles. Le code possédant le handle GORM
brut ou un accès direct en écriture à la base reste de confiance et peut
contourner ces contrôles ; les clients provisioning ne reçoivent aucun de ces
accès. Aucune protection SQL contre un identifiant applicatif compromis n'est
revendiquée.

Les suppressions physiques des extensions Xolo restent hors du profil commun
sans suppression. Un 404 consécutif ne constitue pas un tombstone implicite ;
les événements et la reprise associés sont définis par l'extension Xolo
facultative de cycle de vie décrite plus bas.

## Webhooks durables — extension Xolo

Les webhooks restent des notifications facultatives. Le consommateur interroge
`/v1/events` au démarrage, à la reconnexion et périodiquement ; une notification
ne fait jamais avancer son checkpoint du flux. La livraison est au moins une
fois. Le destinataire vérifie la signature avant de décoder le JSON, accepte
un écart d’horloge maximal de 300 secondes dans les deux sens, déduplique
`(source, id)` durablement et accuse l’acceptation durable avec un statut 2xx.

La migration `202610020003` est automatique au démarrage. Une installation
existante n’a aucune commande de migration à exécuter. Les webhooks restent
désactivés par défaut ; leur activation demande une destination explicitement
autorisée et un abonnement. Le worker fonctionne indépendamment du listener
provisioning. Le CRUD utilise ce listener mTLS lorsque les deux fonctions sont
activées. `GET /v1/xolo/extensions` annonce séparément l’extension ; le manifeste
commun reste inchangé.

| Variable | Défaut | Description |
| --- | --- | --- |
| `XOLO_WEBHOOKS_ENABLED` | `false` | Active matérialisation, livraison et nettoyage |
| `XOLO_WEBHOOKS_ALLOWED_ORIGINS` | obligatoire si activé | Origines HTTPS exactes séparées par des virgules, avec port non standard éventuel |
| `XOLO_WEBHOOKS_ALLOW_PRIVATE_NETWORKS` | `false` | Autorise les adresses privées et loopback des origines permises |
| `XOLO_WEBHOOKS_TLS_CA_FILE` | vide | CA PEM supplémentaire, en complément des racines système |
| `XOLO_WEBHOOKS_WORKERS` | `2` | Livraisons simultanées par processus, entre 1 et 16 |
| `XOLO_WEBHOOKS_POLL_INTERVAL` | `1s` | Intervalle de polling, entre 100 ms et 1 minute |
| `XOLO_WEBHOOKS_QUEUE_CAPACITY` | `10000` | Lignes de livraison conservées, entre 1 et 1 000 000 |

Les origines n’acceptent ni joker, chemin, credentials, query string ou fragment.
Une destination peut ajouter un chemin, mais pas de query string, fragment ou
credentials. Chaque réponse DNS est vérifiée à la connexion ; l’adresse IP
contrôlée est ensuite utilisée directement. Les proxies HTTP de l’environnement
sont ignorés. Les adresses link-local, multicast, non spécifiées, partagées ou
réservées restent interdites, notamment celles des métadonnées cloud, même
avec l’option réseau privé. Les certificats TLS sont toujours vérifiés.
Les restrictions réseau sortantes du déploiement doivent aussi couvrir Xolo.

Chaque abonnement possède un UUID fourni par le client et appartient à un
tenant. La propriété suit la famille `subscription` configurée au démarrage.
La suspension du tenant ne suspend pas les notifications de contrôle. Le
parent est résolu avant lecture des credentials. L’UUID ne peut pas être
réaffecté à un autre tenant. La limite est de 100 abonnements par instance.

| Méthode et route | Comportement |
| --- | --- |
| `GET /v1/xolo/tenants/{tenantID}/webhooks` | Liste sans secrets |
| `GET /v1/xolo/tenants/{tenantID}/webhooks/{id}` | Paramètres, position, état et nombre de clés |
| `PUT /v1/xolo/tenants/{tenantID}/webhooks/{id}` | Création ou remplacement des paramètres, statut 200 |
| `DELETE /v1/xolo/tenants/{tenantID}/webhooks/{id}` | Suppression de l’abonnement, credentials et toutes ses livraisons, statut 204 |
| `GET /v1/xolo/tenants/{tenantID}/webhooks/{id}/deliveries` | Les 100 derniers diagnostics, sans payload ni credentials |
| `POST /v1/xolo/tenants/{tenantID}/webhooks/{id}/reset` | Reconnaît la perte, vide la file et repart de l’horizon courant |
| `GET /v1/xolo/webhooks/status` | Compteurs de file, retards et pertes d’historique |

PUT exige `destination`, `events` et le booléen `enabled`. `events` contient
`["*"]` ou une liste non vide de types exacts du flux commun. `secrets` est
obligatoire à la création : une ou deux clés distinctes en base64, éventuellement
préfixées `whsec_`, décodant chacune 32 à 64 octets. Générer les clés hors de Xolo.
Omettre `secrets` lors d’une mise à jour conserve les clés actuelles. Elles sont
en écriture seule, chiffrées en AES-GCM avec `XOLO_SECRET_KEY` et liées dans le
contenu chiffré au tenant et à l’abonnement. Sauvegarder cette clé avec la base.

Pour une rotation, envoyer `[ancienne, nouvelle]`, faire accepter les deux au
destinataire, puis envoyer `[nouvelle]` après sa bascule. La réponse n’expose
que `secret_count`. Chaque tentative utilise les paramètres actuels : rotation
et changement de destination s’appliquent aussi aux livraisons en attente. Un
changement de filtre ne concerne que les événements non encore matérialisés.
`enabled=false` arrête les nouvelles matérialisations et réservations sans
avancer la position ni supprimer le travail. Une requête déjà réservée peut
encore terminer après désactivation, reset ou suppression ; son appel est
borné et un ancien résultat ne peut pas écraser une réservation plus récente.

Un nouvel abonnement part de l’horizon sûr courant, sans rejouer l’historique.
La matérialisation lit le flux sous son verrou d’allocation, puis insère les
livraisons et avance la position dans la même transaction. La paire unique
abonnement/événement évite les doublons de file. Chaque transaction lit au plus
100 publications, tous abonnements confondus, en priorisant les positions les
plus anciennes pour borner le verrou et éviter la famine. Les réservations expirent après
30 secondes et portent un jeton de réservation renouvelé. L’appel HTTP est hors
transaction. Un crash après acceptation mais avant enregistrement peut donc
redélivrer le même événement avec le même ID.

Le POST HTTPS envoie les octets exacts du CloudEvent conservé avec
`application/cloudevents+json`. `webhook-id` contient son UUID et
`webhook-timestamp` les secondes Unix de la tentative. HMAC-SHA256 signe
`id.timestamp.body` avec les octets de la clé décodée ; `webhook-signature`
contient une signature `v1,<base64>` par clé active, séparées par des espaces.
Une reprise conserve ID et corps, renouvelle timestamp et signatures, sans
ajouter d’acteur, e-mail ou attribut au profil commun.

Le client borne DNS/connexion à 3 secondes, TLS et en-têtes à 5 secondes, et
l’ensemble de l’appel à 10 secondes, lecture comprise. Il refuse les
redirections, limite les en-têtes à 16 Kio et le corps de réponse à 64 Kio,
et respecte l’annulation. Seule une réponse 2xx entièrement lue et bornée
réussit. Les diagnostics sont des codes fixes (`transport_error`, `redirect`,
`http_status`, `response_too_large`, `credentials_unavailable`…), sans corps
de réponse, signature, secret ou détail d’erreur réseau.

Les reprises attendent 5, 10, 20, 40… secondes, avec plafond d’une heure et
limite de 12 réservations ou 24 heures depuis la matérialisation. Une réservation
expirée compte comme tentative. Après épuisement, la livraison devient `failed` :
examiner le diagnostic et réconcilier par le flux. Les succès et échecs terminaux,
avec leurs copies indépendantes des payloads, restent sept jours après leur
fin, puis sont nettoyés lorsque le worker fonctionne. Un payload matérialisé
survit à la purge du flux. La rétention de celui-ci reste illimitée par défaut.

La capacité compte **toutes** les lignes, y compris les succès et échecs conservés.
Une file pleine suspend la matérialisation avec l’état `backpressure`, sans
perdre sa position ni bloquer les écritures de ressources. Prévoir le produit
capacité × taille des payloads/lignes/index, plus le flux et l’audit indépendants.
10 000 lignes représentent habituellement des dizaines de Mio, sans constituer
un quota disque en octets : mesurer la base et surveiller l’espace libre.
Diminuer la capacité ne supprime aucune livraison existante.

Une position située avant la borne de rétention passe à `history_lost` sans
avance implicite. Les payloads déjà matérialisés peuvent encore être livrés.
Reconstruire le consommateur depuis un nouveau C0, puis effectuer un reset
explicite avec `{"acknowledge_loss":true}` ; continuer le polling depuis le
checkpoint propre au consommateur. Le reset efface toutes les livraisons de
l’abonnement et repart de l’horizon courant. La suppression d’un tenant retire
aussi ses abonnements et livraisons dans la même transaction. Le cycle de vie
des organisations et membres reste hors du profil commun sans suppression ;
l'extension Xolo de cycle de vie définit le nettoyage par périmètre.

À configuration par défaut, l’objectif est une première tentative en quelques
secondes lorsque le système est sain, sans SLA de débit ou de latence.
`xolo_webhook_attempts_total` et `xolo_webhook_failures_total{reason}` sont des
compteurs par processus. `xolo_webhook_queue{state}`,
`xolo_webhook_lag_seconds{stage}` et `xolo_webhook_history_lost` sont des jauges
pour toute la base : utiliser le maximum entre réplicas, pas la somme.
Les labels ne contiennent aucun tenant, URI ou ID d’événement. Alerter sur
retard durable, échecs, saturation ou perte d’historique. La publication commune
est synchrone avec le commit ; le retard de matérialisation mesure les événements
validés qui ne sont pas encore mis en file.

L’arrêt annule les appels HTTP et attend les workers. L’enregistrement du
résultat dispose de cinq secondes supplémentaires ; en cas de crash ou de base
indisponible, la réservation devient récupérable à expiration. Prévoir au moins
15 secondes de grâce pour le processus. Une console inaccessible provoque des
reprises, sans arrêt du serveur. Désactiver le worker conserve les abonnements
et le travail ; le nettoyage reprend à sa réactivation.

## Identité, propriété et adoption — extension v1

La découverte `GET /v1/xolo/extensions` expose identité, adoption, propriété
effective et webhooks lorsqu’ils sont activés. Le manifeste minimal ne change pas.

### Propriété au démarrage

`XOLO_OWNERSHIP` configure les familles avec une liste `famille=propriétaire` :

```dotenv
XOLO_OWNERSHIP=tenant=control_plane,tenant_domain=control_plane,organization=control_plane,member=control_plane,organization_membership=control_plane,subscription=control_plane
```

Les six familles ci-dessus acceptent `local` ou `control_plane`. Une famille
omise est locale ; une famille ou valeur inconnue fait échouer la configuration.
Le provisioning mTLS refuse les écritures des familles locales avec 403. Les
écritures locales sur les familles pilotées, y compris les fragments UI, sont
refusées côté stockage avec 403. Les lectures gardent leurs permissions.
Rôles et invitations relèvent de `organization_membership` ; une opération
composite doit avoir autorité sur toutes les familles qu’elle modifie.
Les ressources métier conservent leurs autorisations actuelles.

Avec `member=control_plane`, une connexion exige un membre existant et ne
réécrit pas ses coordonnées depuis le fournisseur. Les comptes issus de
l’adoption restent valides. Les jetons API et comptes applicatifs sont préservés.
L’amorçage des administrateurs configurés reste une exception : l’authentificateur
doit prouver leur e-mail vérifié. Les écritures d’amorçage et de liaison sont
auditées avec l’UUID du compte. Le provisioning n’accorde aucun rôle de plateforme.

Cette politique n’est pas une permission SQL ni une coordination à chaud.
Arrêter tous les serveurs, workers et outils opérateur avant transfert, puis
redémarrer tous les réplicas avec la même configuration. Les commandes opérateur
ont l’autorité de la base et conservent les contrôles de tenant, de parent et de
dernier propriétaire. `cmd/seed` sert aux fixtures de test, pas à la maintenance.

### Identité déclarée et connexion

Le PUT d’un membre accepte `"identity":{"issuer":"https://id.example/","subject":"Sujet"}`.
Issuer est une URL HTTPS exacte de 1 à 2 048 octets UTF-8, avec hôte, sans
identifiants utilisateur, query, fragment, espaces périphériques ni contrôles.
Subject contient 1 à 255 octets UTF-8 sans contrôles ; espaces et casse sont
significatifs. Aucun trim, normalisation ou appel réseau de l’issuer fourni
n’a lieu pendant le PUT. `null`, objets incomplets ou champs inconnus sont refusés.
La déclaration apparaît dans PUT/GET/list et intervient dans l’ETag, jamais dans
le payload des événements communs.

La découverte OIDC/Gitea configurée associe explicitement le nom local du
fournisseur à son issuer ; Google utilise `https://accounts.google.com`.
GitHub OAuth et Gitea statique sans découverte n’inventent pas d’issuer.
Les ID tokens sont vérifiés avec les JWKS configurées lorsqu’elles existent
(RS256, issuer, audience, expiration). Les chemins OAuth opaques utilisent les
endpoints token/UserInfo configurés. Le booléen authentifié `email_verified`
(ou `verified_email` chez Google) est nécessaire au rattachement par e-mail ;
l’adresse de contact stockée ne constitue jamais une preuve.

L’unicité de déclaration et de liaison est **par tenant** : une même personne
garde des UUID distincts entre tenants. La connexion recherche l’identité exacte,
puis le lien existant, avec reprise des noms de providers historiques explicitement
associés à l’issuer. L’e-mail vérifié peut seulement rattacher un membre sans
déclaration ni autre lien. Tout conflit est refusé sans fusion ni réattribution.
Ajouter l’identité correspondant à son propre lien le conserve ; une identité
différente est refusée tant que ce lien existe. Maintenir l’identité et changer
l’e-mail conserve le lien. Omettre l’identité retire la déclaration et détache le
lien ; une connexion ultérieure par e-mail vérifié peut le rattacher.
Un PUT identique et une connexion sans changement restent sans effet sur la version.

### Sessions et logout

La migration `202610030001` ajoute déclaration, registre de sessions, marques
de révocation et garde anti-rejeu. Chaque requête utilisant une session OIDC
consulte la base partagée sans cache. Les sessions du registre durent au maximum
24 heures ; l’expiration du cookie peut raccourcir cette durée. Les anciens
cookies OIDC sans entrée de registre sont refusés. Déployer en arrêtant les
anciens processus : ils ne savent pas appliquer ces révocations. Conserver les
clés de cookies partagées et synchroniser les horloges. Une erreur de base refuse
l’authentification de session.

Configurer `/auth/oidc/providers/{provider}/backchannel-logout` sur un hôte de
tenant actif et accessible au fournisseur, avec
`backchannel_logout_session_required=false`. Le POST est un formulaire de
16 Kio maximum avec un seul `logout_token`, sans query ni cookie nécessaire.
Le profil exige RS256, les clés configurées, l’issuer exact, l’unique audience
client, un éventuel `azp` correspondant, `sub`, `jti`, `iat` et `exp`, avec âge
et durée de vie limités à cinq minutes et sans tolérance future/d’expiration.
L’événement logout est unique et contient `{}` ; `nonce` est interdit même null.
`sid` seul est refusé ; avec `sub`, il ne restreint pas la portée.
Succès : 200 ; token invalide ou rejoué : 400 ; erreur de registre : 503.
Le token brut n’est pas journalisé. Ce profil restreint s’appuie sur
[OIDC Back-Channel Logout](https://openid.net/specs/openid-connect-backchannel-1_0.html).

La révocation et la garde anti-rejeu sont atomiques et persistent après
redémarrage. Toutes les sessions OIDC Xolo du couple issuer/sujet sont révoquées,
y compris entre tenants et alias de providers ; les autres sujets/issuers,
comptes, rôles et adhésions sont préservés. Création de session et logout utilisent
le même verrou ; le début de connexion est lié au provider et au state OAuth.
Un callback d’une authentification commencée avant révocation ne peut recréer
une session après celle-ci : recommencer la connexion. Une requête déjà autorisée
peut finir. Les jetons API internes et bearer/ID tokens externes sont des
credentials distincts, hors de cette révocation des sessions navigateur OIDC.
Les marques et gardes durables sont conservées ; prévoir leur croissance.

### Export, adoption et détachement

L’export privé est disponible par `GET /v1/xolo/export` sur le listener mTLS
(`Cache-Control: no-store`) ou avec le DSN existant :

```sh
go run ./cmd/adoption -action export -file /secure/xolo-adoption.json
go run ./cmd/adoption -action verify -file /secure/xolo-adoption.json
```

Le fichier est créé en 0600 sans écrasement ; une erreur signalée le supprime,
mais un processus tué peut laisser un export incomplet. Il contient des données
personnelles : protéger le répertoire et le transfert, sans dépôt Git ni logs.
L’enveloppe JSON `xolo-adoption/1` contient `payload` et `sha256`. L’empreinte
hexadécimale minuscule SHA-256 couvre **les octets UTF-8 exacts de la valeur JSON
payload, accolades incluses**, sans l’enveloppe ni ses espaces. Ne pas reformater
le payload avant vérification. L’empreinte détecte une corruption, pas un
remplacement malveillant ; fichier et transport établissent sa provenance.

Le payload contient version, contrat, source persistante, `c0`, cinq familles,
records (`family`, `key`, `representation`, `etag`), nombre et `complete: true`.
C0 précède les lectures, sous verrou de publication pour un snapshot complet.
`default` et les ressources suspendues sont inclus. L’importeur vérifie fichier,
empreinte, nombre et complétude avant toute génération autoritaire. L’export
actuel garde l’inventaire commun en mémoire et bloque les writers pendant la
lecture : prévoir une fenêtre de maintenance pour les gros inventaires.
Il exclut données métier, liens non déclarés, sessions, jetons API, audit et
secrets webhook. Conserver une sauvegarde de base réellement restaurable.

1. Tester une connexion réelle d’administrateur de plateforme et sauvegarder.
   Exporter en mode local ; importer côté console les UUID qualifiés par la
   source. Réconcilier noms, domaines et e-mails dans la console, sans remapper
   les UUID, dupliquer `default`, fusionner les comptes ni envoyer de PUT de
   marquage. Xolo fournit le validateur de format, pas une application console
   ni un endpoint d’import dans sa base.
2. Rejouer depuis C0 et relire les ressources de façon idempotente. Un 410 impose
   d’abandonner la génération en cours et de recommencer entièrement.
3. Arrêter tous les writers, rattraper le flux final, configurer les propriétaires
   et redémarrer sur la même base. Vérifier découverte, refus des anciennes
   écritures et accès des comptes existants.
4. Pour détacher, vérifier par connexion réelle l’accès opérateur qui subsistera,
   arrêter serveurs, workers et outils, puis exécuter :

   ```sh
   go run ./cmd/adoption -action detach -writers-stopped -operator-access-verified
   ```

   La commande exige un administrateur actif enregistré et supprime atomiquement
   abonnements, secrets chiffrés et livraisons. L’audit attribue l’opération à
   l’UID système de l’opérateur. Les flags attestent les vérifications humaines ;
   ils ne testent pas le fournisseur et n’arrêtent aucun autre processus.
   Ressources, UUID, liens, audit, source et curseurs restent intacts. Un webhook
   déjà transmis ne peut être rappelé. Retirer les overrides de propriété,
   redémarrer en autonomie et vérifier connexion, écritures locales et flux.

## Cycle de vie et ressources métier (extensions)

Ces extensions sont désactivées par défaut et découvertes par
`GET /v1/xolo/extensions`, séparément du manifeste commun. Activer
`XOLO_LIFECYCLE_ENABLED=true` seulement après mise à niveau des consommateurs :
ils doivent accepter l’état `deleted`, relire les clés après notification et
reconstruire leur inventaire en cas de perte d’historique. Arrêter les anciennes
répliques et aligner la configuration de tous les writers.

Le DELETE canonique d’un tenant, d’une organisation ou d’un membre renvoie 202.
Le périmètre reste lisible, gelé, jusqu’à réception confirmée de son export et
expiration de la rétention. Les anciennes cascades immédiates ont été retirées ;
une suppression locale refuse avec 409 si l’extension est désactivée.

1. Conserver l’ETag retourné par DELETE.
2. Télécharger `{ressource}/deletion/export` sur le listener mTLS autorisé.
   Vérifier `xolo-deletion/1`, périmètre, version, inventaire, nombre,
   `complete: true` et SHA-256 des octets exacts du payload JSON.
3. Enregistrer et relire l’archive depuis un stockage durable protégé, puis
   envoyer `POST {ressource}/purge-confirmation`, avec l’ETag supprimé dans
   `If-Match` et `{"export_sha256":"…"}`. Générer un fichier ne confirme rien.
4. Suivre `GET {ressource}/deletion`. Le worker reprend après redémarrage ;
   un échec transactionnel conserve le périmètre et le diagnostic
   `purge_failed`. Corriger le stockage, sans modifier le reçu en base.

La rétention `XOLO_LIFECYCLE_RETENTION` vaut `720h` par défaut et est fixée
à la suppression ; `XOLO_LIFECYCLE_POLL_INTERVAL` vaut `1m`. Un parent tenant
supplante ses descendants planifiés. Les UUID purgés restent réservés. Les
sessions d’une identité partagée restent utilisables dans les autres tenants ;
elles ne peuvent pas rétablir le compte supprimé. Domaines et adhésions utilisent
un DELETE immédiat 204 avec contrôle des parents, du dernier propriétaire et
d’`If-Match`. Une recréation reçoit un nouvel ETag.

`XOLO_BUSINESS_RESOURCES_ENABLED=true` active PUT/GET/list des rôles
personnalisés, applications, quotas, alertes et fournisseurs. Les collections
sont sous `/v1/xolo/tenants/{tenantID}/organizations/{orgID}`, sauf
`/v1/xolo/tenants/{tenantID}/quotas`. Elles partagent transactions, propriété
`XOLO_OWNERSHIP`, ETags, préconditions et curseurs. Les clés fournisseur sont
acceptées uniquement en écriture, chiffrées en stockage et exclues des événements.

Le [profil complet et l’inventaire de nettoyage](https://github.com/xolo-gateway/xolo/blob/main/internal/provisionning/LIFECYCLE.md)
décrivent les DTO, permissions, secrets, données personnelles, priorités et
limites de restauration. Protéger séparément les clés de chiffrement ; ne pas
rejouer sessions, jetons ou livraisons archivés. La purge signale les trous du
flux par 410 et `history_lost`. Les requêtes déjà autorisées et notifications
transmises ne sont pas rappelables. L’exploitant fixe et vérifie séparément
l’expiration des exports, sauvegardes et journaux externes.
