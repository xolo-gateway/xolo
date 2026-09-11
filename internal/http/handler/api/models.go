package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/bornholm/go-x/slogx"
	proxyAdapter "github.com/xolo-gateway/xolo/internal/adapter/proxy"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/core/service"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

type openRouterModel struct {
	ID                string            `json:"id"`
	Object            string            `json:"object"`
	Created           int               `json:"created"`
	OwnedBy           string            `json:"owned_by"`
	Name              string            `json:"name"`
	Description       string            `json:"description"`
	ContextLength     *int              `json:"context_length,omitempty"`
	Architecture      modelArchitecture `json:"architecture"`
	DefaultParameters *struct{}         `json:"default_parameters"`
	Pricing           modelPricing      `json:"pricing"`
	PerRequestLimits  *perRequestLimits `json:"per_request_limits,omitempty"`
	SupportedParams   []string          `json:"supported_parameters"`
}

type modelArchitecture struct {
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
	Modality         string   `json:"modality"`
	InstructType     *string  `json:"instruct_type,omitempty"`
	Tokenizer        *string  `json:"tokenizer,omitempty"`
}

type modelPricing struct {
	Prompt     float64 `json:"prompt"`
	Completion float64 `json:"completion"`
}

type perRequestLimits struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type pluginManagerIface interface {
	List() []*proto.PluginDescriptor
	HTTPPort(pluginName string) uint32
}

type Handler struct {
	providerStore       port.ProviderStore
	orgStore            port.OrgStore
	virtualModelStore   port.VirtualModelStore
	personalVMStore     port.PersonalVirtualModelStore
	middlewareStore     port.MiddlewareStore
	secretStore         port.SecretStore
	exchangeRateService *service.ExchangeRateService
	pluginManager       pluginManagerIface
	mux                 *http.ServeMux
}

