# Gouvernance

Traduction française de [GOVERNANCE.md](../../../GOVERNANCE.md) ; la version
anglaise fait foi.

Xolo est dirigé par un mainteneur principal, William Petit
([@Bornholm](https://github.com/Bornholm)), fondateur du projet, qui détient
l'autorité finale sur celui-ci. Les décisions sont préparées en public et
prises par consensus tacite chaque fois que possible ; le mainteneur principal
arbitre lorsque le consensus ne se dégage pas. Ce modèle est volontairement
simple pour un projet de la taille actuelle de Xolo. Il a vocation à évoluer
avec le projet, selon le processus décrit à la fin de ce document.

## Engagement de licence

Xolo est distribué sous licence GNU Affero General Public License version 3
([AGPL-3.0](../../../LICENSE.md)) et le restera. Le projet ne publiera pas
d'édition propriétaire et n'adoptera pas de double licence.

La seule évolution de licence que le projet s'autorise est la bascule vers
une version ultérieure de la GNU AGPL ou une licence libre garantissant des
libertés équivalentes, et encore exige-t-elle l'autorisation de chaque
titulaire de droits. Édition propriétaire et double licence sont exclues
d'emblée : c'est sur cet engagement que la communauté peut construire.

Les plugins sont un cas à part. Le SDK plugins (`pkg/pluginsdk`) est
distribué sous licence Apache 2.0, et
[LICENSE-EXCEPTION](../../../LICENSE-EXCEPTION) permet aux plugins qui
dialoguent avec Xolo via son interface gRPC d'être diffusés sous n'importe
quelle licence. Le cœur reste copyleft ; la frontière des plugins est
ouverte.

Le nom et le logo Xolo sont des marques, détenues par le mainteneur principal
jusqu'à la création d'une association destinée à porter le projet. Leur usage
est régi par [TRADEMARK.md](../../../TRADEMARK.md).

Les contributeurs conservent le droit d'auteur sur leurs contributions. Le
projet utilise le [Developer Certificate of Origin](../../../DCO), pas un
accord de contribution (CLA) : personne ne se voit demander de céder ses
droits ni d'accorder une licence exclusive au projet.

## Rôles

**Utilisateur.** Utilise Xolo, signale des anomalies, propose des
évolutions, participe aux discussions.

**Contributeur.** A au moins une contribution fusionnée, de quelque nature
que ce soit : code, documentation, traduction, tri des tickets.

**Committer.** Peut fusionner des pull requests. Nommé par le mainteneur
principal.

**Mainteneur.** Porte une responsabilité décisionnelle sur tout ou partie du
projet : feuille de route, arbitrages, publications, réponse aux incidents
de sécurité. Nommé par le mainteneur principal.

**Mainteneur principal.** Détient l'autorité finale sur le projet :
architecture, feuille de route technique, standards de développement,
nomination des committers et mainteneurs, approbation des RFC, politiques
de compatibilité et de sécurité, publications. Il agit dans l'intérêt du
projet et déclare tout conflit entre cet intérêt et celui d'un employeur ou
d'un client.

L'accès aux rôles de committer et de mainteneur est public et fondé sur :

- la qualité et la régularité des contributions ;
- la capacité à effectuer des revues ;
- la connaissance du projet ;
- le respect des règles de sécurité et de conduite ;
- la capacité à agir dans l'intérêt général du projet.

Travailler pour le même employeur que le mainteneur principal, ou financer
le projet, ne confère par soi-même aucun rôle. Les titulaires des rôles
sont listés dans [MAINTAINERS.md](../../../MAINTAINERS.md).

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
  ouvertes au moins **trente jours**.

En cas de désaccord technique persistant, les positions sont formalisées
dans la RFC, une voie réversible ou expérimentale est recherchée, et le
mainteneur principal tranche. Quelle que soit l'issue, la décision et ses
motifs sont publiés.

## Règles de développement

- Aucune modification n'atteint une branche protégée sans revue ; l'auteur
  d'une contribution n'en est jamais le seul approbateur. Lorsque le
  mainteneur principal est le seul autre mainteneur, la revue d'un committer
  compte.
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
et est approuvée par le mainteneur principal. Celui-ci peut déléguer le rôle
de release manager à un autre mainteneur.

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
coordonnée par les mainteneurs. Voir [SECURITY.md](../../../SECURITY.md).

## Évolution du présent document

Les modifications substantielles passent par le processus de RFC ci-dessus,
avec la période de commentaires de trente jours, et sont approuvées par le
mainteneur principal. L'engagement de licence ci-dessus échappe à ce
processus : il ne peut être modifié qu'avec l'autorisation de chaque
titulaire de droits.

Le passage à une gouvernance partagée (conseil de mainteneurs, fondation,
partenariat entre plusieurs entreprises) est une modification substantielle
de ce type : elle sera proposée en public, sous forme de RFC, lorsque le
projet aura les contributeurs qui la justifient.
