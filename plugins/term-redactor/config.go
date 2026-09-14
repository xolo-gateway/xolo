package main

import (
	"encoding/json"
	"fmt"
)

// configSchemaJSON est retourné dans PluginDescriptor.ConfigSchema. La valeur
// d'authentification vers l'API externe n'apparaît pas ici : elle est
// stockée via SetSecret, jamais dans la config visible du nœud.
const configSchemaJSON = `{
  "type": "object",
  "required": ["api_url"],
  "properties": {
    "api_url": {
      "type": "string",
      "title": "URL de l'API des termes",
      "description": "Endpoint renvoyant un tableau JSON [{\"uuid\":\"...\",\"name\":\"...\",\"category\":\"...\"}, ...]. Le champ category est optionnel."
    },
    "api_auth_header_name": {
      "type": "string",
      "title": "Nom de l'en-tête d'authentification",
      "description": "Vide = pas d'en-tête envoyé. ex: Authorization",
      "default": "Authorization"
    },
    "cache_ttl_seconds": {
      "type": "integer",
      "title": "Durée de vie du cache (secondes)",
      "description": "Au-delà, la liste est re-téléchargée. Vide = défaut (600s).",
      "minimum": 0
    },
    "http_timeout_seconds": {
      "type": "integer",
      "title": "Timeout HTTP (secondes)",
      "description": "Vide = défaut (5s).",
      "minimum": 0
    },
    "min_retry_interval_seconds": {
      "type": "integer",
      "title": "Intervalle minimal entre deux tentatives (secondes)",
      "description": "Évite de marteler l'API externe en panne à chaque requête. Vide = défaut (10s).",
      "minimum": 0
    },
    "token_hex_length": {
      "type": "integer",
      "title": "Longueur du jeton (caractères hexadécimaux)",
      "description": "Vide = défaut (8). Augmenté automatiquement en cas de collision.",
      "minimum": 4
    },
    "token_prefix_fallback": {
      "type": "string",
      "title": "Préfixe par défaut du jeton",
      "description": "Utilisé quand une entrée n'a pas de category. Vide = défaut (ENTITY).",
      "default": "ENTITY"
    }
  }
}`

// Config représente la configuration non-sensible du nœud (PluginNodeData.Config).
type Config struct {
	APIURL                  string `json:"api_url"`
	APIAuthHeaderName       string `json:"api_auth_header_name"`
	CacheTTLSeconds         int    `json:"cache_ttl_seconds"`
	HTTPTimeoutSeconds      int    `json:"http_timeout_seconds"`
	MinRetryIntervalSeconds int    `json:"min_retry_interval_seconds"`
	TokenHexLength          int    `json:"token_hex_length"`
	TokenPrefixFallback     string `json:"token_prefix_fallback"`
}

const (
	defaultCacheTTLSeconds         = 600
	defaultHTTPTimeoutSeconds      = 5
	defaultMinRetryIntervalSeconds = 10
	defaultTokenHexLength          = 8
	defaultTokenPrefixFallback     = "ENTITY"
	defaultAPIAuthHeaderName       = "Authorization"
)

// parseConfig désérialise configJSON en Config, en appliquant les valeurs
// par défaut aux champs absents ou vides.
func parseConfig(configJSON string) (Config, error) {
	cfg := defaultConfig()
	if configJSON == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if cfg.APIAuthHeaderName == "" {
		cfg.APIAuthHeaderName = defaultAPIAuthHeaderName
	}
	if cfg.CacheTTLSeconds <= 0 {
		cfg.CacheTTLSeconds = defaultCacheTTLSeconds
	}
	if cfg.HTTPTimeoutSeconds <= 0 {
		cfg.HTTPTimeoutSeconds = defaultHTTPTimeoutSeconds
	}
	if cfg.MinRetryIntervalSeconds <= 0 {
		cfg.MinRetryIntervalSeconds = defaultMinRetryIntervalSeconds
	}
	if cfg.TokenHexLength <= 0 {
		cfg.TokenHexLength = defaultTokenHexLength
	}
	if cfg.TokenPrefixFallback == "" {
		cfg.TokenPrefixFallback = defaultTokenPrefixFallback
	}
	return cfg, nil
}

func defaultConfig() Config {
	return Config{
		APIAuthHeaderName:       defaultAPIAuthHeaderName,
		CacheTTLSeconds:         defaultCacheTTLSeconds,
		HTTPTimeoutSeconds:      defaultHTTPTimeoutSeconds,
		MinRetryIntervalSeconds: defaultMinRetryIntervalSeconds,
		TokenHexLength:          defaultTokenHexLength,
		TokenPrefixFallback:     defaultTokenPrefixFallback,
	}
}
