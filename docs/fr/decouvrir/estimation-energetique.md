# Estimation de l'énergie consommée par une requête LLM

## Vue d'ensemble

Xolo estime la consommation énergétique de chaque requête d'inférence en mode **boîte noire**. Il ne connaît pas la configuration réelle du datacenter et l'approche à partir des paramètres physiques du modèle et d'hypothèses calibrées sur le type d'infrastructure.

Le résultat est toujours une **plage** [min, max] accompagnée d'une **valeur médiane**, jamais un chiffre unique. L'énergie consommée par un LLM en production varie en effet d'un facteur 5 à 10 selon les conditions réelles.

Sur le [tableau de bord](../utilisation/tableau-de-bord.md), cette estimation alimente les métriques **Énergie** et **CO2**. Le **niveau d'infrastructure** d'un [fournisseur](./fournisseurs-modeles-pipeline.md) et les caractéristiques physiques d'un modèle sont les entrées principales du calcul.

## Entrées de l'algorithme

| Paramètre | Source | Description |
| --- | --- | --- |
| `activeParams` | Configuration du modèle | Nombre de paramètres actifs par token (ex : 7 milliards pour un 7B) |
| `inputTokens` | Requête API | Tokens envoyés au modèle (prompt) |
| `outputTokens` | Réponse API | Tokens générés par le modèle (complétion) |
| `tokPerSecLow/High` | Configuration ou heuristique | Débit de génération en tokens/s (optionnel) |
| `cloudTier` | Configuration du fournisseur | Catégorie d'infrastructure (hyperscaler, cloud majeur, petit fournisseur) |

## Étape 1 — Estimation du débit de tokens (tokens/s)

Le débit de génération (tokens par seconde) détermine la durée de la requête, et donc l'énergie calculée par la méthode TDP (_Thermal Design Power_, la puissance nominale du matériel), décrite à l'étape 3.

Si l'administrateur n'a pas fourni de valeur, Xolo utilise une **heuristique empirique** fondée sur la taille du modèle :

```
tps_base = 200 / (bParams ^ 0.4)
```

où `bParams` est le nombre de milliards de paramètres actifs.

Quelques valeurs typiques :

| Modèle | Paramètres actifs | tps estimé (base) |
| --- | --- | --- |
| 7B | 7 Md | ~92 tok/s |
| 13B | 13 Md | ~72 tok/s |
| 70B | 70 Md | ~37 tok/s |
| 400B | 400 Md | ~18 tok/s |

Pour construire la plage d'incertitude :

- **Scénario optimiste** : `tps = tps_base` (matériel récent, bon batching)
- **Scénario pessimiste** : `tps = tps_base × 0.5` (matériel saturé, moins optimisé)

> **Pourquoi l'exposant 0,4 ?** La loi d'échelle empirique indique que le temps par token croît plus lentement que linéairement avec la taille du modèle, car les architectures plus grandes bénéficient proportionnellement plus du parallélisme GPU.

## Étape 2 — Calcul de la durée de la requête

Une requête LLM se décompose en deux phases.

### Phase de prefill (traitement du prompt)

Le prefill traite **tous les tokens d'entrée en parallèle** sur le GPU. C'est une opération massivement parallèle, analogue à une multiplication de matrices dense. Mais le mécanisme d'attention a une complexité quadratique O(n²). Plus le contexte est long, plus le gain relatif de la parallélisation diminue.

Le facteur d'accélération dépend donc de la longueur du prompt :

```
speedup_prefill = 20 / (1 + inputTokens / 10000)
prefill_duration = inputTokens / (tps × speedup_prefill)
```

Valeurs typiques :

| Longueur du prompt | Accélération prefill | Durée prefill (tps=100) |
| --- | --- | --- |
| 100 tokens | ~19,8× | ~0,05 s |
| 1 000 tokens | ~18,2× | ~0,55 s |
| 5 000 tokens | ~13,3× | ~3,8 s |
| 20 000 tokens | ~6,7× | ~30 s |
| 50 000 tokens | ~3,3× | ~150 s |

