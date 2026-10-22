package gorm_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	gormpkg "gorm.io/gorm"
)

// TestUpgradeTurnsTheEmbeddingsCapabilityIntoAKind covers 202610220002 on an
// instance created before kinds existed: a model with the embeddings
// capability becomes an embeddings model, every other a completion model,
// and cap_embeddings goes away. A model whose API type serves no embeddings,
// through its provider or its own override, stays a completion model: it
// answered completions only. A model of a frozen organization keeps the
// default: the freeze guards refuse any write to it but the purge's.
func TestUpgradeTurnsTheEmbeddingsCapabilityIntoAKind(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := context.Background()
		store := xologorm.NewStore(db)
		require.NoError(t, store.Migrate(ctx))

		live := model.NewOrganization(testTenantID, "live", "Live", "")
		frozen := model.NewOrganization(testTenantID, "frozen", "Frozen", "")
		var providers []model.Provider
		for _, org := range []model.Organization{live, frozen} {
			require.NoError(t, store.CreateOrg(ctx, org))
			p := model.NewProvider(org.ID(), "OpenAI", "openai", "", "key", "USD")
			require.NoError(t, store.CreateProvider(ctx, p))
			providers = append(providers, p)
		}
		anthropic := model.NewProvider(live.ID(), "Anthropic", "anthropic", "", "key", "USD")
		require.NoError(t, store.CreateProvider(ctx, anthropic))

		// Put the schema back as the previous binary left it.
		require.NoError(t, db.Migrator().DropColumn(&xologorm.LLMModel{}, "kind"))
		require.NoError(t, db.Exec("ALTER TABLE llm_models ADD COLUMN cap_embeddings integer DEFAULT 0").Error)
		require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "202610220002").Error)

		now := time.Now()
		insert := func(id string, org model.Organization, p model.Provider, apiType string, embeddings int) {
			require.NoError(t, db.Exec(
				"INSERT INTO llm_models (id, created_at, updated_at, provider_id, org_id, proxy_name, real_model, enabled, api_type, cap_embeddings) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)",
				id, now, now, string(p.ID()), string(org.ID()), id, id, apiType, embeddings,
			).Error)
		}
		insert("chat", live, providers[0], "", 0)
		insert("vectors", live, providers[0], "", 1)
		insert("frozen-vectors", frozen, providers[1], "", 1)
		insert("claude", live, anthropic, "", 1)
		insert("claude-through-openai", live, providers[0], "anthropic", 1)
		insert("vectors-through-openai", live, anthropic, "openai", 1)
		require.NoError(t, db.Create(&xologorm.ResourceDeletion{
			Family: "organization", ResourceID: string(frozen.ID()), TenantID: string(testTenantID),
			DeletedAt: now, PurgeAfter: now.Add(time.Hour),
		}).Error)

		require.NoError(t, xologorm.NewStore(db).Migrate(ctx))

		require.False(t, db.Migrator().HasColumn("llm_models", "cap_embeddings"), "cap_embeddings must be dropped")
		for id, want := range map[string]model.ModelKind{
			"chat":                   model.ModelKindCompletion,
			"vectors":                model.ModelKindEmbeddings,
			"frozen-vectors":         model.ModelKindCompletion,
			"claude":                 model.ModelKindCompletion,
			"claude-through-openai":  model.ModelKindCompletion,
			"vectors-through-openai": model.ModelKindEmbeddings,
		} {
			got, err := store.GetLLMModelByID(ctx, model.LLMModelID(id))
			require.NoError(t, err)
			require.Equal(t, want, got.Kind(), "model %s", id)
		}
	})
}
