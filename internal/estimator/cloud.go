package estimator

import (
	"fmt"
	"math"
)

// InferenceRequest represents tokens sent/received in a single LLM request.
type InferenceRequest struct {
	InputTokens  int
	OutputTokens int
}

func (r InferenceRequest) TotalTokens() int {
	return r.InputTokens + r.OutputTokens
}

// CloudTier represents the infrastructure category of a cloud provider.
// Values: 0 = Hyperscaler, 1 = MajorCloud, 2 = SmallProvider.
type CloudTier int

const (
	// TierHyperscaler : Google, Microsoft, Meta — PUE ~1.1, hardware dernier cri,
	// batching agressif, quantization optimisée.
	TierHyperscaler CloudTier = iota

	// TierMajorCloud : AWS, OVH, CoreWeave — bon hardware,
	// optimisation correcte, PUE ~1.2-1.4.
	TierMajorCloud

	// TierSmallProvider : startups, providers régionaux — hardware variable,
	// moins d'optimisation, PUE ~1.3-1.6.
	TierSmallProvider
)

func (t CloudTier) String() string {
	switch t {
	case TierHyperscaler:
		return "Hyperscaler (Google, Microsoft, Meta)"
	case TierMajorCloud:
		return "Major Cloud (AWS, OVH, CoreWeave)"
	case TierSmallProvider:
		return "Small Provider"
	default:
		return fmt.Sprintf("CloudTier(%d)", int(t))
	}
}

// CloudPreset encapsule les hypothèses d'infrastructure pour un provider cloud.
// Les valeurs sont des fourchettes [Low, High] pour produire une estimation min/max.
// Sources : Ji & Jiang (2025), IEA "Electricity 2024", SemiAnalysis (2023).
type CloudPreset struct {
	Tier    CloudTier
	PUELow  float64
	PUEHigh float64
	// GPUUtilizationLow/High : fraction d'utilisation GPU (0-1).
	GPUUtilizationLow  float64
	GPUUtilizationHigh float64
	// WattsPerBParamsLow/High : puissance en W par milliard de paramètres actifs.
	WattsPerBParamsLow  float64
	WattsPerBParamsHigh float64
	// MinGPUWattsLow/High : puissance GPU minimale par requête (W), reflétant le batching typique.
	// Low = batching agressif (batch ~100), High = batching léger (batch ~10).
	// Évite la sous-estimation pour les petits modèles (ex : 7B donnerait 0.7W sans plancher).
	MinGPUWattsLow  float64
	MinGPUWattsHigh float64
}

// Presets empiriques (Ji & Jiang 2025).
//
// Références :
//   - Ji & Jiang (2025) "Energy Consumption of LLM Inference"
//   - IEA "Electricity 2024" : ~0.001-0.01 kWh par requête ChatGPT
//   - SemiAnalysis "Inference Race To The Bottom - Make It Up On Volume?" (2023)
var (
	PresetHyperscaler = CloudPreset{
		Tier:                TierHyperscaler,
		PUELow:              1.05,
		PUEHigh:             1.15,
		GPUUtilizationLow:   0.50,
		GPUUtilizationHigh:  0.80,
		WattsPerBParamsLow:  0.10,
		WattsPerBParamsHigh: 0.50,
		MinGPUWattsLow:      2,
		MinGPUWattsHigh:     20,
	}

	PresetMajorCloud = CloudPreset{
		Tier:                TierMajorCloud,
		PUELow:              1.10,
		PUEHigh:             1.40,
		GPUUtilizationLow:   0.30,
		GPUUtilizationHigh:  0.60,
		WattsPerBParamsLow:  0.30,
		WattsPerBParamsHigh: 1.00,
		MinGPUWattsLow:      8,
		MinGPUWattsHigh:     50,
	}

	PresetSmallProvider = CloudPreset{
		Tier:                TierSmallProvider,
		PUELow:              1.20,
		PUEHigh:             1.60,
		GPUUtilizationLow:   0.15,
		GPUUtilizationHigh:  0.40,
		WattsPerBParamsLow:  0.50,
		WattsPerBParamsHigh: 2.00,
		MinGPUWattsLow:      20,
		MinGPUWattsHigh:     100,
	}
)

// CloudEnergyRange représente une estimation avec fourchette d'incertitude.
type CloudEnergyRange struct {
	// Fourchette basse (infrastructure très optimisée)
	Low CloudEnergyPoint
	// Fourchette haute (infrastructure moins optimisée)
	High CloudEnergyPoint
	// Estimation médiane (moyenne géométrique)
	Mid CloudEnergyPoint
	// Midpoints individuels des deux méthodes, pour évaluer leur cohérence.
	// Si TDPMidWh / FLOPMidWh > 5 ou < 0.2, les méthodes divergent significativement.
	TDPMidWh  float64
	FLOPMidWh float64
	// TDPFloorActive indique que le plancher MinGPUWatts était actif lors du calcul,
	// ce qui signifie que la méthode TDP n'est pas informative pour ce modèle.
	// Dans ce cas, Low/High/Mid sont calculés depuis la méthode FLOP uniquement.
	TDPFloorActive bool
}