### Phase de decode (génération de la réponse)

Le decode génère les tokens **un par un**. C'est la phase qui limite le temps total.

```
decode_duration = outputTokens / tps
```

### Durée totale

```
total_duration = prefill_duration + decode_duration
```

**Exemple** : modèle 7B, 1000 tokens en entrée, 200 en sortie, tps = 100.

- Accélération = 20 / (1 + 1000/10000) = **18,2×**
- Prefill : 1000 / (100 × 18,2) = **0,55 s**
- Decode : 200 / 100 = **2,0 s**
- Total : **2,55 s**

## Étape 3 — Méthode 1 : estimation par la puissance (basée TDP)

Cette méthode s'inspire de Ji & Jiang (2025). Plutôt que de compter des opérations, elle modélise la **puissance électrique** que les GPU consomment pendant la durée de la requête.

```
GPU_power = max(MinGPUWatts, WattsPerBParams × bParams)
E_TDP = GPU_power × duration × PUE × 1,20
```

| Terme | Signification |
| --- | --- |
| `MinGPUWatts` | Puissance GPU minimale par requête (W), plancher lié au batching |
| `WattsPerBParams` | Puissance en watts par milliard de paramètres actifs |
| `bParams` | Nombre de milliards de paramètres actifs |
| `duration` | Durée totale de la requête (prefill + decode), en secondes |
| `PUE` | _Power Usage Effectiveness_, surcoût du datacenter (refroidissement, alimentation…) |
| `× 1,20` | +20 % pour les surcoûts serveur (CPU, réseau, cache KV en mémoire HBM) |

**Pourquoi un plancher de puissance ?** Sans ce plancher, un modèle 7B chez un hyperscaler donnerait `0,10 × 7 = 0,7 W`, une valeur irréaliste. En pratique, chaque requête se voit allouer une fraction d'un GPU partiellement chargé, même avec un batching agressif. Le plancher représente la puissance de cette fraction.

