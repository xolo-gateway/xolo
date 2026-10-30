package model

// CatalogOrigin is the entry of the open model catalog a model was imported
// from, and the values that entry gave it: following the catalog compares
// them with a newer release and with the model, so that a value the catalog
// changes is taken while a value the operator changed is kept.
type CatalogOrigin struct {
	// Provider is the catalog provider, Model the provider model ID of its
	// offering.
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Version is the catalog release Catalog and Applied come from.
	Version string `json:"version"`
	// Currency is the currency of Catalog's prices, the catalog provider's.
	Currency string `json:"currency"`
	// Catalog is the offering as the release gave it, adjusted to the
	// provider's endpoint, before any currency conversion: it tells whether
	// a newer release changed a value.
	Catalog ProviderBundleModel `json:"catalog"`
	// Applied is the catalog's value of each field in the provider's
	// currency, as it was last offered to the model: a model that still
	// holds it was not changed by the operator.
	Applied ProviderBundleModel `json:"applied"`
	// Estimated names the model form fields whose catalog value is an
	// estimate rather than a published or measured figure.
	Estimated []string `json:"estimated,omitempty"`
}

// CatalogTrackedModel is a model with a catalog origin, as following the
// catalog lists them across organizations.
type CatalogTrackedModel struct {
	ModelID    LLMModelID
	OrgID      OrgID
	ProviderID ProviderID
	Origin     CatalogOrigin
}
