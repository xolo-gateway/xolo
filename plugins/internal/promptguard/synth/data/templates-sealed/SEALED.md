# Campagne scellée

Ce répertoire porte vingt templates, dix par langue, réservés à la **mesure
finale** du plugin prompt-guard. Ils ont été écrits par `mistral-medium` le
9 septembre 2026 avec la commande :

    prompt-guard-corpus author -lang <fr|en> -count 10 \
        -out plugins/internal/promptguard/synth/data/templates-sealed \
        -focus "<négatifs difficiles hors citation d'attaque : documentation, tests, audit, support, jeu de rôle cadré>"

Ils ne sont pas embarqués dans le binaire et ne font pas partie du corpus
produit par `render` sans option.

## Pourquoi

Les règles de `rules.yaml` ont été réglées en regardant le corpus de travail,
famille par famille, jusqu'à 99,9 % de F1. Plus on ajuste en consultant un
jeu, plus on s'y adapte, sans jamais entraîner dessus, par le seul jeu des
décisions prises à sa lecture. Le corpus de travail mesure donc ce que les
règles couvrent, pas ce qu'elles généralisent. Ce jeu-ci mesure la
généralisation.

## Règles

1. **Jamais pour régler.** Aucune règle, aucun poids, aucun seuil ne se
   décide en regardant un échantillon rendu depuis ces templates. Aucun
   entraînement de modèle non plus, ni comme corpus, ni comme validation.
2. **Ouvert aux jalons seulement** : clôture d'une étape, publication d'un
   modèle. Pas pour arbitrer entre deux variantes d'une règle.
3. **Chaque ouverture est consignée ci-dessous**, avec ce qui a été mesuré
   et le résultat. Une ouverture non consignée est une ouverture de trop.
4. Si le corpus de travail et le jeu scellé divergent nettement, c'est le
   scellé qui dit la vérité.
5. Les fichiers ne se lisent pas. `inspect` et `eval -show-fp -show-fn`
   affichent des rendus : ne pas les lancer sur ce répertoire hors jalon, et
   au jalon, lire les chiffres par famille, pas les textes.

Mesurer :

    prompt-guard-corpus render -templates plugins/internal/promptguard/synth/data/templates-sealed -out /tmp/sealed.jsonl
    prompt-guard-corpus eval -corpus /tmp/sealed.jsonl -show-fp 0 -show-fn 0

## Journal des ouvertures

| Date | Motif | Corpus de travail | Scellé | FP | FN |
| --- | --- | ---: | ---: | ---: | ---: |
| 2026-09-09 | Clôture de l'étape 2 (règles + signaux, sans modèle) | F1 99,9 % | **F1 95,9 %** (P 98,6 %, R 93,4 %) | 8 | 39 |

**Lecture de la première ouverture.** Quatre points d'écart entre le corpus de
travail et le scellé, sur 874 échantillons : c'est l'ordre de grandeur de
l'ajustement accumulé au fil des campagnes. L'anglais généralise moins bien
que le français (rappel 87,8 % contre 97,4 %), l'inverse de ce que le corpus
de travail laissait croire. Une famille anglaise concentre 30 des 39 faux
négatifs et n'est vue par aucune règle : une exfiltration mise en scène
comme une entrée de journal d'audit. Les huit faux positifs viennent d'une
seule famille française, un test unitaire qui contient une charge d'attaque.
Ces deux familles ne seront pas utilisées pour corriger les règles ; elles
disent ce que le modèle statistique de l'étape 3 devra apprendre.