**Pourquoi +20 % et pas +10 % ?** Le cache KV (mémoire HBM pour les activations d'attention) peut représenter 15 à 25 % de la puissance totale du serveur, une composante que le PUE ne couvre pas.

> **Remarque sur le double comptage** : `WattsPerBParams` capture implicitement une partie de la consommation de mémoire GPU (HBM). Le +20 % inclut une approximation du cache KV non couvert par ce terme. Si `WattsPerBParams` est affiné pour inclure explicitement la mémoire HBM, ce surcoût devrait être révisé à la baisse.

`WattsPerBParams` encode indirectement l'efficacité matérielle : un GPU H100 récent consomme moins par paramètre qu'un A100 ou un V100.

Paramètres par niveau. Le formulaire du fournisseur les nomme **Hyperscaler**, **Major Cloud** et **Small Provider**.

| Niveau | WattsPerBParams | MinGPUWatts (bas/haut) | Surcoût |
| --- | --- | --- | --- |
| Hyperscaler | 0,10 à 0,50 W/Md | 2 / 20 W | ×1,20 |
| Cloud majeur | 0,30 à 1,00 W/Md | 8 / 50 W | ×1,20 |
| Petit fournisseur | 0,50 à 2,00 W/Md | 20 / 100 W | ×1,20 |

## Étape 4 — Méthode 2 : estimation par opérations flottantes (FLOP)

Cette méthode compte le nombre d'opérations mathématiques effectuées, puis convertit en énergie via l'efficacité énergétique du matériel.

### Calcul du nombre de FLOP

Pour un transformer, chaque token nécessite environ `2 × N` opérations flottantes, où `N` est le nombre de paramètres actifs (une multiplication + une addition par paramètre).

```
FLOP_per_token = 2 × activeParams

FLOP_prefill = FLOP_per_token × inputTokens
FLOP_decode  = FLOP_per_token × outputTokens
FLOP_total   = FLOP_prefill + FLOP_decode

attentionFactor = 1 + inputTokens / (inputTokens + 5000)
FLOP_total      = FLOP_total × attentionFactor
```

> **Remarque** : le prefill est compté au **coût plein**, identique au decode. La parallélisation GPU réduit le _temps_ mais pas le nombre d'opérations effectuées.

**Facteur d'attention pour les longs contextes** : le mécanisme d'attention a une complexité O(n²) en prefill. Plus le prompt est long, plus la part des FLOP d'attention dans le total augmente. Le `attentionFactor` approxime ce surcoût sans les paramètres internes des couches :

| Longueur du prompt | attentionFactor | Surcoût FLOP |
| --- | --- | --- |
| 100 tokens | 1,02 | +2 % |
| 1 000 tokens | 1,17 | +17 % |
| 5 000 tokens | 1,50 | +50 % |
| 20 000 tokens | 1,80 | +80 % |
| 50 000 tokens | 1,91 | +91 % |

_Source : Ji & Jiang (2025) ; approximation simplifiée sans les paramètres de couches._

### Conversion en énergie

```
E_FLOP = FLOP_total / (efficiency × 10⁹) × PUE × 1,20
```

où `efficiency` est en GFLOP/J (gigaflops par joule).

Plages d'efficacité par niveau :

| Niveau | Efficacité min | Efficacité max |
| --- | --- | --- |
| Hyperscaler (Google, Meta…) | 700 GFLOP/J | 1000 GFLOP/J |
| Cloud majeur (AWS, OVH…) | 350 GFLOP/J | 700 GFLOP/J |
| Petit fournisseur | 150 GFLOP/J | 350 GFLOP/J |

> La borne basse hyperscaler est relevée à 700 GFLOP/J (contre 600 auparavant). Les GPU H100/H200 déployés en 2025 atteignent ≥ 700 GFLOP/J effectifs en conditions d'inférence typiques. La borne haute reste à 1000 GFLOP/J. Une valeur de 1200+ GFLOP/J supposerait du matériel Blackwell avec quantification FP8 et un batching parfait, ce qui est trop optimiste pour une valeur générique.

## Étape 5 — Construction de l'enveloppe [min, max]

Les deux méthodes donnent chacune un résultat différent. La construction de l'enveloppe dépend de l'état du **plancher TDP**, actif ou non.

### Cas 1 — Plancher TDP actif (petits modèles)

Quand `WattsPerBParams_bas × bParams < MinGPUWatts_bas`, le plancher `MinGPUWatts` est actif dans le scénario optimiste. La méthode TDP mesure alors un **surcoût d'infrastructure** (batching, matériel minimal), pas la physique du modèle. Elle n'apporte plus d'information et Xolo n'utilise que la méthode **FLOP** :

```
Si WattsPerBParams_bas × bParams < MinGPUWatts_bas :
    E_bas    = FLOP_bas
    E_haut   = FLOP_haut
    E_median = FLOP_median = √(FLOP_bas × FLOP_haut)
```

### Cas 2 — Hybride (modèles de taille normale)

Quand le plancher n'est pas actif, Xolo prend l'**enveloppe conservatrice** des deux méthodes :

```
E_bas  = min(TDP_bas,  FLOP_bas)
E_haut = max(TDP_haut, FLOP_haut)

TDP_median  = √(TDP_bas  × TDP_haut)   ← médiane de la méthode TDP seule
FLOP_median = √(FLOP_bas × FLOP_haut)  ← médiane de la méthode FLOP seule

E_median = √(TDP_median × FLOP_median) ← moyenne géométrique des médianes par méthode
```

> **Pourquoi E_median à partir des médianes plutôt que de l'enveloppe ?** `√(E_bas × E_haut)` serait dominé par l'enveloppe la plus large (souvent TDP_haut), donnant une médiane trop pessimiste. Calculer la médiane de chaque méthode séparément, puis prendre leur moyenne géométrique, produit une valeur centrale plus représentative du consensus entre les deux approches.

> **Signal de divergence** : si `TDP_median / FLOP_median > 5` ou `< 0,2`, les deux méthodes divergent. Cela indique une forte incertitude, souvent due à des paramètres `tokPerSec` ou `activeParams` mal calibrés.

- `E_bas` : le scénario le plus favorable retenu
- `E_haut` : le scénario le plus défavorable retenu
- `E_median` : valeur centrale, calculée comme **moyenne géométrique** (pas arithmétique) car l'incertitude est multiplicative. Un facteur d'erreur s'estime avec plus de confiance qu'une différence absolue

> **Pourquoi combiner deux méthodes ?** La méthode TDP est plus précise quand le débit de tokens est connu de façon fiable ; la méthode FLOP est plus stable et indépendante du temps de calcul. Leur combinaison couvre mieux l'incertitude réelle. Le repli sur FLOP seul évite que le plancher d'infrastructure (une constante par niveau, indépendante du modèle) ne gonfle artificiellement l'enveloppe pour les petits modèles.

## Niveaux d'infrastructure (CloudTier)

Les paramètres `WattsPerBParams`, `MinGPUWatts` et `PUE` dépendent du type de datacenter. Xolo propose trois niveaux.

### Hyperscaler (Google, Microsoft, Meta)

- PUE : 1,05 à 1,15 _(refroidissement très optimisé)_
- Utilisation GPU : 50 à 80 %
- WattsPerBParams : 0,10 à 0,50 W/Md de paramètres
- MinGPUWatts : 2 à 20 W _(batching agressif ~100 requêtes/GPU à léger ~10 requêtes/GPU)_

### Cloud majeur (AWS, OVH, CoreWeave)

- PUE : 1,10 à 1,40
- Utilisation GPU : 30 à 60 %
- WattsPerBParams : 0,30 à 1,00 W/Md de paramètres
- MinGPUWatts : 8 à 50 W

### Petit fournisseur (startups, régional)

- PUE : 1,20 à 1,60 _(moins d'optimisation)_
- Utilisation GPU : 15 à 40 %
- WattsPerBParams : 0,50 à 2,00 W/Md de paramètres
- MinGPUWatts : 20 à 100 W _(faible batching, souvent un GPU dédié par requête)_

> **PUE (Power Usage Effectiveness)** : rapport entre l'énergie totale du datacenter et l'énergie consommée par les seuls serveurs. Un PUE de 1,0 serait parfait (impossible en pratique). Un PUE de 1,5 signifie que pour chaque watt consommé par les GPU, 0,5 W supplémentaire va au refroidissement et à la distribution d'énergie.

## Étape 6 — Conversion en CO₂

L'énergie électrique n'a pas le même impact carbone selon son lieu de production. Xolo calcule trois valeurs :

```
CO₂ (g) = énergie (Wh) × intensité carbone (gCO₂/Wh)
```

| Scénario | Intensité carbone | Contexte |
| --- | --- | --- |
| **France** | 0,027 gCO₂/Wh | Mix nucléaire |
| Suède | 0,045 gCO₂/Wh | Mix hydraulique/nucléaire |
| Moyenne UE | 0,276 gCO₂/Wh | Mix européen |
| Monde | 0,475 gCO₂/Wh | _(valeur par défaut)_ |
| Gaz naturel | 0,490 gCO₂/Wh | Centrale à gaz |
| Charbon | 0,960 gCO₂/Wh | _(borne haute)_ |

L'affichage présente toujours les trois valeurs : l'intensité choisie, la borne France (meilleur cas), la borne charbon (pire cas).

## Exemple complet

**Requête** : modèle 7B, 1000 tokens en entrée, 200 en sortie, hébergé chez un hyperscaler.

### Étape 1 — Débit

```
tps_base = 200 / 7^0,4 ≈ 91,8 tok/s
tps_optimiste = 91,8 tok/s
tps_pessimiste = 91,8 × 0,5 = 45,9 tok/s
```

### Étape 2 — Durées

```
accélération = 20 / (1 + 1000/10000) = 18,18×

Optimiste : prefill = 1000/(91,8×18,18) = 0,60 s  |  decode = 200/91,8 = 2,18 s  → total = 2,78 s
Pessimiste : prefill = 1000/(45,9×18,18) = 1,20 s  |  decode = 200/45,9 = 4,36 s  → total = 5,55 s
```

_Remarque : l'accélération vaut 20/(1+1000/10000) = 18,18 dans les deux cas, car elle ne dépend que de la longueur du prompt, pas du tps._

### Étape 3 — TDP (préréglage hyperscaler)

```
GPU_power_bas  = max(2 W, 0,10×7) = max(2, 0,7) = 2 W   ← plancher actif
GPU_power_haut = max(20 W, 0,50×7) = max(20, 3,5) = 20 W ← plancher actif

TDP_bas    = 2 W × 2,78 s × 1,05 × 1,20 = 7,0 J   ≈ 1,9 mWh
TDP_haut   = 20 W × 5,55 s × 1,15 × 1,20 = 153 J  ≈ 42,6 mWh
TDP_median = √(7,0 × 153) ≈ 32,8 J ≈ 9,1 mWh
```

### Étape 4 — FLOP

```
FLOP_total = 2 × 7×10⁹ × (1000 + 200) = 1,68×10¹³ FLOP

attentionFactor = 1 + 1000/(1000+5000) ≈ 1,167
FLOP_total (ajusté) = 1,68×10¹³ × 1,167 ≈ 1,96×10¹³ FLOP

FLOP_bas  = 1,96×10¹³ / (1000×10⁹) × 1,05 × 1,20 ≈ 24,7 J  ≈ 6,9 mWh
FLOP_haut = 1,96×10¹³ / (700×10⁹)  × 1,15 × 1,20 ≈ 38,6 J  ≈ 10,7 mWh
FLOP_median = √(24,7 × 38,6) ≈ 30,9 J ≈ 8,6 mWh
```

### Étape 5 — Enveloppe et cohérence inter-méthodes

```
Détection du plancher : WattsPerBParams_bas × 7 = 0,10 × 7 = 0,7 W < MinGPUWatts_bas = 2 W
→ plancher TDP actif → repli sur FLOP seul

E_bas    = FLOP_bas    = 24,7 J  ≈ 6,9 mWh
E_haut   = FLOP_haut   = 38,6 J  ≈ 10,7 mWh
E_median = FLOP_median = 30,9 J  ≈ 8,6 mWh

Étalement : 10,7 / 6,9 ≈ ×1,5  (contre ×22 avec l'enveloppe hybride brute)

Ratio TDP_median / FLOP_median = 32,8 / 30,9 ≈ 1,06  ← excellente cohérence (diagnostic retenu)
```

### Étape 6 — CO₂ (mix mondial, 0,475 gCO₂/Wh)

```
CO₂_median ≈ 8,58 mWh × 0,475 ≈ 4,1 mg CO₂
           (même énergie médiane : 8,58 × 0,027 ≈ 0,23 mg en France → 8,58 × 0,96 ≈ 8,2 mg au charbon)
```

## Limites et mises en garde sur l'interprétation

1. **Inférence en boîte noire** : le matériel réel, le niveau de batching ou la quantification utilisée ne sont pas connus. L'incertitude réelle peut dépasser un facteur 10.

2. **Modèles MoE** : pour les architectures _Mixture of Experts_ (comme Mixtral ou GPT-4), `activeParams` doit représenter les paramètres _actifs par token_, pas le total. Un modèle de 56 milliards de paramètres au total avec 8 experts et un routage top-2 a environ 14 milliards de paramètres actifs par token.

3. **Périmètre limité à l'inférence** : l'estimation ne couvre pas l'entraînement, le stockage des données ni le réseau côté client.

4. **Débit de tokens** : si le fournisseur est configuré avec des valeurs `tokPerSecLow/High`, elles remplacent l'heuristique et améliorent la précision de la méthode TDP.

5. **Intensité carbone** : la valeur par défaut (mix mondial, 0,475 gCO₂/Wh) est une approximation grossière. Pour un fournisseur hébergé en France, l'impact réel est environ 18 fois plus faible.

## Références

Les sources listées ci-dessous ont directement influencé les choix de modélisation. Les sections pertinentes sont indiquées pour chacune.

**[1] Ji, Z. & Jiang, P. (2025)**
_A systematic review of electricity demand for large language models: evaluations, challenges, and solutions._
https://doi.org/10.1016/j.rser.2025.116159

Contributions clés utilisées dans cet algorithme :

- Section 2.2.2 (_approche de mesure en ligne_) : fondement de la méthode basée TDP. `E = N_GPU × TDP × heures` est plus fiable que le comptage théorique de FLOP.
- Section 2.2.3 et tableau A1 : valeurs d'intensité carbone par pays/source (France 0,027, Chine 0,5366, moyenne mondiale 0,475 kgCO₂/kWh).
- Analyse prefill/decode : confirmation que le coût FLOP par token est identique entre prefill et decode. Seule la _durée_ diffère (parallélisation). Corrige l'ancien facteur 0,3 utilisé dans la version initiale.
- Variabilité de la puissance GPU : de 1,252 à 2,735 kW selon le niveau de charge.
- PUE observés : 1,05 pour Falcon Computing (Falcon, Mixtral), jusqu'à 1,2+ pour des fournisseurs moins optimisés.

**[2] International Energy Agency (2024)**
_Electricity 2024 — Analysis and forecast to 2026._
IEA, Paris. https://www.iea.org/reports/electricity-2024

Contribution : estimation d'ordre de grandeur de 0,001 à 0,01 kWh (1 à 10 mWh) par requête ChatGPT, utilisée comme borne de plausibilité pour valider les résultats de l'estimateur.

**[3] Luccioni, A. S., Viguier, S., & Ligozat, A.-L. (2023)**
_Power Hungry Processing: Watts Driving the Cost of Generative AI Deployment?_
arXiv:2311.16863. https://arxiv.org/abs/2311.16863

Contribution : mesures empiriques de la consommation énergétique de modèles génératifs en production (via wattmètre), utilisées pour calibrer les ordres de grandeur et valider la cohérence de l'estimation.

**[4] SemiAnalysis (2023)**
_Inference Race To The Bottom - Make It Up On Volume?_
SemiAnalysis Newsletter, 18 décembre 2023, https://newsletter.semianalysis.com/p/inference-race-to-the-bottom-make

Contribution : données empiriques sur l'efficacité énergétique des GPU en production (A100 : ~300 à 500 GFLOP/J effectifs ; H100 : ~500 à 1000 GFLOP/J effectifs), l'impact du batching sur la consommation par token, et les coûts d'inférence par niveau d'infrastructure. Ces données ont servi à calibrer les plages `WattsPerBParams` et les efficacités GFLOP/J des préréglages.

**[5] Epoch AI (2024)**
_The Rising Costs of Training Frontier AI Models._
Epoch AI Research. https://arxiv.org/abs/2405.21015v2

Contribution : méthodologie de comptage FLOP pour l'inférence (la règle `2 × N` FLOP par passe avant pour un transformer dense) et la relation entre taille du modèle et consommation ; utilisée pour la méthode basée FLOP.

### Données de référence complémentaires

| Donnée | Valeur | Source |
| --- | --- | --- |
| Consommation d'une recherche Google | ~0,3 Wh | Google Environmental Report 2023 |
| Charge complète d'un smartphone | ~14 Wh | Estimations constructeurs (batterie ~4 000 mAh à 3,7 V) |
| Consommation d'une ampoule LED 10 W | 10 W | Physique de base |
| 1 kgCO₂/kWh = 1 gCO₂/Wh | — | Équivalence dimensionnelle directe |
