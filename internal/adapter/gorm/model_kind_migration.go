package gorm

import (
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"gorm.io/gorm"
)

// migrateModelKind adds kind to llm_models. Its column default makes every
// model a completion model, but the ones with the former embeddings
// capability, which become embeddings models, before cap_embeddings is
// dropped: a model answers the routes of one kind only.
//
// Only a model whose API type serves embeddings converts: on another API the
// capability never reached a route, and the model kept answering
// completions, as it still does.
//
// The models of a frozen scope keep the default. Their deletion is recorded,
// the purge removes them, and the freeze triggers refuse any write to them
// but the purge's.
func migrateModelKind(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&LLMModel{}); err != nil {
		return errors.WithStack(err)
	}
	if !tx.Migrator().HasColumn("llm_models", "cap_embeddings") {
		return nil
	}

	var embeddingsTypes []string
	for _, t := range model.ProviderTypes() {
		if model.ProviderTypeServes(t, model.ModelKindEmbeddings) {
			embeddingsTypes = append(embeddingsTypes, t)
		}
	}

	update := tx.Table("llm_models").
		Where("cap_embeddings <> 0").
		Where(`(CASE WHEN llm_models.api_type <> '' THEN llm_models.api_type
			ELSE (SELECT providers.type FROM providers WHERE providers.id = llm_models.provider_id) END) IN ?`, embeddingsTypes)
	if tx.Migrator().HasTable("resource_deletions") {
		update = update.Where(notFrozen("llm_models", "llm_models"))
	}
	if err := update.Update("kind", string(model.ModelKindEmbeddings)).Error; err != nil {
		return errors.Wrap(err, "could not turn the embeddings capability into a kind")
	}
	return errors.WithStack(tx.Exec("ALTER TABLE llm_models DROP COLUMN cap_embeddings").Error)
}

// rollbackModelKind restores cap_embeddings from the embeddings kind, then
// drops kind. A decision model comes back as a completion model, the only
// kind there was. The models of a frozen scope are skipped, as on the way up:
// the freeze triggers refuse the write.
func rollbackModelKind(tx *gorm.DB) error {
	if !tx.Migrator().HasColumn("llm_models", "cap_embeddings") {
		if err := tx.Exec("ALTER TABLE llm_models ADD COLUMN cap_embeddings integer DEFAULT 0").Error; err != nil {
			return errors.WithStack(err)
		}
	}
	update := tx.Table("llm_models").Where("kind = ?", string(model.ModelKindEmbeddings))
	if tx.Migrator().HasTable("resource_deletions") {
		update = update.Where(notFrozen("llm_models", "llm_models"))
	}
	if err := update.Update("cap_embeddings", 1).Error; err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(tx.Migrator().DropColumn(&LLMModel{}, "kind"))
}
