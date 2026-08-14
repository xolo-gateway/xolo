package main

import (
	"context"
	"encoding/json"
	"math/rand"
	"time"

	"github.com/pkg/errors"
	"gorm.io/gorm"

	gormadapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/crypto"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authz"
)

// defaultSecretKey is the AES-GCM key used to encrypt the fake provider API
// keys. The generated database is only usable by a server started with the very
// same XOLO_SECRET_KEY, hence a well-known constant here.
const defaultSecretKey = "0e2ec2e6d5aa74c1b96c65d1b4a0f4d9ad48c6b1f3ff7a1c5f0f5f2ae4c1d3b7"

// Well-known identifiers. They are hand-written (instead of xid-generated) so
// E2E tests can address any entity by a stable ID and stable URLs.
const (
	orgAcme    = "org-acme"
	orgGlobex  = "org-globex"
	orgInitech = "org-initech"

	userRoot  = "usr-root"
	userAlice = "usr-alice"
	userBob   = "usr-bob"
	userCarol = "usr-carol"
	userDave  = "usr-dave"
	userErin  = "usr-erin"
	userFrank = "usr-frank"

	appAcmeCI    = "app-acme-ci"
	appGlobexBot = "app-globex-bot"

	providerAcmeOpenAI  = "prov-acme-openai"
	providerAcmeMistral = "prov-acme-mistral"
	providerAcmeLocal   = "prov-acme-local"
	providerGlobexPlan  = "prov-globex-plan"

	modelAcmeGPT4oMini  = "mdl-acme-gpt4o-mini"
	modelAcmeGPT4o      = "mdl-acme-gpt4o"
	modelAcmeMistral    = "mdl-acme-mistral-small"
	modelAcmeEmbeddings = "mdl-acme-embeddings"
	modelGlobexSonnet   = "mdl-globex-sonnet"
	modelGlobexHaiku    = "mdl-globex-haiku"

	virtualModelAcme = "vm-acme-smart-router"
	middlewareAcme   = "mw-acme-guardrails"
	personalVMCarol  = "pvm-carol-notes"

	roleAcmeAnalyst = "role-acme-analyst"
)

// API token values. Plain-text on purpose: E2E tests send them as-is in the
// Authorization header.
const (
	tokenAlice       = "xolo-e2e-alice-acme"
	tokenCarolAcme   = "xolo-e2e-carol-acme"
	tokenCarolGlobex = "xolo-e2e-carol-globex"
	tokenDaveExpired = "xolo-e2e-dave-expired"
	tokenErin        = "xolo-e2e-erin-globex"
	tokenAppAcmeCI   = "xolo-e2e-app-acme-ci"
	tokenAppGlobex   = "xolo-e2e-app-globex-bot"
)

// usdToEUR is the frozen exchange rate used both for the cached rate row and
// for the cost conversion of USD providers billed inside the EUR org.
const usdToEUR = 0.92

// now is the reference instant of the dataset. It is set once at startup so
// every generated timestamp is relative to a single "now".
var now = time.Now().UTC().Truncate(time.Hour)

type seeder struct {
	db        *gorm.DB
	store     *gormadapter.Store
	dsn       string
	secretKey string
	days      int
	rand      *rand.Rand

	usageRecords int
	usageCost    map[string]int64 // orgID -> total cost (microcents, org currency)

	// tenantID is the default tenant, created by the schema migration. The
	// fixture is single-tenant: everything hangs from it, exactly like an
	// instance running with multi-tenancy disabled.
	tenantID string
}

func newRand(seed int64) *rand.Rand {
	return rand.New(rand.NewSource(seed))
}

func (s *seeder) seed(ctx context.Context) error {
	s.usageCost = map[string]int64{}

	steps := []struct {
		name string
		fn   func(ctx context.Context) error
	}{
		{"tenant", s.resolveTenant},
		{"organizations", s.seedOrganizations},
		{"users", s.seedUsers},
		{"roles", s.seedRoles},
		{"memberships", s.seedMemberships},
		{"applications", s.seedApplications},
		{"auth tokens", s.seedAuthTokens},
		{"providers", s.seedProviders},
		{"models", s.seedModels},
		{"pipelines", s.seedPipelines},
		{"quotas", s.seedQuotas},
		{"invites", s.seedInvites},
		{"exchange rates", s.seedExchangeRates},
		{"usage history", s.seedUsage},
		{"events & alerts", s.seedEvents},
	}

	for _, step := range steps {
		if err := step.fn(ctx); err != nil {
			return errors.Wrapf(err, "could not seed %s", step.name)
		}
	}

	return nil
}

