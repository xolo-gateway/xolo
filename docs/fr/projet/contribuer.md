# Contribuer à Xolo

Traduction française de [CONTRIBUTING.md](../../../CONTRIBUTING.md) ; la
version anglaise fait foi.

Les issues et pull requests peuvent être rédigées en anglais ou en français.

## Avant de commencer

- **Corrections de bugs, coquilles, améliorations de doc** : ouvrez
  directement une pull request.
- **Nouvelles fonctionnalités ou changements de comportement** : ouvrez
  d'abord une issue pour décrire ce que vous voulez faire. Cela évite d'écrire
  du code impossible à fusionner parce qu'il entre en conflit avec la feuille
  de route ou duplique un travail en cours.
- **Changements structurants** (architecture, ruptures de compatibilité,
  dépendances centrales, interface plugins, gouvernance) : ils requièrent
  une RFC — voir [GOVERNANCE.md](../../../GOVERNANCE.md).

Les propositions courantes suivent le consensus tacite : publiées
publiquement, acceptées si aucune objection motivée n'est soulevée sous
trois jours ouvrés ([GOVERNANCE.md](../../../GOVERNANCE.md) décrit ce
qu'est une objection motivée).

## Environnement de développement

Il vous faut Go (la version épinglée dans `go.mod`), GNU make, et Docker pour
les tests d'intégration.

```bash
make build          # construit bin/server
make generate       # templ + tailwind ; requis après édition de fichiers .templ
make watch          # serveur de dev avec rechargement à chaud (utilise .env)

go test ./...       # tests unitaires, SQLite seul, pas besoin de Docker
make test-integration  # suite des stores sur SQLite ET PostgreSQL (Docker requis)
```

La configuration passe par des variables d'environnement préfixées `XOLO_` ;
voir `.env.dist`.

## Conventions

- Écrivez du code qui ressemble au code environnant.
- Ne modifiez jamais les fichiers générés (`*_templ.go`, `*.pb.go`) ; modifiez
  la source (`.templ`, `.proto`) et régénérez.
- Toute interface web doit utiliser les composants templui sous
  `internal/http/handler/webui/templui/component/`, pas des balises HTML
  brutes.
- Les messages de commit suivent
  [Conventional Commits](https://www.conventionalcommits.org/) :
  `feat(scope): sujet`, `fix: sujet`, etc.
- Tout nouveau comportement de store doit être vérifié sur les deux backends
  (les tests de stores passent par `eachBackend()`).

## Developer Certificate of Origin (DCO)

Ce projet utilise le [Developer Certificate of Origin](../../../DCO) plutôt
qu'un accord de contribution (CLA).

En signant vos commits, vous certifiez que vous avez le droit de soumettre le
code proposé et qu'il peut être distribué sous la licence du projet. Vous
conservez l'intégralité de vos droits d'auteur : le projet ne demande aucune
cession ni licence exclusive.

Xolo est et restera distribué sous AGPL-3.0. Le projet ne publiera pas
d'édition propriétaire.

### Comment signer

```bash
git commit -s -m "fix: description de la modification"
```

Git ajoute alors une ligne au message :

```
Signed-off-by: Prénom Nom <prenom.nom@exemple.fr>
```

Configurez votre identité une fois pour toutes :

```bash
git config --global user.name "Prénom Nom"
git config --global user.email "prenom.nom@exemple.fr"
```

### Si vous avez oublié la signature

Dernier commit :

```bash
git commit --amend -s --no-edit
```

Les N derniers commits :

```bash
git rebase --signoff HEAD~N
git push --force-with-lease
```

La CI rejette les pull requests contenant des commits non signés.

### Pseudonymes

Un pseudonyme stable est accepté s'il vous identifie de manière constante et
qu'une adresse de contact valide y est associée. Les contributions anonymes ou
sous adresse jetable ne peuvent pas être acceptées.

### Contributions en cadre professionnel

Dans beaucoup de juridictions, votre employeur détient les droits sur le code
écrit dans le cadre de votre travail (en France, article L113-9 du Code de la
propriété intellectuelle). Assurez-vous d'avoir l'autorisation de contribuer,
et utilisez de préférence votre adresse professionnelle.

## Processus de pull request

1. Forkez, créez une branche.
2. Gardez la pull request ciblée ; les changements sans rapport vont dans des
   PR séparées.
3. Ajoutez ou mettez à jour les tests pour ce que vous changez.
4. Lancez `go test ./...` et, si vous avez touché aux stores,
   `make test-integration`.
5. Signez chaque commit (`git commit -s`).
6. Ouvrez la PR et remplissez le modèle.
7. Répondez aux retours de revue.

Toute modification est relue avant d'atteindre une branche protégée, et
l'auteur d'une contribution n'en est jamais le seul approbateur — cela vaut
aussi pour les mainteneurs.

Première réponse sous 5 jours ouvrés, au mieux de nos disponibilités. La
fusion reste à la discrétion des committers et mainteneurs ; ouvrir une PR
ne crée aucune obligation de l'accepter, et financer un développement ne
confère aucun droit automatique à son intégration.

## Plugins

Les plugins s'exécutent dans des processus séparés et dialoguent avec Xolo via
l'interface gRPC définie dans `pkg/pluginsdk/proto/plugin.proto`. Les
contributions de nouveaux plugins embarqués (sous `plugins/`) sont bienvenues ;
discutez-en d'abord dans une issue. Le régime de licence de l'interface et du
SDK pour les auteurs de plugins tiers est en cours de finalisation et sera
documenté séparément.
