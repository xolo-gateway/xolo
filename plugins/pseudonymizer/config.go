package main

import (
	"encoding/json"
	"fmt"
	"slices"

	goanon "github.com/bornholm/go-anon"
)

const configSchemaJSON = `{
  "type": "object",
  "properties": {
    "engine": {
      "type": "string",
      "title": "Moteur de détection",
      "description": "crf = modèle d'entités nommées de go-anon, un par langue, précis sur les messages courts. indecis = modèle de masquage d'indecis-ner, multilingue, qui trouve aussi les quasi-identifiants (âge, métier, proches…) et les données sensibles de l'article 9, mais masque aussi des termes techniques dans les demandes courtes : à réserver aux contenus riches en données personnelles.",
      "default": "crf",
      "enum": ["crf", "indecis"]
    },
    "cache_dir": {
      "type": "string",
      "title": "Répertoire de cache",
      "description": "Chemin local pour stocker les modèles téléchargés. Par défaut : répertoire cache système."
    },
    "manifest_url": {
      "type": "string",
      "title": "URL du manifest",
      "description": "URL du manifest des modèles disponibles. Par défaut : dépôt officiel go-anon. Moteur crf."
    },
    "model_dir": {
      "type": "string",
      "title": "Répertoire du modèle indecis",
      "description": "Dossier d'un modèle indecis déjà présent sur la machine, utilisé tel quel à la place du modèle téléchargé. Moteur indecis."
    },
    "model_url": {
      "type": "string",
      "title": "URL du modèle indecis",
      "description": "Adresse d'un miroir des fichiers du modèle. Par défaut : release d'indecis-ner. Les fichiers restent vérifiés par leur empreinte. Moteur indecis."
    },
    "offline": {
      "type": "boolean",
      "title": "Mode hors-ligne",
      "description": "Désactive les téléchargements réseau. Nécessite des modèles déjà en cache.",
      "default": false
    },
    "language": {
      "type": "string",
      "title": "Langue",
      "description": "Langue des messages à anonymiser. 'auto' détecte automatiquement la langue de la conversation, à partir de ses premiers messages utilisateur, parmi les modèles disponibles.",
      "default": "auto",
      "enum": ["auto", "fr", "en", "es"]
    },
    "fallback_language": {
      "type": "string",
      "title": "Langue de repli",
      "description": "Langue utilisée lorsque la détection automatique n'est pas fiable (texte trop court, langue non supportée).",
      "default": "fr",
      "enum": ["fr", "en", "es"]
    },
    "strategy": {
      "type": "string",
      "title": "Stratégie d'anonymisation",
      "description": "Mode de remplacement des entités : tag=⟦PERSON_1_a1b2c3⟧, redact=████, hash=⟦PER_a1b2⟧, consistent=numérotation cohérente. La stratégie hash exige une clé HMAC enregistrée sur le nœud : sans clé, les requêtes sont refusées.",
      "default": "tag",
      "enum": ["tag", "redact", "hash", "consistent"]
    },
    "bias": {
      "type": "number",
      "title": "Prudence",
      "description": "Biais de décodage du modèle indecis (0–10) : plus il est haut, moins il reste de données lisibles, et plus il y a de masques superflus. Moteur indecis.",
      "default": 3,
      "minimum": 0,
      "maximum": 10
    },
    "min_confidence": {
      "type": "number",
      "title": "Confiance minimale",
      "description": "Seuil de confiance NER (0.0–1.0). Les entités en-dessous du seuil sont ignorées.",
      "default": 0.30,
      "minimum": 0,
      "maximum": 1
    },
    "max_tokens": {
      "type": "integer",
      "title": "Tokens max par entité",
      "description": "Les entités dépassant ce nombre de tokens sont ignorées. 0 = pas de limite.",
      "default": 0,
      "minimum": 0
    },
    "min_runes": {
      "type": "integer",
      "title": "Caractères min par entité",
      "description": "Les entités comptant moins de caractères que ce seuil sont ignorées. Écarte les fragments d'un ou deux signes produits par un texte disloqué. 0 = pas de limite.",
      "default": 0,
      "minimum": 0
    },
    "skip_types": {
      "type": "array",
      "title": "Types à ignorer",
      "description": "Types d'entités à ne pas anonymiser. PER est le type de personne du moteur crf, PERSON celui du moteur indecis, qui y ajoute CODE, DEM, DATETIME et QUANTITY : l'un vaut pour l'autre.",
      "items": {
        "type": "string",
        "enum": ["PER","PERSON","CODE","LOC","ORG","DEM","DATETIME","QUANTITY","MISC","EMAIL","IPV4","IPV6","IBAN","SIRET","SIREN","PHONE","API_KEY","JWT","SECRET"]
      }
    },
    "blocklist": {
      "type": "object",
      "title": "Liste de blocage",
      "description": "Mots à ignorer par type d'entité. Ex: {\"PER\": [\"Monsieur\", \"Madame\"]}",
      "additionalProperties": {
        "type": "array",
        "items": {"type": "string"}
      }
    },
    "first_name_reclassify": {
      "type": "boolean",
      "title": "Reclassification des prénoms",
      "description": "Reclasse les entités LOC d'un seul token en PER si le token figure dans le gazetteer de prénoms.",
      "default": false
    },
    "merge": {
      "type": "boolean",
      "title": "Fusion des entités adjacentes",
      "description": "Fusionne les entités adjacentes de même type (ex : prénom + nom de famille).",
      "default": false
    },
    "name_completion": {
      "type": "boolean",
      "title": "Complétion des noms",
      "description": "Complète les entités PER partielles avec le token de nom de famille adjacent.",
      "default": false
    },
    "builtin_regex_patterns": {
      "type": "boolean",
      "title": "Patterns regex intégrés",
      "description": "Détecte automatiquement EMAIL, IPV4/6, IBAN, SIRET, SIREN, PHONE via regex.",
      "default": true
    },
    "builtin_secret_patterns": {
      "type": "boolean",
      "title": "Patterns secrets intégrés",
      "description": "Détecte automatiquement JWT, clés API (OpenAI, AWS, GitHub, Slack…) via regex.",
      "default": true
    },
    "siren_contextual": {
      "type": "boolean",
      "title": "SIREN contextuel",
      "description": "N'accepte un numéro SIREN que précédé d'un marqueur textuel (« SIREN 123456782 »). Réduit les faux positifs sur les identifiants à neuf chiffres au prix de quelques oublis.",
      "default": false
    },
    "process_attachments": {
      "type": "boolean",
      "title": "Traiter les pièces jointes documentaires",
      "description": "Extrait le texte des documents joints (PDF, DOCX, ODT, CSV/TSV, texte), le pseudonymise et le transmet au LLM à la place du fichier. Le fichier d'origine n'est jamais transmis.",
      "default": true
    },
    "unsupported_attachments": {
      "type": "string",
      "title": "Pièces jointes non traitables",
      "description": "Comportement face à un fichier dont le contenu ne peut pas être pseudonymisé (image, PDF scanné, format inconnu, fichier référencé à distance). block = refus de la requête, remove = retrait de la pièce jointe et avertissement dans la réponse.",
      "default": "block",
      "enum": ["block", "remove"]
    },
    "max_attachment_bytes": {
      "type": "integer",
      "title": "Taille max par pièce jointe (octets)",
      "description": "Les fichiers plus volumineux sont traités comme non traitables. 0 = pas de limite.",
      "default": 10485760,
      "minimum": 0
    },
    "max_attachment_chars": {
      "type": "integer",
      "title": "Caractères max extraits par pièce jointe",
      "description": "Le texte extrait au-delà de ce seuil est tronqué, et la troncature signalée au LLM. 0 = valeur par défaut (200 000).",
      "default": 200000,
      "minimum": 0
    },
    "inject_instruction": {
      "type": "boolean",
      "title": "Instruction de préservation des jetons",
      "description": "Ajoute une instruction système demandant au LLM de recopier les jetons de substitution (délimités par ⟦ et ⟧) sans les modifier. Ignoré pour la stratégie 'redact'.",
      "default": true
    },
    "verification": {
      "type": "boolean",
      "title": "Observer les fuites (sans bloquer)",
      "description": "Mesure les fuites de PII résiduelles après anonymisation sans bloquer la requête. Toute fuite est émise comme événement.",
      "default": true
    },
    "verification_strict": {
      "type": "boolean",
      "title": "Mode strict — bloque les requêtes sur fuite",
      "description": "Toute fuite détectée provoque l'échec de l'anonymisation (fail-closed).",
      "default": false
    },
    "verification_on_leak": {
      "type": "string",
      "title": "Comportement sur fuite (mode strict)",
      "description": "allow = passe-plat, événement sensible-data.leak émis. block = refus de la requête.",
      "default": "allow",
      "enum": ["allow", "block"]
    },
    "hash_scope": {
      "type": "string",
      "title": "Périmètre HMAC (stratégie hash)",
      "description": "Compartimente les pseudonymes HMAC. Vide = partage entre toutes les requêtes."
    }
  }
}`