// create inserts the given records without touching their associations, so
// insertion order stays explicit and foreign keys are never auto-upserted.
func (s *seeder) create(records ...any) error {
	for _, record := range records {
		if err := s.db.Omit("Org", "User", "Owner", "Application", "Roles", "Providers",
			"LLMModels", "Memberships", "AuthTokens", "ModelGrants", "Permissions").
			Create(record).Error; err != nil {
			return errors.WithStack(err)
		}
	}
	return nil
}

// resolveTenant reads the tenant every fixture belongs to. It is created by the
// schema migration, which has already run by the time the seeder starts.
func (s *seeder) resolveTenant(ctx context.Context) error {
	tenant, err := s.store.GetTenantBySlug(ctx, model.DefaultTenantSlug)
	if err != nil {
		return errors.WithStack(err)
	}

	s.tenantID = string(tenant.ID())

	return nil
}

func (s *seeder) seedOrganizations(ctx context.Context) error {
	orgs := []*gormadapter.Organization{
		{
			ID:          orgAcme,
			CreatedAt:   now.AddDate(0, -14, 0),
			UpdatedAt:   now.AddDate(0, 0, -3),
			Slug:        "acme",
			Name:        "Acme Corporation",
			Description: "Organisation principale du jeu de test : multi-providers, quotas et pipelines.",
			Active:      1,
			Currency:    "EUR",
		},
		{
			ID:                orgGlobex,
			CreatedAt:         now.AddDate(0, -6, 0),
			UpdatedAt:         now.AddDate(0, 0, -1),
			Slug:              "globex",
			Name:              "Globex Industries",
			Description:       "Organisation facturée à l'abonnement, quota partagé équitablement.",
			Active:            1,
			Currency:          "USD",
			ShareQuotaEqually: 1,
		},
		{
			ID:          orgInitech,
			CreatedAt:   now.AddDate(0, -2, 0),
			UpdatedAt:   now.AddDate(0, -1, 0),
			Slug:        "initech",
			Name:        "Initech",
			Description: "Organisation désactivée : sert aux tests de rejet d'accès.",
			Active:      0,
			Currency:    "USD",
		},
	}

	for _, org := range orgs {
		org.TenantID = s.tenantID

		if err := s.create(org); err != nil {
			return errors.WithStack(err)
		}
		// Creates the builtin owner/admin/member roles with their up-to-date
		// permission catalog.
		if err := s.store.EnsureBuiltinRoles(ctx, model.OrgID(org.ID)); err != nil {
			return errors.WithStack(err)
		}
	}

	return nil
}

func (s *seeder) seedUsers(ctx context.Context) error {
	darkMode := true

	users := []*gormadapter.User{
		{
			ID:          userRoot,
			CreatedAt:   now.AddDate(0, -14, 0),
			Subject:     "root",
			Provider:    "local",
			DisplayName: "Root",
			Email:       "root@xolo.test",
			Active:      true,
			Roles: []*gormadapter.UserRole{
				{UserID: userRoot, Role: authz.RoleAdmin},
			},
		},
		{
			ID:          userAlice,
			CreatedAt:   now.AddDate(0, -14, 0),
			Subject:     "alice",
			Provider:    "local",
			DisplayName: "Alice Martin",
			Email:       "alice@acme.test",
			Active:      true,
			Preferences: &gormadapter.UserPreferences{UserID: userAlice, DarkMode: &darkMode},
		},
		{
			ID:          userBob,
			CreatedAt:   now.AddDate(0, -12, 0),
			Subject:     "bob",
			Provider:    "local",
			DisplayName: "Bob Durand",
			Email:       "bob@acme.test",
			Active:      true,
		},
		{
			ID:          userCarol,
			CreatedAt:   now.AddDate(0, -9, 0),
			Subject:     "carol",
			Provider:    "local",
			DisplayName: "Carol Nguyen",
			Email:       "carol@acme.test",
			Active:      true,
		},
		{
			ID:          userDave,
			CreatedAt:   now.AddDate(0, -5, 0),
			Subject:     "dave",
			Provider:    "local",
			DisplayName: "Dave Leroy",
			Email:       "dave@acme.test",
			Active:      true,
		},
		{
			ID:          userErin,
			CreatedAt:   now.AddDate(0, -6, 0),
			Subject:     "erin",
			Provider:    "local",
			DisplayName: "Erin Silva",
			Email:       "erin@globex.test",
			Active:      true,
		},
		{
			// Deactivated user: every authenticated call on their behalf must be
			// rejected even though the membership still exists.
			ID:          userFrank,
			CreatedAt:   now.AddDate(0, -4, 0),
			Subject:     "frank",
			Provider:    "local",
			DisplayName: "Frank Weber",
			Email:       "frank@globex.test",
			Active:      false,
		},
	}

	for _, user := range users {
		user.TenantID = s.tenantID

		roles := user.Roles
		prefs := user.Preferences
		user.Roles = nil
		user.Preferences = nil

		if err := s.create(user); err != nil {
			return errors.WithStack(err)
		}
		for _, role := range roles {
			if err := s.create(role); err != nil {
				return errors.WithStack(err)
			}
		}
		if prefs != nil {
			if err := s.create(prefs); err != nil {
				return errors.WithStack(err)
			}
		}
	}

	return nil
}