// Permission checks resolve through the request context (see authz.go), so the
// handler does not need a RoleStore of its own.
func NewHandler(providerStore port.ProviderStore, orgStore port.OrgStore, virtualModelStore port.VirtualModelStore, personalVMStore port.PersonalVirtualModelStore, middlewareStore port.MiddlewareStore, secretStore port.SecretStore, exchangeRateService *service.ExchangeRateService, pluginManager pluginManagerIface) *Handler {
	h := &Handler{
		providerStore:       providerStore,
		orgStore:            orgStore,
		virtualModelStore:   virtualModelStore,
		personalVMStore:     personalVMStore,
		middlewareStore:     middlewareStore,
		secretStore:         secretStore,
		exchangeRateService: exchangeRateService,
		pluginManager:       pluginManager,
		mux:                 http.NewServeMux(),
	}
	h.mux.HandleFunc("GET /api/v1/models", h.handleModels)
	h.mux.HandleFunc("GET /api/models-dev/lookup", h.handleModelsDevLookup)
	h.mux.HandleFunc("GET /api/exchange-rate", h.handleExchangeRate)
	// Plugin UI config sync (seed before iframe open, read back after close)
	h.mux.HandleFunc("PUT /api/orgs/{orgSlug}/plugin-ui-config", h.handleSeedPluginUIConfig)
	h.mux.HandleFunc("GET /api/orgs/{orgSlug}/plugin-ui-config", h.handleReadPluginUIConfig)
	// Personal context plugin UI config (no org required)
	h.mux.HandleFunc("PUT /api/personal-plugin-ui-config", h.handleSeedPersonalPluginUIConfig)
	h.mux.HandleFunc("GET /api/personal-plugin-ui-config", h.handleReadPersonalPluginUIConfig)
	// Org virtual model pipeline CRUD
	h.mux.HandleFunc("GET /api/orgs/{orgSlug}/virtual-models", h.handleListVirtualModels)
	h.mux.HandleFunc("POST /api/orgs/{orgSlug}/virtual-models", h.handleCreateVirtualModel)
	h.mux.HandleFunc("POST /api/orgs/{orgSlug}/virtual-models/import", h.handleImportVirtualModel)
	h.mux.HandleFunc("GET /api/orgs/{orgSlug}/virtual-models/{vmID}", h.handleGetVirtualModel)
	h.mux.HandleFunc("GET /api/orgs/{orgSlug}/virtual-models/{vmID}/export", h.handleExportVirtualModel)
	h.mux.HandleFunc("PUT /api/orgs/{orgSlug}/virtual-models/{vmID}", h.handleUpdateVirtualModel)
	h.mux.HandleFunc("DELETE /api/orgs/{orgSlug}/virtual-models/{vmID}", h.handleDeleteVirtualModel)
	h.mux.HandleFunc("GET /api/orgs/{orgSlug}/pipeline-node-types", h.handlePipelineNodeTypes)
	h.mux.HandleFunc("GET /api/orgs/{orgSlug}/pipeline-models", h.handlePipelineModels)
	// Org middleware pipeline CRUD (shares the generic graph handlers)
	h.mux.HandleFunc("GET /api/orgs/{orgSlug}/middlewares", h.handleListMiddlewares)
	h.mux.HandleFunc("POST /api/orgs/{orgSlug}/middlewares", h.handleCreateMiddleware)
	h.mux.HandleFunc("GET /api/orgs/{orgSlug}/middlewares/{mwID}", h.handleGetMiddleware)
	h.mux.HandleFunc("PUT /api/orgs/{orgSlug}/middlewares/{mwID}", h.handleUpdateMiddleware)
	h.mux.HandleFunc("PUT /api/orgs/{orgSlug}/middlewares/{mwID}/settings", h.handleUpdateMiddlewareSettings)
	h.mux.HandleFunc("DELETE /api/orgs/{orgSlug}/middlewares/{mwID}", h.handleDeleteMiddleware)
	// Personal virtual model pipeline CRUD
	h.mux.HandleFunc("GET /api/personal-models", h.handleListPersonalVMs)
	h.mux.HandleFunc("POST /api/personal-models", h.handleCreatePersonalVM)
	h.mux.HandleFunc("POST /api/personal-models/import", h.handleImportPersonalVM)
	h.mux.HandleFunc("GET /api/personal-models/{vmID}", h.handleGetPersonalVM)
	h.mux.HandleFunc("GET /api/personal-models/{vmID}/export", h.handleExportPersonalVM)
	h.mux.HandleFunc("PUT /api/personal-models/{vmID}", h.handleUpdatePersonalVM)
	h.mux.HandleFunc("DELETE /api/personal-models/{vmID}", h.handleDeletePersonalVM)
	h.mux.HandleFunc("GET /api/personal-models/pipeline-node-types", h.handlePersonalPipelineNodeTypes)
	h.mux.HandleFunc("GET /api/personal-models/pipeline-models", h.handlePersonalPipelineModels)
	return h
}

type exchangeRateResponse struct {
	From string  `json:"from"`
	To   string  `json:"to"`
	Rate float64 `json:"rate"`
}

func (h *Handler) handleExchangeRate(w http.ResponseWriter, r *http.Request) {
	from := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("from")))
	to := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("to")))
	if from == "" || to == "" {
		writeError(w, http.StatusBadRequest, "missing from or to parameter")
		return
	}
	if from == to {
		writeJSON(w, http.StatusOK, exchangeRateResponse{From: from, To: to, Rate: 1})
		return
	}
	// Convert 1_000_000 microcents to get the rate as a float64.
	converted, err := h.exchangeRateService.Convert(r.Context(), 1_000_000, from, to)
	if err != nil {
		slog.WarnContext(r.Context(), "exchange rate unavailable", slog.String("from", from), slog.String("to", to), slog.Any("error", err))
		writeError(w, http.StatusServiceUnavailable, "exchange rate unavailable")
		return
	}
	writeJSON(w, http.StatusOK, exchangeRateResponse{From: from, To: to, Rate: float64(converted) / 1_000_000})
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

type modelsResponse struct {
	Object string            `json:"object"`
	Data   []openRouterModel `json:"data"`
}