// Config représente la configuration org-level du plugin.
type Config struct {
	// Engine choisit le moteur de détection : engineCRF ou engineIndecis.
	Engine string `json:"engine"`

	// Modelstore
	CacheDir    string `json:"cache_dir"`
	ManifestURL string `json:"manifest_url"`
	Offline     bool   `json:"offline"`
	// ModelDir désigne un modèle indecis déjà présent, utilisé tel quel.
	ModelDir string `json:"model_dir"`
	// ModelURL remplace l'adresse de la release d'indecis-ner.
	ModelURL string `json:"model_url"`
	// Bias est le biais de décodage des questions du modèle indecis (0–10).
	Bias float64 `json:"bias"`

	// Anonymisation
	Language              string              `json:"language"`
	FallbackLanguage      string              `json:"fallback_language"`
	Strategy              string              `json:"strategy"`
	MinConfidence         float64             `json:"min_confidence"`
	MaxTokens             int                 `json:"max_tokens"`
	MinRunes              int                 `json:"min_runes"`
	SkipTypes             []string            `json:"skip_types"`
	Blocklist             map[string][]string `json:"blocklist"`
	FirstNameReclassify   bool                `json:"first_name_reclassify"`
	Merge                 bool                `json:"merge"`
	NameCompletion        bool                `json:"name_completion"`
	BuiltinRegexPatterns  bool                `json:"builtin_regex_patterns"`
	BuiltinSecretPatterns bool                `json:"builtin_secret_patterns"`
	// SirenContextual remplace le pattern SIREN intégré par sa variante
	// contextuelle : le numéro n'est retenu que précédé d'un marqueur textuel.
	// Sans effet si BuiltinRegexPatterns est désactivé.
	SirenContextual   bool `json:"siren_contextual"`
	InjectInstruction bool `json:"inject_instruction"`

	// Pièces jointes documentaires
	// ProcessAttachments active l'extraction du texte des documents joints :
	// le texte est pseudonymisé puis transmis à la place du fichier, qui n'est
	// jamais relayé au LLM.
	ProcessAttachments bool `json:"process_attachments"`
	// UnsupportedAttachments pilote le sort d'un fichier dont le contenu ne
	// peut pas être pseudonymisé : "block" refuse la requête, "remove" retire
	// la pièce jointe et le signale dans la réponse.
	// Valeurs autorisées : "block", "remove". Défaut : "block".
	UnsupportedAttachments string `json:"unsupported_attachments"`
	// MaxAttachmentBytes borne la taille d'un fichier traité (0 = illimité).
	// Au-delà, la pièce jointe est considérée non traitable.
	MaxAttachmentBytes int `json:"max_attachment_bytes"`
	// MaxAttachmentChars borne le texte extrait d'un fichier (0 = illimité).
	// Au-delà, le texte est tronqué et la troncature signalée au LLM.
	MaxAttachmentChars int `json:"max_attachment_chars"`

	// Vérification de la sortie (go-anon v0.1+)
	// Verification active WithVerification : toute fuite résiduelle est
	// observée et émise comme événement, sans bloquer.
	Verification bool `json:"verification"`
	// VerificationStrict active WithStrictVerification : une fuite fait
	// échouer Anonymize avec *VerificationError.
	VerificationStrict bool `json:"verification_strict"`
	// VerificationOnLeak pilote le comportement du plugin quand le mode strict
	// détecte une fuite : "allow" passe-plat, "block" refuse la requête.
	// Valeurs autorisées : "allow", "block". Défaut : "allow".
	VerificationOnLeak string `json:"verification_on_leak"`

	// HashScope compartimente les pseudonymes HMAC (transmis à WithHashScope).
	// Vide = scope partagé entre toutes les requêtes de l'org.
	HashScope string `json:"hash_scope"`
}