// seedRoles adds an org-scoped custom role on top of the builtin ones, granting
// access to a restricted set of models.
func (s *seeder) seedRoles(ctx context.Context) error {
	role := &gormadapter.Role{
		ID:          roleAcmeAnalyst,
		CreatedAt:   now.AddDate(0, 0, -30),
		UpdatedAt:   now.AddDate(0, 0, -30),
		OrgID:       orgAcme,
		Name:        "Analyste",
		Description: "Lecture de l'usage et accès aux seuls modèles économiques.",
	}
	if err := s.create(role); err != nil {
		return errors.WithStack(err)
	}

	for _, code := range []rbac.Permission{rbac.PermUsageRead, rbac.PermModelUseOrg} {
		if err := s.create(&gormadapter.RolePermission{RoleID: roleAcmeAnalyst, Code: string(code)}); err != nil {
			return errors.WithStack(err)
		}
	}

	for _, modelID := range []string{modelAcmeGPT4oMini, modelAcmeMistral} {
		if err := s.create(&gormadapter.RoleModel{
			RoleID:    roleAcmeAnalyst,
			ModelID:   modelID,
			ModelKind: rbac.ModelKindLLM,
		}); err != nil {
			return errors.WithStack(err)
		}
	}

	return nil
}

func (s *seeder) seedMemberships(ctx context.Context) error {
	memberships := []struct {
		id      string
		userID  string
		orgID   string
		builtin string
		extra   []string
	}{
		{"mbr-alice-acme", userAlice, orgAcme, model.BuiltinKindOwner, nil},
		{"mbr-bob-acme", userBob, orgAcme, model.BuiltinKindAdmin, nil},
		{"mbr-carol-acme", userCarol, orgAcme, model.BuiltinKindMember, nil},
		{"mbr-dave-acme", userDave, orgAcme, model.BuiltinKindMember, []string{roleAcmeAnalyst}},
		{"mbr-carol-globex", userCarol, orgGlobex, model.BuiltinKindMember, nil},
		{"mbr-erin-globex", userErin, orgGlobex, model.BuiltinKindOwner, nil},
		{"mbr-frank-globex", userFrank, orgGlobex, model.BuiltinKindMember, nil},
		{"mbr-root-initech", userRoot, orgInitech, model.BuiltinKindOwner, nil},
	}

	for _, m := range memberships {
		if err := s.create(&gormadapter.Membership{
			ID:        m.id,
			CreatedAt: now.AddDate(0, -3, 0),
			UserID:    m.userID,
			OrgID:     m.orgID,
		}); err != nil {
			return errors.WithStack(err)
		}

		roleID, err := s.builtinRoleID(m.orgID, m.builtin)
		if err != nil {
			return errors.WithStack(err)
		}

		for _, id := range append([]string{roleID}, m.extra...) {
			if err := s.create(&gormadapter.MembershipRole{
				MembershipID: m.id,
				RoleID:       id,
				CreatedAt:    now.AddDate(0, -3, 0),
			}); err != nil {
				return errors.WithStack(err)
			}
		}
	}

	return nil
}

// builtinRoleID resolves the (xid-generated) identifier of an organization's
// builtin role created by EnsureBuiltinRoles.
func (s *seeder) builtinRoleID(orgID, kind string) (string, error) {
	var role gormadapter.Role
	if err := s.db.Where("org_id = ? AND builtin_kind = ?", orgID, kind).First(&role).Error; err != nil {
		return "", errors.Wrapf(err, "could not find builtin role %q of org %q", kind, orgID)
	}
	return role.ID, nil
}

