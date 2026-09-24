package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"aura-saku/internal/ai"
	"aura-saku/internal/apperror"
	"aura-saku/internal/auth"
	"aura-saku/internal/i18n"
	"aura-saku/internal/logger"
	"aura-saku/internal/messaging"
	"aura-saku/internal/models"
	"aura-saku/internal/quota"
	"aura-saku/internal/reward"
	"aura-saku/internal/service/budget"
	"aura-saku/internal/service/reconciliation"
	"aura-saku/internal/service/transaction"
	"aura-saku/internal/service/usage"
)

// Config bundles the small set of tunables bot.Service needs from
// internal/config, kept as its own type so NewService's parameter list
// doesn't grow with every quota/reward knob.
type Config struct {
	FreeTierDailyQuota int

	// RewardAffiliateURL enables the earn-bonus-quota flow when set; empty
	// disables it (no reward is ever offered without a real link configured).
	RewardAffiliateURL    string
	RewardBonusAmount     int
	RewardMaxClaimsPerDay int

	// DashboardBaseURL enables /dashboard when set; empty replies "not
	// available yet" instead of a broken link.
	DashboardBaseURL string
}

// Service is the platform-agnostic bot brain: it receives a normalized
// messaging.IncomingMessage (from Telegram today, WhatsApp later) and routes
// it to the right command/parsing flow. It never imports a concrete
// messaging or AI provider package — only the interfaces.
type Service struct {
	db          *gorm.DB
	redisClient *redis.Client
	messenger   messaging.Messenger
	aiClient    ai.AIClient
	txSvc       *transaction.Service
	budgetSvc   *budget.Service
	reconSvc    *reconciliation.Service
	usageSvc    *usage.Service
	cfg         Config
}

func NewService(db *gorm.DB, redisClient *redis.Client, messenger messaging.Messenger, aiClient ai.AIClient, txSvc *transaction.Service, budgetSvc *budget.Service, reconSvc *reconciliation.Service, usageSvc *usage.Service, cfg Config) *Service {
	return &Service{
		db:          db,
		redisClient: redisClient,
		messenger:   messenger,
		aiClient:    aiClient,
		txSvc:       txSvc,
		budgetSvc:   budgetSvc,
		reconSvc:    reconSvc,
		usageSvc:    usageSvc,
		cfg:         cfg,
	}
}

// HandleUpdate is the single entrypoint invoked (async, off the webhook's
// request goroutine) for every inbound message.
func (s *Service) HandleUpdate(ctx context.Context, msg *messaging.IncomingMessage) {
	user, err := s.getOrCreateUser(msg)
	if err != nil {
		logger.Error(logger.SvcUserResolveFailed, fmt.Sprintf("get or create user for chat %d", msg.ChatID), err)
		return
	}

	if err := s.messenger.SendTypingIndicator(ctx, msg.ChatID); err != nil {
		logger.Error(logger.TelegramSendFailed, fmt.Sprintf("send typing indicator to chat %d", msg.ChatID), err)
	}

	switch msg.Command {
	case "/start":
		s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeyWelcome))
	case "/undo":
		s.handleUndo(ctx, user, msg)
	case "/sync":
		s.handleSync(ctx, user, msg)
	case "/language":
		s.handleLanguage(ctx, user, msg)
	case "/claim":
		s.handleClaim(ctx, user, msg)
	case "/dashboard":
		s.handleDashboard(ctx, user, msg)
	default:
		s.handleTransactionInput(ctx, user, msg)
	}
}

// getOrCreateUser resolves the user, seeding Language from Telegram's own
// language_code on first contact (see i18n.DetectFromTelegramCode). Attrs
// only applies on insert, so returning users keep whatever /language they
// last chose.
func (s *Service) getOrCreateUser(msg *messaging.IncomingMessage) (*models.User, error) {
	var user models.User
	result := s.db.Where(models.User{TelegramChatID: msg.ChatID}).
		Attrs(models.User{
			DefaultCurrency: "IDR",
			Tier:            models.TierFree,
			Language:        i18n.DetectFromTelegramCode(msg.LanguageCode),
		}).
		FirstOrCreate(&user)
	if result.Error != nil {
		return nil, result.Error
	}
	return &user, nil
}

func (s *Service) handleLanguage(ctx context.Context, user *models.User, msg *messaging.IncomingMessage) {
	lang, ok := i18n.ParseCommandInput(msg.CommandArgs)
	if !ok {
		s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeyLanguageUsage))
		return
	}

	if err := s.db.WithContext(ctx).Model(&models.User{}).Where("id = ?", user.ID).Update("language", lang).Error; err != nil {
		logger.Error(logger.SvcUserResolveFailed, fmt.Sprintf("update language for user %d", user.ID), err)
		s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeyLanguageUsage))
		return
	}

	s.reply(ctx, msg.ChatID, i18n.T(lang, i18n.KeyLanguageSet))
}

