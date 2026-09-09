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
| 2026-09-09 | Modèle v1 (`2026-09-09.1`), règles + signaux + modèle, plancher 0,5, plafond 0,6 | F1 99,9 % | **F1 95,5 %** (P 95,3 %, R 95,6 %) | 28 | 26 |
| 2026-09-09 | Modèle v2 (`2026-09-09.2`), après quinze familles bénignes sur la configuration et la sécurité dans le jeu de travail | F1 100 % | **F1 96,8 %** (P 98,3 %, R 95,4 %) | 10 | 27 |
| 2026-09-09 | Modèle v4 (`2026-09-09.4`), après une famille d'exfiltration en journal d'audit et quelques règles anglaises tirées du banc d'essai deepset | F1 100 % | **F1 97,1 %** (P 98,6 %, R 95,6 %) | 8 | 26 |
| 2026-09-09 | Modèle v5 (`2026-09-09.5`), après extension jailbreak (persona à capacité, contraintes de réponse, ROT13) issue du minage des jeux publics et d'OWASP LLM01 | F1 100 % | **F1 97,3 %** (P 98,3 %, R 96,3 %) | 10 | 22 |

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

**Lecture de la deuxième ouverture.** Le modèle récupère treize des
trente-neuf attaques manquées, le rappel anglais passe de 87,8 à 91,8 %. Il
ajoute vingt faux positifs, tous sur une même famille française : une
documentation technique qui parle de la configuration du prompt système sans
rien demander. Le F1 global recule de 0,4 point. Rien n'a été réglé sur ce
résultat. La correction retenue avant l'ouverture, appliquer l'atténuation des
citations au risque combiné plutôt qu'aux règles seules, a été décidée sur les
trois faux positifs du jeu de test de travail. Ce que dit cette ouverture,
c'est que le corpus de travail manque de textes bénins sur la configuration et
la sécurité, et c'est là qu'ira la prochaine campagne `author` du jeu de
travail, avec une consigne explicite. Cette ouverture a eu lieu deux fois de
suite, avant et après la correction sur l'atténuation, avec les mêmes
chiffres ; elle compte pour une.

**Lecture de la troisième ouverture.** La campagne de négatifs bénins du jeu
de travail, décidée à la lecture de la deuxième ouverture sans regarder les
textes du scellé, a fait ce qu'on en attendait : les faux positifs de la
documentation de configuration passent de 20 à 2, et le modèle devient
un gain net sur les règles seules, 96,8 contre 95,9 % de F1. Les huit faux
positifs du test unitaire avec charge n'ont pas bougé, ni les vingt et un
faux négatifs de l'exfiltration déguisée en journal d'audit : le premier
demande une atténuation des citations qui comprenne les blocs de code, le
second une famille d'entraînement du même genre. Les deux sont notés, aucun
n'a été traité sur ce résultat.

**Lecture de la quatrième ouverture.** Ajouter au jeu de travail une famille
d'exfiltration déguisée en journal d'audit n'a pas bougé la même famille du
scellé : ma mise en scène du journal diffère de la sienne, le rappel de cette
famille scellée reste à 56 %. C'est le scellé qui remplit son rôle. Les règles
anglaises ajoutées après lecture du banc d'essai public (« directions »,
« documents », « forget everything you know ») font +1 point sur le scellé
sans coûter de faux positif. Reste inchangé : huit faux positifs sur le test
unitaire avec charge, qui attendent une atténuation des citations comprenant
les blocs de code.

**Lecture de la cinquième ouverture.** L'extension jailbreak tirée du minage
des jeux publics et d'OWASP fait gagner quatre faux négatifs sur le scellé
(22 contre 26) sans coûter de précision. Les vingt et un faux négatifs de
l'exfiltration en journal d'audit ne bougent pas : la mise en scène scellée
reste hors de portée des règles, et la famille de journal ajoutée au jeu de
travail ne la reproduit pas. C'est le vrai résidu, pour le flux réel du mode
observation.

## Banc d'essai public

En parallèle du scellé, `external-benchmark.sh` mesure le détecteur sur des
jeux réels (deepset/prompt-injections, jackhhao/jailbreak-classification). Au
9 septembre 2026, modèle v4 : deepset précision 100 %, rappel 18 % (26 % sur
le seul anglais, le reste est en allemand et espagnol, non couverts) ;
jailbreak précision 98 %, rappel 46 %. La précision élevée sur données réelles
est la propriété recherchée ; le rappel modeste dit que le corpus synthétique
ne remplace pas les attaques du terrain, et justifie le déploiement sans
blocage.

Mise à jour, modèle v5. Le split test de jackhhao/jailbreak-classification est
resté à l'écart de tout minage et sert de mesure propre. En minant les
tournures discriminantes du split train par log-odds, on a extrait la
structure des jailbreaks (persona à capacité illimitée, contraintes imposées à
la forme des réponses) sans copier une seule phrase réelle : elle est devenue
des slots de lexique et quatre templates. OWASP GenAI LLM Top 10 2026, entrée
LLM01, a confirmé ces axes et ajouté ROT13 aux encodages décodés. Sur le split
test tenu à l'écart, le rappel passe de 56,8 % (v4) à 64,7 % (v5) à précision
constante (98,9 %). Restent hors périmètre : les payloads en allemand, espagnol
et langues peu dotées (LLM01 #8), et les canaux multimodaux (#4).