func (s *seeder) seedApplications(ctx context.Context) error {
	apps := []struct {
		app     *gormadapter.Application
		builtin string
	}{
		{
			app: &gormadapter.Application{
				ID:          appAcmeCI,
				CreatedAt:   now.AddDate(0, -2, 0),
				UpdatedAt:   now.AddDate(0, -2, 0),
				OrgID:       orgAcme,
				Name:        "CI Pipeline",
				Description: "Compte de service utilisé par l'intégration continue.",
				Active:      true,
			},
			builtin: model.BuiltinKindMember,
		},
		{
			app: &gormadapter.Application{
				ID:          appGlobexBot,
				CreatedAt:   now.AddDate(0, -1, 0),
				UpdatedAt:   now.AddDate(0, -1, 0),
				OrgID:       orgGlobex,
				Name:        "Support Bot",
				Description: "Bot de support client (désactivé pour les tests de rejet).",
				Active:      false,
			},
			builtin: model.BuiltinKindMember,
		},
	}

	for _, a := range apps {
		if err := s.create(a.app); err != nil {
			return errors.WithStack(err)
		}
		roleID, err := s.builtinRoleID(a.app.OrgID, a.builtin)
		if err != nil {
			return errors.WithStack(err)
		}
		if err := s.create(&gormadapter.ApplicationRole{
			ApplicationID: a.app.ID,
			RoleID:        roleID,
			CreatedAt:     a.app.CreatedAt,
		}); err != nil {
			return errors.WithStack(err)
		}
	}

	return nil
}

func (s *seeder) seedAuthTokens(ctx context.Context) error {
	expired := now.AddDate(0, 0, -2)
	future := now.AddDate(1, 0, 0)

	strptr := func(v string) *string { return &v }

	tokens := []*gormadapter.AuthToken{
		{
			ID: "tok-alice-acme", CreatedAt: now.AddDate(0, -3, 0), UpdatedAt: now.AddDate(0, -3, 0),
			OwnerID: strptr(userAlice), Label: "Poste de travail", Value: tokenAlice, OrgID: orgAcme,
		},
		{
			ID: "tok-carol-acme", CreatedAt: now.AddDate(0, -2, 0), UpdatedAt: now.AddDate(0, -2, 0),
			OwnerID: strptr(userCarol), Label: "Notebook", Value: tokenCarolAcme, OrgID: orgAcme, ExpiresAt: &future,
		},
		{
			ID: "tok-carol-globex", CreatedAt: now.AddDate(0, -1, 0), UpdatedAt: now.AddDate(0, -1, 0),
			OwnerID: strptr(userCarol), Label: "Notebook (Globex)", Value: tokenCarolGlobex, OrgID: orgGlobex,
		},
		{
			// Already expired: authentication with it must fail.
			ID: "tok-dave-expired", CreatedAt: now.AddDate(0, -1, 0), UpdatedAt: now.AddDate(0, -1, 0),
			OwnerID: strptr(userDave), Label: "Ancien jeton", Value: tokenDaveExpired, OrgID: orgAcme, ExpiresAt: &expired,
		},
		{
			ID: "tok-erin-globex", CreatedAt: now.AddDate(0, -4, 0), UpdatedAt: now.AddDate(0, -4, 0),
			OwnerID: strptr(userErin), Label: "CLI", Value: tokenErin, OrgID: orgGlobex,
		},
		{
			ID: "tok-app-acme-ci", CreatedAt: now.AddDate(0, -2, 0), UpdatedAt: now.AddDate(0, -2, 0),
			ApplicationID: strptr(appAcmeCI), Label: "Jeton CI", Value: tokenAppAcmeCI, OrgID: orgAcme,
		},
		{
			ID: "tok-app-globex-bot", CreatedAt: now.AddDate(0, -1, 0), UpdatedAt: now.AddDate(0, -1, 0),
			ApplicationID: strptr(appGlobexBot), Label: "Jeton bot", Value: tokenAppGlobex, OrgID: orgGlobex,
		},
	}

	for _, token := range tokens {
		if err := s.create(token); err != nil {
			return errors.WithStack(err)
		}
	}

	return nil
}