func (s *Service) handleUndo(ctx context.Context, user *models.User, msg *messaging.IncomingMessage) {
	undoneTx, err := s.txSvc.UndoLast(ctx, user.ID)
	if err != nil {
		if errors.Is(err, apperror.ErrNoUndoableTransaction) {
			s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeyUndoNone))
			return
		}
		logger.Error(logger.SvcUndoFailed, fmt.Sprintf("undo last transaction for user %d", user.ID), err)
		s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeyUndoFailed))
		return
	}
	s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeyUndoSuccess,
		undoneTx.Type, undoneTx.OriginalAmount, undoneTx.OriginalCurrency))
}

func (s *Service) handleSync(ctx context.Context, user *models.User, msg *messaging.IncomingMessage) {
	amount, err := strconv.ParseFloat(msg.CommandArgs, 64)
	if err != nil {
		s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeySyncUsage))
		return
	}

	rec, err := s.reconSvc.Sync(ctx, user.ID, amount)
	if err != nil {
		logger.Error(logger.SvcSyncFailed, fmt.Sprintf("sync balance for user %d", user.ID), err)
		s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeySyncFailed))
		return
	}

	if rec.Status == models.ReconMatched {
		s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeySyncMatched, rec.CalculatedBalance))
		return
	}

	s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeySyncDiscrepancy,
		rec.Discrepancy, rec.ActualBalance, rec.CalculatedBalance))
}

func (s *Service) handleClaim(ctx context.Context, user *models.User, msg *messaging.IncomingMessage) {
	code := strings.TrimSpace(msg.CommandArgs)
	if code == "" {
		s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeyClaimUsage))
		return
	}

	result, err := reward.Claim(ctx, s.redisClient, msg.ChatID, code, s.cfg.RewardBonusAmount, s.cfg.RewardMaxClaimsPerDay)
	if err != nil {
		logger.Error(logger.RedisOpFailed, fmt.Sprintf("claim reward for chat %d", msg.ChatID), err)
		s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeyClaimInvalidCode))
		return
	}

	switch result {
	case reward.ClaimGranted:
		s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeyClaimSuccess, s.cfg.RewardBonusAmount))
	case reward.ClaimLimitReached:
		s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeyClaimLimitReached))
	default:
		s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeyClaimInvalidCode))
	}
}

func (s *Service) handleDashboard(ctx context.Context, user *models.User, msg *messaging.IncomingMessage) {
	if s.cfg.DashboardBaseURL == "" {
		s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeyDashboardUnavailable))
		return
	}

	token, err := auth.IssueLoginToken(ctx, s.redisClient, user.ID)
	if err != nil {
		logger.Error(logger.RedisOpFailed, fmt.Sprintf("issue dashboard login token for user %d", user.ID), err)
		s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeyDashboardUnavailable))
		return
	}

	link := fmt.Sprintf("%s/auth/verify?token=%s", strings.TrimRight(s.cfg.DashboardBaseURL, "/"), token)
	s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeyDashboardLink, link))
}

// enforceQuota checks the free-tier daily AI-prompt cap before any AI call
// is made, replying (and returning true) if the user is blocked. Pro tier
// always passes. Redis errors on the core count/bonus reads fail OPEN
// (allow the request through) rather than incorrectly blocking a user over
// an infrastructure hiccup — this is a cost guardrail, not a security gate.
func (s *Service) enforceQuota(ctx context.Context, user *models.User, msg *messaging.IncomingMessage) bool {
	if user.Tier == models.TierPro {
		return false
	}

	count, err := quota.CountToday(ctx, s.redisClient, msg.ChatID)
	if err != nil {
		logger.Error(logger.RedisOpFailed, fmt.Sprintf("read quota count for chat %d, failing open", msg.ChatID), err)
		return false
	}

	bonus, err := reward.BonusGrantedToday(ctx, s.redisClient, msg.ChatID)
	if err != nil {
		logger.Error(logger.RedisOpFailed, fmt.Sprintf("read reward bonus for chat %d, failing open", msg.ChatID), err)
		return false
	}

	if count < s.cfg.FreeTierDailyQuota+bonus {
		return false
	}

	if s.cfg.RewardAffiliateURL != "" {
		claims, cErr := reward.ClaimsToday(ctx, s.redisClient, msg.ChatID)
		if cErr != nil {
			logger.Error(logger.RedisOpFailed, fmt.Sprintf("read reward claims for chat %d", msg.ChatID), cErr)
		} else if claims < s.cfg.RewardMaxClaimsPerDay {
			if code, iErr := reward.IssueCode(ctx, s.redisClient, msg.ChatID); iErr == nil {
				s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeyQuotaExceededWithReward,
					s.cfg.FreeTierDailyQuota, s.cfg.RewardBonusAmount, s.cfg.RewardAffiliateURL, code))
				return true
			} else {
				logger.Error(logger.RedisOpFailed, "issue reward code", iErr)
			}
		}
	}

	s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeyQuotaExceededNoReward, s.cfg.FreeTierDailyQuota))
	return true
}

