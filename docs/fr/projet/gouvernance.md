# Gouvernance

Traduction française de [GOVERNANCE.md](../../../GOVERNANCE.md) ; la version
anglaise fait foi.

Xolo est gouverné à deux niveaux. Les entreprises qui portent conjointement
le projet sont liées par une convention de partenariat qui organise leurs
relations : coûts communs, marque, infrastructures, loyauté commerciale,
entrée et sortie des membres. Le présent document est la face publique de
cette gouvernance : il décrit les organes, les rôles et les processus de
décision qui s'appliquent à quiconque contribue, salarié d'un membre du
consortium ou non.

## Engagement de licence

Xolo est distribué sous licence GNU Affero General Public License version 3
([AGPL-3.0](../../../LICENSE.md)) et le restera. Le projet ne publiera pas
d'édition propriétaire et n'adoptera pas de double licence.

La seule évolution de licence que la convention de partenariat autorise est
la bascule vers une version ultérieure de la GNU AGPL ou une licence libre garantissant des libertés équivalentes, et encore exige-t-elle à la fois l'unanimité du
comité de pilotage et l'autorisation de chaque titulaire de droits. Édition propriétaire et double licence sont exclues d'emblée :
c'est sur cet engagement que la communauté peut construire.

Les contributeurs conservent le droit d'auteur sur leurs contributions. Le
projet utilise le [Developer Certificate of Origin](../../../DCO), pas un
accord de contribution (CLA) : personne ne se voit demander de céder ses
droits ni d'accorder une licence exclusive au projet.

## Organes de gouvernance

**Comité de pilotage (COPIL).** Représente les entreprises membres du
consortium, une voix chacune. Il traite ce qui n'est pas technique : budget
et coûts communs, politique de marque, admission de nouveaux membres,
évolutions de la convention de partenariat, différends commerciaux entre
membres. Il désigne les membres initiaux du comité technique.

**Comité technique (TSC).** Autorité technique du projet. Il définit
l'architecture, administre la feuille de route technique, adopte les
standards de développement, nomme les committers et mainteneurs, approuve
les RFC, définit les politiques de compatibilité et de sécurité, et
autorise les publications.

Les sièges du TSC sont :

- un mainteneur présenté par chaque entreprise membre du consortium ;
- un siège communautaire, ouvert à tout mainteneur non désigné par une
  entreprise membre (voir plus bas).

Les membres du TSC agissent dans l'intérêt du projet et déclarent tout
conflit entre cet intérêt et celui de leur employeur ou d'un client. Le TSC
publie ses décisions non confidentielles dans le dépôt du projet.

## Rôles

**Utilisateur.** Utilise Xolo, signale des anomalies, propose des
évolutions, participe aux discussions.

**Contributeur.** A au moins une contribution fusionnée, de quelque nature
que ce soit : code, documentation, traduction, tri des tickets.

**Committer.** Peut fusionner des pull requests. Nommé par le TSC.

**Mainteneur.** Porte une responsabilité décisionnelle sur tout ou partie du
projet : feuille de route, arbitrages, publications, réponse aux incidents
de sécurité.

L'accès aux rôles de committer et de mainteneur est public et fondé sur :

- la qualité et la régularité des contributions ;
- la capacité à effectuer des revues ;
- la connaissance du projet ;
- le respect des règles de sécurité et de conduite ;
- la capacité à agir dans l'intérêt général du projet.

L'appartenance à une entreprise membre du consortium ne confère par
elle-même aucun rôle. Les titulaires des rôles sont listés dans
[MAINTAINERS.md](../../../MAINTAINERS.md).

## Le siège communautaire

Le siège communautaire du TSC est réservé à un mainteneur non désigné par
une entreprise membre. Il reste vacant jusqu'à ce qu'un candidat remplisse
les critères suivants :

- statut de mainteneur détenu depuis au moins six mois ;
- contributions soutenues et substantielles sur au moins les douze mois
  précédents ;
- participation régulière aux revues et aux discussions techniques
  publiques ;
- absence de conflit d'intérêts non déclaré.

Dès qu'au moins un candidat remplit les critères, le siège est pourvu par
élection : les candidats se déclarent publiquement, et les committers et
mainteneurs du projet votent, une voix chacun, pour un mandat de douze mois
renouvelable. Le TSC organise le vote et en publie le résultat.

## Prise de décision

Les décisions courantes se prennent par consensus tacite, en public. Une
proposition publiée dans les canaux de discussion du projet est réputée
acceptée si elle n'a suscité aucune objection motivée après **trois jours
ouvrés**.

Une objection doit présenter :

- le risque identifié ;
- les éléments techniques sur lesquels elle repose ;
- une alternative, ou les conditions permettant sa levée.

Les décisions structurantes passent par une RFC publique, ouverte aux
commentaires pendant au moins **sept jours ouvrés**. Sont notamment
structurants :

- une évolution majeure de l'architecture ;
- une rupture de compatibilité, y compris les changements incompatibles de
  l'interface gRPC des plugins (`pkg/pluginsdk/proto/plugin.proto`) ;
- l'ajout d'une dépendance centrale ;
- un changement de format de données ;
- une modification significative du modèle de sécurité ;
- la suppression d'une fonctionnalité publique ;
- la création ou la suppression d'un composant principal ;
- les modifications du présent document ou du régime de licence de
  l'interface plugins et du SDK (`pkg/pluginsdk`) — celles-ci restent
  ouvertes au moins **trente jours** et requièrent l'approbation du COPIL
  en plus du TSC.

En cas de désaccord technique persistant, les positions sont formalisées
dans la RFC, le TSC recherche une voie réversible ou expérimentale, et le
statu quo est maintenu — soixante jours au plus, après quoi le comité de
pilotage statue ou désigne un expert indépendant. Quelle que soit l'issue,
la décision et ses motifs sont publiés.

## Règles de développement

- Aucune modification n'atteint une branche protégée sans revue ; l'auteur
  d'une contribution n'en est jamais le seul approbateur.
- Les tests automatiques obligatoires doivent passer.
- Les changements structurants référencent une RFC ou une décision
  d'architecture.
- Les commits identifient leur auteur et leur origine (voir le DCO dans
  [CONTRIBUTING.md](../../../CONTRIBUTING.md)).
- Les correctifs de sécurité suivent [SECURITY.md](../../../SECURITY.md).

## Publications

Une version est officielle lorsqu'elle est construite depuis le dépôt
officiel, suit le processus de publication, passe les contrôles automatisés,
porte un numéro de version et des notes de version, est signée ou attestée,
et est approuvée par le release manager et au moins un autre mainteneur. Le
rôle de release manager alterne entre les membres du consortium selon un
calendrier fixé par le TSC.

Toute distribution modifiée, quel qu'en soit l'auteur, doit permettre
d'identifier ses différences avec la version officielle.

## Développements financés

Chacun peut financer le développement d'une fonctionnalité. Le financement
ne confère aucun droit automatique à l'intégration dans le cœur, à
l'inclusion dans une version donnée, à la modification des standards
techniques ou à un rôle de mainteneur. Les développements financés suivent
le même processus de contribution que les autres.

## Sécurité

Les vulnérabilités se signalent en privé et sont traitées en divulgation
coordonnée par une équipe sécurité restreinte comprenant au moins un
représentant habilité de chaque membre du consortium. Voir
[SECURITY.md](../../../SECURITY.md).

## Évolution du présent document

Les modifications substantielles passent par le processus de RFC ci-dessus,
avec la période de commentaires de trente jours, et requièrent
l'approbation du comité de pilotage.