func (s *seeder) seedProviders(ctx context.Context) error {
	apiKey, err := crypto.Encrypt(s.secretKey, "sk-e2e-fake-api-key")
	if err != nil {
		return errors.WithStack(err)
	}
	otherKey, err := crypto.Encrypt(s.secretKey, "sk-e2e-fake-api-key-2")
	if err != nil {
		return errors.WithStack(err)
	}

	maxConcurrent := 5
	tokenBudget := int64(20_000_000)
	valueBudget := int64(50_000_000) // 50 USD in microcents

	providers := []*gormadapter.Provider{
		{
			ID: providerAcmeOpenAI, CreatedAt: now.AddDate(0, -13, 0), UpdatedAt: now.AddDate(0, 0, -10),
			OrgID: orgAcme, Name: "OpenAI", Type: "openai", BaseURL: "https://api.openai.com/v1",
			APIKey: apiKey, Active: 1, Currency: "USD", CloudTier: 1,
			BillingMode: string(model.BillingModePayg),
			RetryConfig: gormadapter.JSONColumn[model.RetryConfig]{Val: &model.RetryConfig{
				Enabled: true, MaxAttempts: 3, Delay: 500 * time.Millisecond,
			}},
			RateLimitConfig: gormadapter.JSONColumn[model.RateLimitConfig]{Val: &model.RateLimitConfig{
				Enabled: true, Interval: time.Second, MaxBurst: 10,
			}},
		},
		{
			ID: providerAcmeMistral, CreatedAt: now.AddDate(0, -8, 0), UpdatedAt: now.AddDate(0, 0, -5),
			OrgID: orgAcme, Name: "Mistral AI", Type: "mistral", BaseURL: "https://api.mistral.ai/v1",
			APIKey: otherKey, Active: 1, Currency: "EUR", CloudTier: 1,
			BillingMode: string(model.BillingModePayg),
		},
		{
			// Disabled provider: its models must never be routable.
			ID: providerAcmeLocal, CreatedAt: now.AddDate(0, -1, 0), UpdatedAt: now.AddDate(0, -1, 0),
			OrgID: orgAcme, Name: "Ollama (local)", Type: "openai", BaseURL: "http://localhost:11434/v1",
			APIKey: apiKey, Active: 0, Currency: "EUR",
			BillingMode: string(model.BillingModePayg),
		},
		{
			ID: providerGlobexPlan, CreatedAt: now.AddDate(0, -5, 0), UpdatedAt: now.AddDate(0, 0, -2),
			OrgID: orgGlobex, Name: "Anthropic (abonnement)", Type: "openai", BaseURL: "https://api.anthropic.com/v1",
			APIKey: apiKey, Active: 1, Currency: "USD", CloudTier: 1,
			BillingMode: string(model.BillingModeSubscription),
			SubscriptionPlan: gormadapter.JSONColumn[model.SubscriptionPlan]{Val: &model.SubscriptionPlan{
				Label: "Team — 5h rolling window",
				Constraints: []model.PlanConstraint{
					{
						Kind:        model.ConstraintRollingWindow,
						Label:       "Fenêtre 5h",
						Duration:    model.PlanDuration(5 * time.Hour),
						TokenBudget: &tokenBudget,
						ValueBudget: &valueBudget,
					},
					{
						Kind:          model.ConstraintConcurrency,
						Label:         "Requêtes simultanées",
						MaxConcurrent: &maxConcurrent,
					},
				},
			}},
		},
	}

	for _, provider := range providers {
		if err := s.create(provider); err != nil {
			return errors.WithStack(err)
		}
	}

	return nil
}

// llmModel is the local description of a seeded model, kept around because the
// usage generator needs the tariff to compute coherent costs.
type llmModel struct {
	entity   *gormadapter.LLMModel
	currency string // provider currency
	orgID    string
}

var seededModels []llmModel

