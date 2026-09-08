package main

import "encoding/json"

// Category is one possible answer, with the sentence that tells the model
// what belongs in it.
type Category struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type Config struct {
	// Model is the proxy name used when the model_name port is not connected.
	Model string `json:"model"`
	// Categories the model must choose from.
	Categories []Category `json:"categories"`
	// Instructions are appended to the system prompt (tie-breaking rules, language…).
	Instructions string `json:"instructions"`
	// FallbackCategory is answered when the call fails or the answer is unusable.
	FallbackCategory string `json:"fallback_category"`
	// IncludeHistory sends an excerpt of the earlier conversation along with the request.
	IncludeHistory bool `json:"include_history"`
	// MaxContextChars bounds the text sent to the model.
	MaxContextChars int `json:"max_context_chars"`
	// MaxTokens bounds the answer; a JSON verdict needs a few dozen.
	MaxTokens int `json:"max_tokens"`
	// Temperature of the classification call; 0 for reproducible answers.
	Temperature float64 `json:"temperature"`
	// TimeoutSeconds bounds the call; on timeout the fallback category is answered.
	TimeoutSeconds float64 `json:"timeout_seconds"`
}

func defaultConfig() Config {
	return Config{
		Categories: []Category{
			{Name: "code", Description: "writing, fixing, explaining or reviewing source code, shell commands, configuration"},
			{Name: "math", Description: "calculations, equations, proofs, statistics"},
			{Name: "writing", Description: "drafting, rewriting, translating or summarizing text"},
			{Name: "analysis", Description: "comparing options, evaluating a situation, explaining causes, recommending"},
			{Name: "factual", Description: "a short question with a known factual answer"},
			{Name: "conversation", Description: "greetings, small talk, acknowledgements"},
		},
		FallbackCategory: "unknown",
		MaxContextChars:  2000,
		MaxTokens:        120,
		Temperature:      0,
		TimeoutSeconds:   15,
	}
}

func parseConfig(raw string) Config {
	cfg := defaultConfig()
	if raw == "" || raw == "{}" {
		return cfg
	}
	var aux struct {
		Model            *string     `json:"model"`
		Categories       *[]Category `json:"categories"`
		Instructions     *string     `json:"instructions"`
		FallbackCategory *string     `json:"fallback_category"`
		IncludeHistory   *bool       `json:"include_history"`
		MaxContextChars  *int        `json:"max_context_chars"`
		MaxTokens        *int        `json:"max_tokens"`
		Temperature      *float64    `json:"temperature"`
		TimeoutSeconds   *float64    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal([]byte(raw), &aux); err != nil {
		return cfg
	}
	if aux.Model != nil {
		cfg.Model = *aux.Model
	}
	if aux.Categories != nil {
		cats := make([]Category, 0, len(*aux.Categories))
		for _, c := range *aux.Categories {
			if c.Name != "" {
				cats = append(cats, c)
			}
		}
		cfg.Categories = cats
	}
	if aux.Instructions != nil {
		cfg.Instructions = *aux.Instructions
	}
	if aux.FallbackCategory != nil && *aux.FallbackCategory != "" {
		cfg.FallbackCategory = *aux.FallbackCategory
	}
	if aux.IncludeHistory != nil {
		cfg.IncludeHistory = *aux.IncludeHistory
	}
	if aux.MaxContextChars != nil && *aux.MaxContextChars > 0 {
		cfg.MaxContextChars = *aux.MaxContextChars
	}
	if aux.MaxTokens != nil && *aux.MaxTokens > 0 {
		cfg.MaxTokens = *aux.MaxTokens
	}
	if aux.Temperature != nil && *aux.Temperature >= 0 {
		cfg.Temperature = *aux.Temperature
	}
	if aux.TimeoutSeconds != nil && *aux.TimeoutSeconds > 0 {
		cfg.TimeoutSeconds = *aux.TimeoutSeconds
	}
	return cfg
}

const configSchemaJSON = `{
  "type": "object",
  "properties": {
    "model": {
      "type": "string",
      "title": "Modèle de classification",
      "description": "Nom proxy du modèle interrogé quand le port model_name n'est pas connecté. Préférez un petit modèle."
    },
    "categories": {
      "type": "array",
      "title": "Catégories",
      "items": {
        "type": "object",
        "properties": {
          "name": { "type": "string", "title": "Nom" },
          "description": { "type": "string", "title": "Description" }
        },
        "required": ["name"]
      }
    },
    "instructions": {
      "type": "string",
      "title": "Instructions supplémentaires",
      "format": "multiline"
    },
    "fallback_category": {
      "type": "string",
      "title": "Catégorie de repli",
      "default": "unknown"
    },
    "include_history": {
      "type": "boolean",
      "title": "Joindre un extrait de l'historique",
      "default": false
    },
    "max_context_chars": { "type": "integer", "title": "Taille maximale du texte envoyé", "default": 2000 },
    "max_tokens": { "type": "integer", "title": "Tokens de réponse maximum", "default": 120 },
    "temperature": { "type": "number", "title": "Température", "default": 0, "minimum": 0, "maximum": 2 },
    "timeout_seconds": { "type": "number", "title": "Délai maximal (secondes)", "default": 15 }
  }
}`