func parseConfig(configJSON string) (Config, error) {
	cfg := defaultConfig()
	if configJSON == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Language == "" {
		cfg.Language = LanguageAuto
	}
	if cfg.FallbackLanguage == "" {
		cfg.FallbackLanguage = defaultLanguage
	}
	if cfg.Strategy == "" {
		cfg.Strategy = "tag"
	}
	if cfg.Engine == "" {
		cfg.Engine = engineCRF
	}
	switch cfg.Engine {
	case engineCRF, engineIndecis:
	default:
		return Config{}, fmt.Errorf("engine invalide : %q (attendu : crf|indecis)", cfg.Engine)
	}
	if cfg.Bias < 0 || cfg.Bias > maxBias {
		return Config{}, fmt.Errorf("bias invalide : %v (attendu : 0 à %v)", cfg.Bias, maxBias)
	}
	if cfg.VerificationOnLeak == "" {
		cfg.VerificationOnLeak = "allow"
	}
	switch cfg.VerificationOnLeak {
	case "allow", "block":
	default:
		return Config{}, fmt.Errorf("verification_on_leak invalide : %q (attendu : allow|block)", cfg.VerificationOnLeak)
	}
	if cfg.UnsupportedAttachments == "" {
		cfg.UnsupportedAttachments = "block"
	}
	switch cfg.UnsupportedAttachments {
	case "block", "remove":
	default:
		return Config{}, fmt.Errorf("unsupported_attachments invalide : %q (attendu : block|remove)", cfg.UnsupportedAttachments)
	}
	return cfg, nil
}