func (s *seeder) seedModels(ctx context.Context) error {
	tokenLimit := &model.TokenLimitConfig{Enabled: true, MaxTokens: 200_000, Interval: time.Minute}

	models := []llmModel{
		{
			orgID: orgAcme, currency: "USD",
			entity: &gormadapter.LLMModel{
				ID: modelAcmeGPT4oMini, CreatedAt: now.AddDate(0, -13, 0), UpdatedAt: now.AddDate(0, 0, -10),
				ProviderID: providerAcmeOpenAI, OrgID: orgAcme,
				ProxyName: "gpt-4o-mini", RealModel: "gpt-4o-mini",
				Description: "Modèle économique par défaut.",
				Enabled:     1,
				PromptCostPer1KTokens: 150, CachedPromptCostPer1KTokens: 75, CompletionCostPer1KTokens: 600,
				ContextWindow: 128_000, OutputWindow: 16_384, ActiveParams: 8_000_000_000,
				TokensPerSecLow: 60, TokensPerSecHigh: 110,
				CapTools: 1, CapVision: 1,
			},
		},
		{
			orgID: orgAcme, currency: "USD",
			entity: &gormadapter.LLMModel{
				ID: modelAcmeGPT4o, CreatedAt: now.AddDate(0, -13, 0), UpdatedAt: now.AddDate(0, 0, -10),
				ProviderID: providerAcmeOpenAI, OrgID: orgAcme,
				ProxyName: "gpt-4o", RealModel: "gpt-4o",
				Description: "Modèle généraliste haut de gamme.",
				Enabled:     1,
				PromptCostPer1KTokens: 2500, CachedPromptCostPer1KTokens: 1250, CompletionCostPer1KTokens: 10000,
				ContextWindow: 128_000, OutputWindow: 16_384,
				TokensPerSecLow: 30, TokensPerSecHigh: 70,
				CapTools: 1, CapVision: 1, CapReasoning: 1,
				TokenLimitConfig: gormadapter.JSONColumn[model.TokenLimitConfig]{Val: tokenLimit},
			},
		},
		{
			orgID: orgAcme, currency: "EUR",
			entity: &gormadapter.LLMModel{
				ID: modelAcmeMistral, CreatedAt: now.AddDate(0, -8, 0), UpdatedAt: now.AddDate(0, 0, -5),
				ProviderID: providerAcmeMistral, OrgID: orgAcme,
				ProxyName: "mistral-small", RealModel: "mistral-small-latest",
				Description: "Modèle souverain, facturé en euros.",
				Enabled:     1,
				PromptCostPer1KTokens: 200, CachedPromptCostPer1KTokens: 100, CompletionCostPer1KTokens: 600,
				ContextWindow: 32_000, OutputWindow: 8_192,
				TokensPerSecLow: 80, TokensPerSecHigh: 140,
				CapTools: 1,
				ExtraBody: gormadapter.JSONColumn[map[string]any]{Val: &map[string]any{
					"safe_prompt": true,
				}},
			},
		},
		{
			orgID: orgAcme, currency: "USD",
			entity: &gormadapter.LLMModel{
				ID: modelAcmeEmbeddings, CreatedAt: now.AddDate(0, -6, 0), UpdatedAt: now.AddDate(0, -6, 0),
				ProviderID: providerAcmeOpenAI, OrgID: orgAcme,
				ProxyName: "text-embedding-3-small", RealModel: "text-embedding-3-small",
				Description: "Embeddings pour la recherche interne.",
				Enabled:     1,
				PromptCostPer1KTokens: 20,
				ContextWindow:         8_191,
				CapEmbeddings:         1,
			},
		},
		{
			orgID: orgGlobex, currency: "USD",
			entity: &gormadapter.LLMModel{
				ID: modelGlobexSonnet, CreatedAt: now.AddDate(0, -5, 0), UpdatedAt: now.AddDate(0, 0, -2),
				ProviderID: providerGlobexPlan, OrgID: orgGlobex,
				ProxyName: "claude-sonnet", RealModel: "claude-sonnet-4-5",
				Description: "Modèle principal couvert par l'abonnement.",
				Enabled:     1,
				PromptCostPer1KTokens: 3000, CachedPromptCostPer1KTokens: 300, CompletionCostPer1KTokens: 15000,
				ContextWindow: 200_000, OutputWindow: 64_000,
				TokensPerSecLow: 40, TokensPerSecHigh: 90,
				CapTools: 1, CapVision: 1, CapReasoning: 1,
			},
		},
		{
			orgID: orgGlobex, currency: "USD",
			entity: &gormadapter.LLMModel{
				// Disabled model: listing must hide it and routing must refuse it.
				ID: modelGlobexHaiku, CreatedAt: now.AddDate(0, -5, 0), UpdatedAt: now.AddDate(0, 0, -2),
				ProviderID: providerGlobexPlan, OrgID: orgGlobex,
				ProxyName: "claude-haiku", RealModel: "claude-haiku-4-5",
				Description: "Modèle rapide, temporairement désactivé.",
				Enabled:     0,
				PromptCostPer1KTokens: 100, CachedPromptCostPer1KTokens: 10, CompletionCostPer1KTokens: 500,
				ContextWindow: 200_000, OutputWindow: 32_000,
			},
		},
	}

	for _, m := range models {
		if err := s.create(m.entity); err != nil {
			return errors.WithStack(err)
		}
	}

	seededModels = models

	return nil
}

