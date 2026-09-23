package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"gorm.io/gorm"

	"aura-saku/internal/ai"
	"aura-saku/internal/apperror"
	"aura-saku/internal/logger"
	"aura-saku/internal/messaging"
	"aura-saku/internal/models"
	"aura-saku/internal/service/reconciliation"
	"aura-saku/internal/service/transaction"
)

// Service is the platform-agnostic bot brain: it receives a normalized
// messaging.IncomingMessage (from Telegram today, WhatsApp later) and routes
// it to the right command/parsing flow. It never imports a concrete
// messaging or AI provider package — only the interfaces.
type Service struct {
	db        *gorm.DB
	messenger messaging.Messenger
	aiClient  ai.AIClient
	txSvc     *transaction.Service
	reconSvc  *reconciliation.Service
}

func NewService(db *gorm.DB, messenger messaging.Messenger, aiClient ai.AIClient, txSvc *transaction.Service, reconSvc *reconciliation.Service) *Service {
	return &Service{
		db:        db,
		messenger: messenger,
		aiClient:  aiClient,
		txSvc:     txSvc,
		reconSvc:  reconSvc,
	}
}

// HandleUpdate is the single entrypoint invoked (async, off the webhook's
// request goroutine) for every inbound message.
func (s *Service) HandleUpdate(ctx context.Context, msg *messaging.IncomingMessage) {
	user, err := s.getOrCreateUser(msg.ChatID)
	if err != nil {
		logger.Error(logger.SvcUserResolveFailed, fmt.Sprintf("get or create user for chat %d", msg.ChatID), err)
		return
	}

	if err := s.messenger.SendTypingIndicator(ctx, msg.ChatID); err != nil {
		logger.Error(logger.TelegramSendFailed, fmt.Sprintf("send typing indicator to chat %d", msg.ChatID), err)
	}

	switch msg.Command {
	case "/start":
		s.reply(ctx, msg.ChatID, "Welcome to Aura-Saku! Send me something like \"lunch 25000\" to record an expense, or a voice note.")
	case "/undo":
		s.handleUndo(ctx, user, msg)
	case "/sync":
		s.handleSync(ctx, user, msg)
	default:
		s.handleTransactionInput(ctx, user, msg)
	}
}

func (s *Service) getOrCreateUser(chatID int64) (*models.User, error) {
	var user models.User
	result := s.db.Where(models.User{TelegramChatID: chatID}).
		Attrs(models.User{DefaultCurrency: "IDR", Tier: models.TierFree}).
		FirstOrCreate(&user)
	if result.Error != nil {
		return nil, result.Error
	}
	return &user, nil
}

func (s *Service) handleUndo(ctx context.Context, user *models.User, msg *messaging.IncomingMessage) {
	undoneTx, err := s.txSvc.UndoLast(ctx, user.ID)
	if err != nil {
		if errors.Is(err, apperror.ErrNoUndoableTransaction) {
			s.reply(ctx, msg.ChatID, "You don't have any transactions to undo yet.")
			return
		}
		logger.Error(logger.SvcUndoFailed, fmt.Sprintf("undo last transaction for user %d", user.ID), err)
		s.reply(ctx, msg.ChatID, "Something went wrong undoing your last transaction.")
		return
	}
	s.reply(ctx, msg.ChatID, fmt.Sprintf(
		"Undone: %s of %.2f %s.",
		undoneTx.Type, undoneTx.OriginalAmount, undoneTx.OriginalCurrency,
	))
}

func (s *Service) handleSync(ctx context.Context, user *models.User, msg *messaging.IncomingMessage) {
	amount, err := strconv.ParseFloat(msg.CommandArgs, 64)
	if err != nil {
		s.reply(ctx, msg.ChatID, "Usage: /sync <actual balance>, e.g. /sync 1500000")
		return
	}

	_, err = s.reconSvc.Sync(ctx, user.ID, amount)
	if err != nil {
		if errors.Is(err, apperror.ErrNotImplemented) {
			s.reply(ctx, msg.ChatID, "Balance sync isn't wired up yet — coming in a future update.")
			return
		}
		logger.Error(logger.SvcSyncFailed, fmt.Sprintf("sync balance for user %d", user.ID), err)
		s.reply(ctx, msg.ChatID, "Something went wrong syncing your balance.")
		return
	}
	s.reply(ctx, msg.ChatID, "Balance synced.")
}

func (s *Service) handleTransactionInput(ctx context.Context, user *models.User, msg *messaging.IncomingMessage) {
	var (
		parsed *ai.ParsedTransaction
		err    error
		source models.TransactionSource
		raw    string
	)

	if msg.IsVoice {
		source = models.SourceVoice
		data, mimeType, dlErr := s.messenger.DownloadFile(ctx, msg.VoiceFileID)
		if dlErr != nil {
			logger.Error(logger.TelegramDownloadFailed, fmt.Sprintf("download voice file for user %d", user.ID), dlErr)
			s.reply(ctx, msg.ChatID, "Sorry, I couldn't download your voice note. Please try again.")
			return
		}
		raw = "[voice note]"
		parsed, _, err = s.aiClient.TranscribeAndParseAudio(ctx, data, mimeType, user.DefaultCurrency)
	} else {
		source = models.SourceText
		raw = msg.Text
		parsed, _, err = s.aiClient.ParseTransactionText(ctx, msg.Text, user.DefaultCurrency)
	}

	if err != nil {
		logger.Error(logger.AIParseFailed, fmt.Sprintf("AI parse for user %d", user.ID), err)
		s.reply(ctx, msg.ChatID, "Sorry, I had trouble processing that. Please try again in a moment.")
		return
	}

	if parsed.IsAmbiguous {
		s.reply(ctx, msg.ChatID, parsed.ClarificationQuestion)
		return
	}

	createdTx, err := s.txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, parsed, source, raw)
	if err != nil {
		logger.Error(logger.SvcCreateTxFailed, fmt.Sprintf("create transaction for user %d", user.ID), err)
		s.reply(ctx, msg.ChatID, "Something went wrong saving your transaction.")
		return
	}

	s.reply(ctx, msg.ChatID, fmt.Sprintf(
		"Recorded: %s of %.2f %s for \"%s\".",
		createdTx.Type, createdTx.OriginalAmount, createdTx.OriginalCurrency, parsed.CategoryName,
	))
}

func (s *Service) reply(ctx context.Context, chatID int64, text string) {
	if err := s.messenger.SendMessage(ctx, chatID, text); err != nil {
		logger.Error(logger.TelegramSendFailed, fmt.Sprintf("send reply to chat %d", chatID), err)
	}
}
