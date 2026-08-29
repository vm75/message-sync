package telegram

import (
	"context"
	"strconv"

	"github.com/go-telegram/bot/models"
	"github.com/vm75/message-sync/internal/config"
)

func telegramMigrationIDs(message *models.Message) (int64, int64, bool) {
	if message == nil {
		return 0, 0, false
	}
	if message.MigrateToChatID != 0 {
		return message.Chat.ID, message.MigrateToChatID, true
	}
	if message.MigrateFromChatID != 0 {
		return message.MigrateFromChatID, message.Chat.ID, true
	}
	return 0, 0, false
}

func (a *Adapter) handleTelegramMigration(ctx context.Context, normalizer *Normalizer, message *models.Message) bool {
	oldChatID, newChatID, migration := telegramMigrationIDs(message)
	if !migration {
		return false
	}
	if a == nil || normalizer == nil {
		return true
	}

	oldRemoteID := strconv.FormatInt(oldChatID, 10)
	newRemoteID := strconv.FormatInt(newChatID, 10)
	if config.ValidateEndpointRemoteID(config.TransportTelegram, oldRemoteID) != nil ||
		config.ValidateEndpointRemoteID(config.TransportTelegram, newRemoteID) != nil {
		if a.logger != nil {
			a.logger.Warn("Telegram group migration ignored",
				"event", "telegram_migration_ignored",
				"reason", "invalid_addressing",
			)
		}
		return true
	}

	endpoint, configured := normalizer.endpoint(oldChatID)
	if !configured {
		// A replay after the runtime mapping was already changed is harmless.
		return true
	}
	updated, err := normalizer.withChatMigration(endpoint, oldChatID, newChatID)
	if err != nil {
		if a.logger != nil {
			a.logger.Warn("Telegram group migration ignored",
				"event", "telegram_migration_ignored",
				"endpoint", string(endpoint),
				"reason", "mapping_conflict",
			)
		}
		return true
	}

	a.mu.RLock()
	migrateEndpoint := a.migrateEndpoint
	a.mu.RUnlock()
	if migrateEndpoint == nil {
		if a.logger != nil {
			a.logger.Warn("Telegram group migration ignored",
				"event", "telegram_migration_ignored",
				"endpoint", string(endpoint),
				"reason", "persistence_unavailable",
			)
		}
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := migrateEndpoint(ctx, endpoint, oldRemoteID, newRemoteID); err != nil {
		if a.logger != nil {
			a.logger.Warn("Telegram group migration failed",
				"event", "telegram_migration_failed",
				"endpoint", string(endpoint),
				"reason", "persistence",
			)
		}
		return true
	}

	a.mu.Lock()
	if a.normalizer == normalizer {
		a.normalizer = updated
	} else if a.normalizer != nil {
		if current, remapErr := a.normalizer.withChatMigration(endpoint, oldChatID, newChatID); remapErr == nil {
			a.normalizer = current
		} else if a.logger != nil {
			a.logger.Warn("Telegram group migration requires runtime reload",
				"event", "telegram_migration_reload_required",
				"endpoint", string(endpoint),
			)
		}
	}
	a.mu.Unlock()

	if a.logger != nil {
		a.logger.Info("Telegram group migration applied",
			"event", "telegram_migration_applied",
			"endpoint", string(endpoint),
		)
	}
	return true
}

func (a *Adapter) logIgnoredTelegramMessage(normalizer *Normalizer, message *models.Message, botUserID int64) {
	if a == nil || a.logger == nil || normalizer == nil || message == nil {
		return
	}
	if botUserID != 0 && message.From != nil && message.From.ID == botUserID {
		return
	}
	endpoint, configured := normalizer.endpoint(message.Chat.ID)
	if !configured {
		return
	}
	class, ignored := ignoredTelegramMessageClass(message)
	if !ignored {
		return
	}
	a.logger.Info("Telegram message ignored",
		"event", "telegram_message_ignored",
		"endpoint", string(endpoint),
		"class", class,
	)
}