// CloudEnergyPoint est un point d'estimation unique.
type CloudEnergyPoint struct {
	TotalJoules    float64
	TotalWh        float64
	TotalKWh       float64
	JoulesPerToken float64
	WhPerToken     float64
	DurationMs     float64
	Equivalences   Equivalences
}

// CloudEstimatorOption is a functional option for CloudEstimator.
type CloudEstimatorOption func(*CloudEstimator)

// WithCarbonIntensity sets the carbon intensity used for CO₂ estimation.
func WithCarbonIntensity(ci CarbonIntensity) CloudEstimatorOption {
	return func(ce *CloudEstimator) {
		ce.carbonIntensity = ci
	}
}

// CloudEstimator estime la consommation d'un modèle cloud en boîte noire.
type CloudEstimator struct {
	preset          CloudPreset
	carbonIntensity CarbonIntensity
}

// NewCloudEstimator crée un estimateur cloud à partir d'un tier.
func NewCloudEstimator(tier CloudTier, opts ...CloudEstimatorOption) *CloudEstimator {
	var preset CloudPreset
	switch tier {
	case TierHyperscaler:
		preset = PresetHyperscaler
	case TierMajorCloud:
		preset = PresetMajorCloud
	default:
		preset = PresetSmallProvider
	}
	ce := &CloudEstimator{
		preset:          preset,
		carbonIntensity: CarbonIntensityWorldAvg,
	}
	for _, o := range opts {
		o(ce)
	}
	return ce
}

// EstimateFromParams estime la consommation pour un modèle dont on connaît
// (ou estime) le nombre de paramètres actifs.
// tokPerSecLow/High : débit tokens/s connu (0 = auto-estimation heuristique).
func (ce *CloudEstimator) EstimateFromParams(
	activeParams float64,
	req InferenceRequest,
	tokPerSecLow, tokPerSecHigh float64,
) CloudEnergyRange {
	if req.TotalTokens() == 0 {
		zero := newCloudPoint(0, 0, 0, ce.carbonIntensity)
		return CloudEnergyRange{Low: zero, High: zero, Mid: zero}
	}

	// Auto-estimation du débit si non fourni.
	if tokPerSecLow <= 0 {
		tokPerSecLow = estimateTokensPerSec(activeParams, true) // optimiste = plus rapide
	}
	if tokPerSecHigh <= 0 {
		tokPerSecHigh = estimateTokensPerSec(activeParams, false) // pessimiste = plus lent
	}

	totalTokens := float64(req.TotalTokens())

	// Durées (en secondes)
	lowDurationSec := decodeDuration(req.OutputTokens, tokPerSecLow) + prefillDuration(req.InputTokens, tokPerSecLow)
	highDurationSec := decodeDuration(req.OutputTokens, tokPerSecHigh) + prefillDuration(req.InputTokens, tokPerSecHigh)

	// Méthode 1 : TDP-based (Ji & Jiang 2025)
	tdpLow, tdpHigh := ce.tdpBasedEstimate(activeParams, lowDurationSec, highDurationSec)

	// Méthode 2 : FLOP-based corrigé (prefill au coût plein)
	flopLow, flopHigh := ce.flopBasedEstimate(activeParams, req)

	// E_mid calculé depuis les midpoints par méthode (et non depuis l'enveloppe globale),
	// pour mieux refléter le centre de gravité de chaque méthode indépendamment.
	tdpMidJoules := geometricMean(tdpLow, tdpHigh)
	flopMidJoules := geometricMean(flopLow, flopHigh)

	// Détecter si le plancher MinGPUWatts est actif (puissance modèle < plancher infrastructure).
	// Dans ce cas, la méthode TDP reflète l'overhead d'infrastructure, pas la physique du modèle.
	bParams := activeParams / 1e9
	tdpFloorActive := ce.preset.WattsPerBParamsLow*bParams < ce.preset.MinGPUWattsLow

	var finalLow, finalHigh, finalMid float64
	if tdpFloorActive {
		// FLOP seul : enveloppe compacte et physiquement fondée
		finalLow = flopLow
		finalHigh = flopHigh
		finalMid = flopMidJoules
	} else {
		// Hybride : enveloppe conservative prenant le pire et le meilleur des deux méthodes
		finalLow = math.Min(tdpLow, flopLow)
		finalHigh = math.Max(tdpHigh, flopHigh)
		finalMid = geometricMean(tdpMidJoules, flopMidJoules)
	}

	return CloudEnergyRange{
		Low:            newCloudPoint(finalLow, totalTokens, lowDurationSec*1000, ce.carbonIntensity),
		High:           newCloudPoint(finalHigh, totalTokens, highDurationSec*1000, ce.carbonIntensity),
		Mid:            newCloudPoint(finalMid, totalTokens, (lowDurationSec+highDurationSec)/2*1000, ce.carbonIntensity),
		TDPMidWh:       tdpMidJoules / 3600.0,
		FLOPMidWh:      flopMidJoules / 3600.0,
		TDPFloorActive: tdpFloorActive,
	}
}