func (s *seeder) seedPipelines(ctx context.Context) error {
	// Virtual model: generator -> model(gpt-4o-mini) -> sink.
	graph := &model.PipelineGraph{
		Nodes: []model.PipelineNode{
			{ID: "gen", Type: model.NodeTypeGenerator, Position: model.NodePosition{X: 0, Y: 120}},
			{
				ID: "llm", Type: model.NodeTypeModel, Position: model.NodePosition{X: 280, Y: 120},
				Data: mustJSON(model.ModelNodeData{ProxyName: "gpt-4o-mini"}),
			},
			{ID: "out", Type: model.NodeTypeSink, Position: model.NodePosition{X: 560, Y: 120}},
		},
		Edges: []model.PipelineEdge{
			{ID: "e1", Source: "gen", SourcePort: "request", Target: "llm", TargetPort: "request"},
			{ID: "e2", Source: "llm", SourcePort: "response", Target: "out", TargetPort: "response"},
		},
	}

	if err := s.create(&gormadapter.VirtualModel{
		ID:          virtualModelAcme,
		OrgID:       orgAcme,
		Name:        "smart-router",
		Description: "Modèle virtuel de démonstration routant vers gpt-4o-mini.",
		GraphJSON:   string(mustJSON(graph)),
		CreatedAt:   now.AddDate(0, -2, 0),
		UpdatedAt:   now.AddDate(0, 0, -7),
	}); err != nil {
		return errors.WithStack(err)
	}

	// Middleware: same shape but the model node is a passthrough, so it wraps
	// whichever model the caller targeted.
	mwGraph := &model.PipelineGraph{
		Nodes: []model.PipelineNode{
			{ID: "gen", Type: model.NodeTypeGenerator, Position: model.NodePosition{X: 0, Y: 120}},
			{
				ID: "llm", Type: model.NodeTypeModel, Position: model.NodePosition{X: 280, Y: 120},
				Data: mustJSON(model.ModelNodeData{Passthrough: true}),
			},
			{ID: "out", Type: model.NodeTypeSink, Position: model.NodePosition{X: 560, Y: 120}},
		},
		Edges: []model.PipelineEdge{
			{ID: "e1", Source: "gen", SourcePort: "request", Target: "llm", TargetPort: "request"},
			{ID: "e2", Source: "llm", SourcePort: "response", Target: "out", TargetPort: "response"},
		},
	}

	if err := s.create(&gormadapter.Middleware{
		ID:           middlewareAcme,
		OrgID:        orgAcme,
		Name:         "guardrails",
		Description:  "Middleware de démonstration appliqué à tous les modèles de l'organisation.",
		Enabled:      true,
		Priority:     10,
		AppliesToAll: true,
		TargetsJSON:  "[]",
		GraphJSON:    string(mustJSON(mwGraph)),
		CreatedAt:    now.AddDate(0, -1, 0),
		UpdatedAt:    now.AddDate(0, 0, -4),
	}); err != nil {
		return errors.WithStack(err)
	}

	// A disabled middleware targeting a single model, to exercise the targeting UI.
	if err := s.create(&gormadapter.Middleware{
		ID:          "mw-acme-translate",
		OrgID:       orgAcme,
		Name:        "translate-fr",
		Description: "Middleware désactivé ciblant uniquement gpt-4o.",
		Enabled:     false,
		Priority:    20,
		TargetsJSON: string(mustJSON([]model.ModelRef{
			{Kind: model.ModelRefKindLLM, ID: modelAcmeGPT4o},
		})),
		GraphJSON: string(mustJSON(mwGraph)),
		CreatedAt: now.AddDate(0, 0, -20),
		UpdatedAt: now.AddDate(0, 0, -20),
	}); err != nil {
		return errors.WithStack(err)
	}

	// Personal virtual model owned by Carol.
	if err := s.create(&gormadapter.PersonalVirtualModel{
		ID:          personalVMCarol,
		UserID:      userCarol,
		Name:        "notes-summarizer",
		Description: "Pipeline personnel de résumé de notes.",
		GraphJSON:   string(mustJSON(graph)),
		CreatedAt:   now.AddDate(0, 0, -15),
		UpdatedAt:   now.AddDate(0, 0, -15),
	}); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

func (s *seeder) seedQuotas(ctx context.Context) error {
	i64 := func(v int64) *int64 { return &v }

	quotas := []*gormadapter.Quota{
		{
			// Calibrated against the generated history so the dashboard gauges sit
			// around a third of the budget: ~0.13 EUR/day, ~4 EUR/month.
			ID: "qta-org-acme", CreatedAt: now.AddDate(0, -3, 0), UpdatedAt: now.AddDate(0, 0, -3),
			Scope: string(model.QuotaScopeOrg), ScopeID: orgAcme, Currency: "EUR",
			DailyBudget: i64(400_000), MonthlyBudget: i64(12_000_000), YearlyBudget: i64(150_000_000),
		},
		{
			// Deliberately tight: Dave's daily consumption is generated above it,
			// so the quota enforcer must reject his calls.
			ID: "qta-user-dave", CreatedAt: now.AddDate(0, -1, 0), UpdatedAt: now.AddDate(0, 0, -1),
			Scope: string(model.QuotaScopeUser), ScopeID: userDave, Currency: "EUR",
			DailyBudget: i64(1_000), MonthlyBudget: i64(2_000_000),
		},
		{
			ID: "qta-user-carol", CreatedAt: now.AddDate(0, -2, 0), UpdatedAt: now.AddDate(0, 0, -2),
			Scope: string(model.QuotaScopeUser), ScopeID: userCarol, Currency: "EUR",
			MonthlyBudget: i64(600_000),
		},
		{
			ID: "qta-app-acme-ci", CreatedAt: now.AddDate(0, -2, 0), UpdatedAt: now.AddDate(0, -2, 0),
			Scope: string(model.QuotaScopeApplication), ScopeID: appAcmeCI, Currency: "EUR",
			DailyBudget: i64(60_000), MonthlyBudget: i64(1_500_000),
		},
		{
			ID: "qta-org-globex", CreatedAt: now.AddDate(0, -5, 0), UpdatedAt: now.AddDate(0, 0, -5),
			Scope: string(model.QuotaScopeOrg), ScopeID: orgGlobex, Currency: "USD",
			MonthlyBudget: i64(30_000_000), YearlyBudget: i64(300_000_000),
		},
	}

	for _, quota := range quotas {
		if err := s.create(quota); err != nil {
			return errors.WithStack(err)
		}
	}

	return nil
}

func (s *seeder) seedInvites(ctx context.Context) error {
	strptr := func(v string) *string { return &v }
	intptr := func(v int) *int { return &v }

	valid := now.AddDate(0, 1, 0)
	expired := now.AddDate(0, 0, -5)
	revoked := now.AddDate(0, 0, -10)

	invites := []*gormadapter.InviteToken{
		{
			// Usable: /join/inv-acme-open must let a new user join Acme.
			ID: "inv-acme-open", CreatedAt: now.AddDate(0, 0, -3), OrgID: orgAcme,
			Role: model.BuiltinKindMember, ExpiresAt: &valid, MaxUses: intptr(10), UsesCount: 2,
			CreatedByUserID: userAlice,
		},
		{
			// Email-bound invitation.
			ID: "inv-acme-grace", CreatedAt: now.AddDate(0, 0, -1), OrgID: orgAcme,
			Role: model.BuiltinKindAdmin, InviteeEmail: strptr("grace@acme.test"),
			ExpiresAt: &valid, MaxUses: intptr(1), CreatedByUserID: userAlice,
		},
		{
			ID: "inv-acme-expired", CreatedAt: now.AddDate(0, 0, -30), OrgID: orgAcme,
			Role: model.BuiltinKindMember, ExpiresAt: &expired, MaxUses: intptr(5), UsesCount: 1,
			CreatedByUserID: userBob,
		},
		{
			ID: "inv-globex-revoked", CreatedAt: now.AddDate(0, 0, -20), OrgID: orgGlobex,
			Role: model.BuiltinKindMember, ExpiresAt: &valid, MaxUses: intptr(3),
			CreatedByUserID: userErin, RevokedAt: &revoked,
		},
		{
			// Exhausted: MaxUses reached.
			ID: "inv-globex-used", CreatedAt: now.AddDate(0, 0, -12), OrgID: orgGlobex,
			Role: model.BuiltinKindMember, MaxUses: intptr(1), UsesCount: 1,
			CreatedByUserID: userErin,
		},
	}

	for _, invite := range invites {
		if err := s.create(invite); err != nil {
			return errors.WithStack(err)
		}
	}

	return nil
}

func (s *seeder) seedExchangeRates(ctx context.Context) error {
	rates := []*gormadapter.ExchangeRate{
		{FromCurrency: "USD", ToCurrency: "EUR", Rate: usdToEUR, FetchedAt: now.Add(-time.Hour)},
		{FromCurrency: "EUR", ToCurrency: "USD", Rate: 1 / usdToEUR, FetchedAt: now.Add(-time.Hour)},
		{FromCurrency: "USD", ToCurrency: "USD", Rate: 1, FetchedAt: now.Add(-time.Hour)},
		{FromCurrency: "EUR", ToCurrency: "EUR", Rate: 1, FetchedAt: now.Add(-time.Hour)},
	}

	for _, rate := range rates {
		if err := s.create(rate); err != nil {
			return errors.WithStack(err)
		}
	}

	return nil
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