func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	filterParams := r.URL.Query().Get("supported_parameters")
	filterModalities := r.URL.Query().Get("output_modalities")

	var orgIDs []model.OrgID

	if orgID := model.OrgID(proxyAdapter.OrgIDFromContext(ctx)); orgID != "" {
		orgIDs = []model.OrgID{orgID}
	} else {
		user := httpCtx.User(ctx)
		if user == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		memberships, err := h.orgStore.GetUserMemberships(ctx, user.ID())
		if err != nil {
			slog.ErrorContext(ctx, "could not fetch user memberships", slogx.Error(err))
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		for _, m := range memberships {
			orgIDs = append(orgIDs, m.OrgID())
		}
	}

	data := make([]openRouterModel, 0)
	canUsePersonalVM := false
	for _, orgID := range orgIDs {
		org, err := h.orgStore.GetOrgByID(ctx, orgID)
		if err != nil {
			slog.WarnContext(ctx, "could not get org for models listing", slogx.Error(err), slog.String("orgID", string(orgID)))
			continue
		}
		perms, err := httpCtx.ResolvePermissions(ctx, orgID)
		if err != nil {
			slog.WarnContext(ctx, "could not resolve permissions", slogx.Error(err), slog.String("orgID", string(orgID)))
			continue
		}
		if perms.IsOwner() || perms.Has(rbac.PermPersonalVMCreate) {
			canUsePersonalVM = true
		}
		models, err := h.providerStore.ListEnabledLLMModels(ctx, orgID)
		if err != nil {
			slog.WarnContext(ctx, "could not list enabled models", slogx.Error(err), slog.String("orgID", string(orgID)))
			continue
		}
		for _, m := range models {
			if !perms.IsOwner() && !perms.Has(rbac.PermModelUseOrg) && !perms.HasModelAccess(string(m.ID()), rbac.ModelKindLLM) {
				continue
			}
			model := h.llmModelToOpenRouterModel(org.Slug(), m)
			if filterModel(model, filterParams, filterModalities) {
				data = append(data, model)
			}
		}
		virtualModels, err := h.virtualModelStore.ListVirtualModels(ctx, orgID)
		if err != nil {
			slog.WarnContext(ctx, "could not list virtual models", slogx.Error(err), slog.String("orgID", string(orgID)))
		} else {
			for _, vm := range virtualModels {
				if !perms.IsOwner() && !perms.Has(rbac.PermModelUseVirtual) && !perms.HasModelAccess(string(vm.ID()), rbac.ModelKindVirtual) {
					continue
				}
				model := h.virtualModelToOpenRouterModel(org.Slug(), vm)
				if filterModel(model, filterParams, filterModalities) {
					data = append(data, model)
				}
			}
		}
	}

	// Personal virtual models — gated by the personal-vm permission.
	if h.personalVMStore != nil && canUsePersonalVM {
		user := httpCtx.User(ctx)
		if user != nil {
			pvms, err := h.personalVMStore.ListPersonalVirtualModels(ctx, user.ID())
			if err != nil {
				slog.WarnContext(ctx, "could not list personal virtual models", slogx.Error(err))
			} else {
				for _, pvm := range pvms {
					m := h.personalVMToOpenRouterModel(pvm)
					if filterModel(m, filterParams, filterModalities) {
						data = append(data, m)
					}
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, modelsResponse{
		Object: "list",
		Data:   data,
	})
}

// baseSupportedParams lists the OpenAI chat parameters every model accepts:
// the ones the proxy maps explicitly plus those it forwards verbatim.
func baseSupportedParams() []string {
	return []string{
		"max_tokens", "max_completion_tokens", "temperature", "top_p", "stop",
		"seed", "frequency_penalty", "presence_penalty", "logit_bias", "user",
		"response_format", "structured_outputs",
	}
}

func (h *Handler) personalVMToOpenRouterModel(pvm model.PersonalVirtualModel) openRouterModel {
	return openRouterModel{
		ID:          "~/" + pvm.Name(),
		Object:      "model",
		Created:     int(pvm.CreatedAt().Unix()),
		OwnedBy:     "xolo",
		Name:        pvm.Name(),
		Description: pvm.Description(),
		Architecture: modelArchitecture{
			InputModalities:  []string{"text"},
			OutputModalities: []string{"text"},
			Modality:         "text",
		},
		DefaultParameters: nil,
		Pricing:           modelPricing{Prompt: 0, Completion: 0},
		SupportedParams:   baseSupportedParams(),
	}
}

func (h *Handler) llmModelToOpenRouterModel(orgSlug string, m model.LLMModel) openRouterModel {
	caps := m.Capabilities()
	inputModalities := []string{"text"}
	outputModalities := []string{"text"}
	modality := "text"

	if caps.Vision {
		inputModalities = append(inputModalities, "image")
	}
	if caps.Embeddings {
		outputModalities = append(outputModalities, "embeddings")
	}
	if caps.Audio {
		outputModalities = append(outputModalities, "audio")
	}

	supportedParams := baseSupportedParams()
	if caps.Tools {
		supportedParams = append(supportedParams, "tools", "tool_choice", "parallel_tool_calls")
	}
	if caps.Reasoning {
		supportedParams = append(supportedParams, "reasoning", "include_reasoning")
	}

	var contextLength *int
	if cw := m.ContextWindow(); cw > 0 {
		cl := int(cw)
		contextLength = &cl
	}

	pricing := modelPricing{
		Prompt:     float64(m.PromptCostPer1KTokens()) / 1_000_000,
		Completion: float64(m.CompletionCostPer1KTokens()) / 1_000_000,
	}

	var perLimits *perRequestLimits
	if tlc := m.TokenLimitConfig(); tlc != nil && tlc.MaxTokens > 0 {
		perLimits = &perRequestLimits{
			PromptTokens:     tlc.MaxTokens,
			CompletionTokens: tlc.MaxTokens,
		}
	}

	return openRouterModel{
		ID:            orgSlug + "/" + m.ProxyName(),
		Object:        "model",
		Created:       int(m.CreatedAt().Unix()),
		OwnedBy:       "xolo",
		Name:          m.ProxyName(),
		Description:   m.Description(),
		ContextLength: contextLength,
		Architecture: modelArchitecture{
			InputModalities:  inputModalities,
			OutputModalities: outputModalities,
			Modality:         modality,
		},
		DefaultParameters: nil,
		Pricing:           pricing,
		PerRequestLimits:  perLimits,
		SupportedParams:   supportedParams,
	}
}

func (h *Handler) virtualModelToOpenRouterModel(orgSlug string, vm model.VirtualModel) openRouterModel {
	return openRouterModel{
		ID:          orgSlug + "/" + vm.Name(),
		Object:      "model",
		Created:     int(vm.CreatedAt().Unix()),
		OwnedBy:     "xolo",
		Name:        vm.Name(),
		Description: vm.Description(),
		Architecture: modelArchitecture{
			InputModalities:  []string{"text"},
			OutputModalities: []string{"text"},
			Modality:         "text",
		},
		DefaultParameters: nil,
		Pricing:           modelPricing{Prompt: 0, Completion: 0},
		SupportedParams:   baseSupportedParams(),
	}
}

func filterModel(m openRouterModel, filterParams, filterModalities string) bool {
	if filterParams != "" {
		wantParams := strings.Split(filterParams, ",")
		has := false
		for _, wp := range wantParams {
			if slices.Contains(m.SupportedParams, wp) {
				has = true
				break
			}
		}
		if !has {
			return false
		}
	}
	if filterModalities != "" && filterModalities != "all" {
		wantModalities := strings.Split(filterModalities, ",")
		has := false
		for _, wm := range wantModalities {
			if slices.Contains(m.Architecture.OutputModalities, wm) {
				has = true
				break
			}
		}
		if !has {
			return false
		}
	}
	return true
}

type apiError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("could not write JSON response", slog.Any("error", err))
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	var resp apiError
	resp.Error.Message = msg
	resp.Error.Type = "invalid_request_error"
	writeJSON(w, status, resp)
}