// tdpBasedEstimate calcule l'énergie via E = max(MinGPUWatts, WattsPerBParams×bParams) × duration × PUE × 1.20.
// Le plancher MinGPUWatts évite la sous-estimation pour les petits modèles en l'absence de mesure réelle.
func (ce *CloudEstimator) tdpBasedEstimate(activeParams, lowDurationSec, highDurationSec float64) (low, high float64) {
	bParams := activeParams / 1e9
	powerLow := math.Max(ce.preset.MinGPUWattsLow, ce.preset.WattsPerBParamsLow*bParams)
	powerHigh := math.Max(ce.preset.MinGPUWattsHigh, ce.preset.WattsPerBParamsHigh*bParams)
	lowJoules := powerLow * lowDurationSec * ce.preset.PUELow * 1.20
	highJoules := powerHigh * highDurationSec * ce.preset.PUEHigh * 1.20
	return lowJoules, highJoules
}

// flopBasedEstimate calcule l'énergie via FLOP totaux (prefill au coût plein, sans facteur 0.3).
func (ce *CloudEstimator) flopBasedEstimate(activeParams float64, req InferenceRequest) (low, high float64) {
	flopPerToken := 2.0 * activeParams
	// Prefill au coût plein (parallélisation GPU — pas de facteur 0.3)
	prefillFLOP := flopPerToken * float64(req.InputTokens)
	decodeFLOP := flopPerToken * float64(req.OutputTokens)
	totalFLOP := prefillFLOP + decodeFLOP

	// Surcoût de l'attention pour les longs contextes (approximation basée sur la longueur du prompt).
	// Pour 100 tokens : +2%, pour 5000 tokens : +50%, pour 50000 tokens : +91%.
	// Source : Ji & Jiang (2025) ; approximation simplifiée sans paramètres de couche.
	attentionFactor := 1.0 + float64(req.InputTokens)/(float64(req.InputTokens)+5000.0)
	totalFLOP *= attentionFactor

	effLow, effHigh := flopEfficiencyRange(ce.preset.Tier)

	// Optimiste : haute efficacité, bas PUE
	lowJoules := totalFLOP / (effHigh * 1e9) * ce.preset.PUELow * 1.20
	// Pessimiste : basse efficacité, haut PUE
	highJoules := totalFLOP / (effLow * 1e9) * ce.preset.PUEHigh * 1.20

	return lowJoules, highJoules
}

// flopEfficiencyRange retourne la fourchette d'efficacité FLOP/J pour un tier donné.
func flopEfficiencyRange(tier CloudTier) (low, high float64) {
	switch tier {
	case TierHyperscaler:
		return 700, 1000 // H100/H200 en 2025 ≥ 700 GFLOP/J effectifs
	case TierMajorCloud:
		return 350, 700
	default: // TierSmallProvider
		return 150, 350
	}
}

// estimateTokensPerSec heuristique : 200 / bParams^0.4, soit 7B→~92 tok/s, 70B→~37, 400B→~18.
// optimistic=true retourne une vitesse nominale, false retourne 50% de la vitesse nominale.
func estimateTokensPerSec(activeParams float64, optimistic bool) float64 {
	bParams := activeParams / 1e9
	base := 200.0 / math.Pow(bParams, 0.4)
	if optimistic {
		return base
	}
	return base * 0.5
}

// decodeDuration retourne la durée decode en secondes (séquentiel).
func decodeDuration(outputTokens int, tps float64) float64 {
	if tps <= 0 {
		return 0
	}
	return float64(outputTokens) / tps
}

// prefillSpeedup retourne le facteur de parallélisation du prefill par rapport au decode.
// Varie de ~20× pour un prompt court à ~2× pour un très long contexte (complexité quadratique
// de l'attention : O(n²) en prefill vs O(n) en decode).
func prefillSpeedup(inputTokens int) float64 {
	return 20.0 / (1.0 + float64(inputTokens)/10000.0)
}

// prefillDuration retourne la durée prefill en secondes avec facteur dynamique.
func prefillDuration(inputTokens int, tps float64) float64 {
	if tps <= 0 || inputTokens == 0 {
		return 0
	}
	return float64(inputTokens) / (tps * prefillSpeedup(inputTokens))
}

func newCloudPoint(joules, totalTokens, durationMs float64, ci CarbonIntensity) CloudEnergyPoint {
	wh := joules / 3600.0
	pt := CloudEnergyPoint{
		TotalJoules:  joules,
		TotalWh:      wh,
		TotalKWh:     wh / 1000.0,
		DurationMs:   durationMs,
		Equivalences: computeEquivalences(wh, ci),
	}
	if totalTokens > 0 {
		pt.JoulesPerToken = joules / totalTokens
		pt.WhPerToken = wh / totalTokens
	}
	return pt
}

func geometricMean(a, b float64) float64 {
	if a <= 0 || b <= 0 {
		return (a + b) / 2
	}
	return math.Sqrt(a * b)
}
