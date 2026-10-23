package main

import (
	"encoding/json"
	"fmt"
)

// configSchemaJSON est retourné dans PluginDescriptor.ConfigSchema.
// La valeur d'authentification n'apparaît pas ici : elle est stockée via
// SetSecret, jamais dans la config visible du nœud.
const configSchemaJSON = `{
  "type": "object",
  "required": ["endpoint"],
  "properties": {
    "endpoint": {
      "type": "string",
      "title": "URL du serveur MCP",
      "description": "Endpoint Streamable HTTP, ex: https://mcp.example.com/mcp"
    },
    "authMode": {
      "type": "string",
      "title": "Authentification",
      "enum": ["static", "oauth"],
      "description": "static : un en-tête fixe pour tous ; oauth : chaque utilisateur autorise Xolo sur le serveur MCP"
    },
    "publicBaseURL": {
      "type": "string",
      "title": "URL publique de Xolo",
      "description": "Base des liens d'autorisation donnés aux utilisateurs (renseignée automatiquement)"
    },
    "authHeaderName": {
      "type": "string",
      "title": "Nom de l'en-tête d'authentification",
      "description": "ex: Authorization"
    },
    "toolFilter": {
      "type": "array",
      "title": "Tools autorisés (vide = tous)",
      "items": { "type": "string" }
    },
    "resources": {
      "type": "array",
      "title": "Ressources MCP injectées",
      "description": "URI ou nom des ressources du serveur à lire et à injecter avant le premier appel au modèle.",
      "items": { "type": "string" }
    },
    "prefetchTool": {
      "type": "string",
      "title": "Tool pré-appelé",
      "description": "Nom d'un tool du serveur, appelé une fois avant le premier appel au modèle, à chaque requête. Son résultat est injecté dans la conversation."
    },
    "prefetchArguments": {
      "type": "string",
      "title": "Arguments du tool pré-appelé (JSON)",
      "description": "Objet JSON. Les valeurs peuvent utiliser {{last_user_message}}, {{user_id}}, {{org_id}}, {{display_name}} ou {{session_id}}. Défaut : {}."
    },
    "prefetchResultTemplate": {
      "type": "string",
      "title": "Gabarit du résultat pré-appelé",
      "description": "Encadre le résultat du tool : chaque {{result}} est remplacé par le texte du résultat. Vide = résultat brut."
    },
    "prefetchLabel": {
      "type": "string",
      "title": "Titre du bloc injecté",
      "description": "Ouvre le bloc du résultat pré-appelé. Vide = pas de titre."
    },
    "timeoutSeconds": {
      "type": "number",
      "title": "Timeout (secondes)"
    },
    "maxConsecutiveToolCalls": {
      "type": "number",
      "title": "Appels d'outils consécutifs max.",
      "description": "Au-delà, le LLM est forcé de répondre sans appeler d'outil supplémentaire. Défaut: 2."
    }
  }
}`

// Config représente la configuration non-sensible du nœud (PluginNodeData.Config).
type Config struct {
	Endpoint string `json:"endpoint"`
	// AuthMode is AuthModeStatic (default) or AuthModeOAuth.
	AuthMode string `json:"authMode,omitempty"`
	// PublicBaseURL is the public URL of Xolo, captured when the node is
	// configured: the authorization links given to the users start with it.
	PublicBaseURL           string   `json:"publicBaseURL,omitempty"`
	AuthHeaderName          string   `json:"authHeaderName"`
	ToolFilter              []string `json:"toolFilter,omitempty"`
	TimeoutSeconds          int      `json:"timeoutSeconds,omitempty"`
	MaxConsecutiveToolCalls int      `json:"maxConsecutiveToolCalls,omitempty"`
	// Resources are the MCP resources injected before the model's first call,
	// each named by URI or by resource name.
	Resources []string `json:"resources,omitempty"`
	// PrefetchTool is the tool called once, before the model's first call, on
	// every request. Empty disables the prefetch: the model then has to decide
	// on its own whether to call it.
	PrefetchTool string `json:"prefetchTool,omitempty"`
	// PrefetchArguments is the JSON object the prefetched tool is called with,
	// placeholders included. Empty means "{}".
	PrefetchArguments string `json:"prefetchArguments,omitempty"`
	// PrefetchResultTemplate wraps the prefetched result; each "{{result}}" is
	// replaced by the result text.
	PrefetchResultTemplate string `json:"prefetchResultTemplate,omitempty"`
	// PrefetchLabel titles the injected block.
	PrefetchLabel string `json:"prefetchLabel,omitempty"`
}

// parseConfig désérialise configJSON en Config.
func parseConfig(configJSON string) (Config, error) {
	if configJSON == "" {
		return Config{}, nil
	}
	var cfg Config
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return Config{}, fmt.Errorf("parse plugin config: %w", err)
	}
	return cfg, nil
}