func defaultConfig() Config {
	return Config{
		Engine:                engineCRF,
		Bias:                  defaultBias,
		Language:              LanguageAuto,
		FallbackLanguage:      defaultLanguage,
		Strategy:              "tag",
		MinConfidence:         0.30,
		BuiltinRegexPatterns:  true,
		BuiltinSecretPatterns: true,
		InjectInstruction:     true,
		Verification:          true,
		VerificationOnLeak:    "allow",

		ProcessAttachments:     true,
		UnsupportedAttachments: "block",
		MaxAttachmentBytes:     defaultMaxAttachmentBytes,
		MaxAttachmentChars:     defaultMaxAttachmentChars,
	}
}

const (
	// defaultMaxAttachmentBytes borne le fichier décodé à 10 Mio : au-delà,
	// l'extraction coûte plus que ce qu'un contexte de LLM peut absorber.
	defaultMaxAttachmentBytes = 10 * 1024 * 1024
	// defaultMaxAttachmentChars borne le texte extrait, un ordre de grandeur
	// au-dessus de ce qu'une fenêtre de contexte courante accepte.
	defaultMaxAttachmentChars = 200_000
)

// strategyFromString converts the string strategy name to goanon.Strategy.
func strategyFromString(s string) goanon.Strategy {
	switch s {
	case "redact":
		return goanon.Redact
	case "hash":
		return goanon.Hash
	case "consistent":
		return goanon.Consistent
	default:
		return goanon.TagReplace
	}
}

// The detection engines.
const (
	engineCRF     = "crf"
	engineIndecis = "indecis"
)

const (
	// defaultBias leaves no direct identifier readable on the court rulings
	// of TAB in English and Spanish, 2 out of 366 in French, while masking
	// far less than indecis-caviardage's 5 in short requests.
	defaultBias = 3
	maxBias     = 10
)

// The types of the indecis model's "entities" question, which types each
// mask. LOC, ORG and MISC are named as go-anon's.
const (
	typePerson   goanon.EntityType = "PERSON"
	typeCode     goanon.EntityType = "CODE"
	typeDem      goanon.EntityType = "DEM"
	typeDatetime goanon.EntityType = "DATETIME"
	typeQuantity goanon.EntityType = "QUANTITY"
)

// allEntityTypes is the complete list of entity types the anonymizer knows about.
var allEntityTypes = []goanon.EntityType{
	goanon.TypePER, goanon.TypeLOC, goanon.TypeORG, goanon.TypeMISC,
	typePerson, typeCode, typeDem, typeDatetime, typeQuantity,
	goanon.TypeEMAIL, goanon.TypeIPV4, goanon.TypeIPV6, goanon.TypeIBAN,
	goanon.TypeSIRET, goanon.TypeSIREN, goanon.TypePHONE,
	goanon.TypeAPIKey, goanon.TypeJWT, goanon.TypeSecret,
}

// engineTypes lists the types the page offers for engine: those its model
// gives, then the identifiers and the secrets of the regular expressions.
func engineTypes(engine string) []goanon.EntityType {
	model := []goanon.EntityType{goanon.TypePER, goanon.TypeLOC, goanon.TypeORG, goanon.TypeMISC}
	if engine == engineIndecis {
		model = []goanon.EntityType{typePerson, typeCode, goanon.TypeLOC, goanon.TypeORG, typeDem, typeDatetime, typeQuantity, goanon.TypeMISC}
	}
	return append(model,
		goanon.TypeEMAIL, goanon.TypePHONE, goanon.TypeIBAN, goanon.TypeSIRET, goanon.TypeSIREN,
		goanon.TypeIPV4, goanon.TypeIPV6, goanon.TypeAPIKey, goanon.TypeJWT, goanon.TypeSecret)
}

// typeSkipped reports whether t, or the type naming the same thing for the
// other engine, is among skip.
func typeSkipped(skip []string, t goanon.EntityType) bool {
	for _, alias := range typeAliases(string(t)) {
		if slices.Contains(skip, alias) {
			return true
		}
	}
	return false
}

// skipTypesOf returns the types to skip once the page checked the types of
// checked among those it showed. The skipped types it did not show, those of
// the other engine for instance, are kept.
func skipTypesOf(base, shown, checked []string) []string {
	var shownAll []string
	for _, t := range shown {
		shownAll = append(shownAll, typeAliases(t)...)
	}
	var skip []string
	for _, t := range base {
		if !slices.Contains(shownAll, t) {
			skip = append(skip, t)
		}
	}
	for _, t := range shown {
		if !slices.Contains(checked, t) {
			skip = append(skip, t)
		}
	}
	return skip
}