func (s *Service) handleTransactionInput(ctx context.Context, user *models.User, msg *messaging.IncomingMessage) {
	if s.enforceQuota(ctx, user, msg) {
		return
	}

	var (
		parsed      *ai.ParsedTransaction
		usageMeta   *ai.UsageMetadata
		err         error
		source      models.TransactionSource
		raw         string
		requestType string
	)

	preferredLanguage := string(user.Language)

	if msg.IsVoice {
		source = models.SourceVoice
		requestType = "audio_transcribe"
		data, mimeType, dlErr := s.messenger.DownloadFile(ctx, msg.VoiceFileID)
		if dlErr != nil {
			logger.Error(logger.TelegramDownloadFailed, fmt.Sprintf("download voice file for user %d", user.ID), dlErr)
			s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeyVoiceDownloadFailed))
			return
		}
		raw = "[voice note]"
		parsed, usageMeta, err = s.aiClient.TranscribeAndParseAudio(ctx, data, mimeType, user.DefaultCurrency, preferredLanguage)
	} else {
		source = models.SourceText
		requestType = "text_parse"
		raw = msg.Text
		parsed, usageMeta, err = s.aiClient.ParseTransactionText(ctx, msg.Text, user.DefaultCurrency, preferredLanguage)
	}

	if err != nil {
		if errors.Is(err, apperror.ErrProviderAudioUnsupported) {
			// Expected capability gap, not a failure — no error log.
			s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeyVoiceUnsupported, s.aiClient.ProviderName()))
			return
		}
		logger.Error(logger.AIParseFailed, fmt.Sprintf("AI parse for user %d", user.ID), err)
		s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeyAITrouble))
		return
	}

	// A successful AI call counts against today's quota regardless of tier
	// (Redis is only ever checked for free-tier users, but recording it for
	// everyone gives complete audit data) and regardless of whether the
	// result was ambiguous — the API cost was already incurred either way.
	if qErr := quota.Increment(ctx, s.redisClient, msg.ChatID); qErr != nil {
		logger.Error(logger.RedisOpFailed, fmt.Sprintf("increment quota for chat %d", msg.ChatID), qErr)
	}
	go s.usageSvc.RecordAICall(context.Background(), user.ID, models.AIProvider(s.aiClient.ProviderName()), requestType, usageMeta)

	if parsed.IsAmbiguous {
		s.reply(ctx, msg.ChatID, parsed.ClarificationQuestion)
		return
	}

	createdTx, err := s.txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, parsed, source, raw)
	if err != nil {
		logger.Error(logger.SvcCreateTxFailed, fmt.Sprintf("create transaction for user %d", user.ID), err)
		s.reply(ctx, msg.ChatID, i18n.T(user.Language, i18n.KeyTransactionSaveFailed))
		return
	}

	reply := i18n.T(user.Language, i18n.KeyTransactionRecorded,
		createdTx.Type, createdTx.OriginalAmount, createdTx.OriginalCurrency, parsed.CategoryName)

	// Instant budget feedback: only expenses can trip a Needs/Wants
	// guardrail, so skip the lookup entirely for other transaction types.
	if createdTx.Type == models.TxExpense {
		if status, budgetErr := s.budgetSvc.EvaluateBudget(ctx, user.ID, user.Language); budgetErr != nil {
			logger.Error(logger.SvcBudgetEvalFailed, fmt.Sprintf("evaluate budget for user %d", user.ID), budgetErr)
		} else {
			for _, warning := range status.Warnings {
				reply += "\n⚠️ " + warning
			}
		}
	}

	s.reply(ctx, msg.ChatID, reply)
}

func (s *Service) reply(ctx context.Context, chatID int64, text string) {
	if err := s.messenger.SendMessage(ctx, chatID, text); err != nil {
		logger.Error(logger.TelegramSendFailed, fmt.Sprintf("send reply to chat %d", chatID), err)
	}
}
