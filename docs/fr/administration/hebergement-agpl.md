# Héberger Xolo : ce que l'AGPL implique

Xolo est distribué sous licence [AGPL-3.0](../../../LICENSE.md). Cette page
explique ce que cela implique quand vous hébergez une instance pour d'autres
utilisateurs que vous-même. C'est une explication, pas un avis juridique ; le
texte de la licence fait foi.

## Vous hébergez la version officielle, sans modification

Aucune obligation particulière. Indiquez à vos utilisateurs où trouver le code
source, par exemple avec un lien vers le
[dépôt officiel](https://github.com/xolo-gateway/xolo).

## Vous hébergez une version modifiée

L'article 13 de l'AGPL-3.0 s'applique : vous devez offrir à tout utilisateur
qui interagit avec l'instance via le réseau la possibilité de récupérer le
code source correspondant à la version réellement déployée.

Cela concerne **toute** modification du cœur : un correctif, une configuration
compilée, un thème, un patch d'intégration de trois lignes.

### Mise en conformité

1. Publiez votre fork dans un dépôt public sous AGPL-3.0.
2. Affichez un lien visible dans l'interface (pied de page, page « À propos »).
3. Assurez-vous que le code publié correspond **exactement** à la version
   déployée (tag ou commit précis).

### Comment éviter la contrainte

Faites passer vos spécificités par des **plugins** plutôt que par des
modifications du cœur. Les plugins s'exécutent dans des processus séparés et
communiquent avec Xolo via l'interface gRPC définie dans
`pkg/pluginsdk/proto/plugin.proto` ; l'article 13 ne les atteint pas. Ce
n'est pas qu'une interprétation : le fichier
[LICENSE-EXCEPTION](../../../LICENSE-EXCEPTION) accorde, au titre de
l'article 7 de l'AGPL, la permission de diffuser ces plugins sous n'importe
quelle licence, et le SDK sur lequel ils s'appuient est sous Apache-2.0.

C'est aussi le bon choix technique : vos adaptations survivent aux mises à
jour.

Attention à la frontière : un patch de Xolo lui-même dont votre plugin a
besoin, aussi petit soit-il, est une modification du cœur et reste sous AGPL.
