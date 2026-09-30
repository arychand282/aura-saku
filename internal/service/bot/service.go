package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"aura-saku/internal/ai"
	"aura-saku/internal/apperror"
	"aura-saku/internal/auth"
	"aura-saku/internal/i18n"
	"aura-saku/internal/idempotency"
	"aura-saku/internal/logger"
	"aura-saku/internal/messaging"
	"aura-saku/internal/models"
	"aura-saku/internal/quota"
	"aura-saku/internal/reward"
	"aura-saku/internal/service/budget"
	"aura-saku/internal/service/conversation"
	"aura-saku/internal/service/reconciliation"
	"aura-saku/internal/service/recurring"
	"aura-saku/internal/service/report"
	"aura-saku/internal/service/savingsgoal"
	"aura-saku/internal/service/transaction"
	"aura-saku/internal/service/usage"
)

// Config bundles the small set of tunables bot.Service needs from
// internal/config, kept as its own type so NewService's parameter list
// doesn't grow with every quota/reward knob.
type Config struct {
	FreeTierDailyQuota int

	// MaxInputChars caps a text message's length before it's sent to the AI
	// at all — a cost/abuse guardrail, not a product constraint. Enforced
	// here (not in HTTP middleware) so a rejected message still gets a real
	// reply instead of vanishing silently from the user's perspective.
	MaxInputChars int

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
	reportSvc   *report.Service
	convSvc     *conversation.Service
	recurSvc    *recurring.Service
	goalSvc     *savingsgoal.Service
	cfg         Config
}

func NewService(db *gorm.DB, redisClient *redis.Client, messenger messaging.Messenger, aiClient ai.AIClient, txSvc *transaction.Service, budgetSvc *budget.Service, reconSvc *reconciliation.Service, usageSvc *usage.Service, reportSvc *report.Service, convSvc *conversation.Service, recurSvc *recurring.Service, goalSvc *savingsgoal.Service, cfg Config) *Service {
	return &Service{
		db:          db,
		redisClient: redisClient,
		messenger:   messenger,
		aiClient:    aiClient,
		txSvc:       txSvc,
		budgetSvc:   budgetSvc,
		reconSvc:    reconSvc,
		usageSvc:    usageSvc,
		reportSvc:   reportSvc,
		convSvc:     convSvc,
		recurSvc:    recurSvc,
		goalSvc:     goalSvc,
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

	// Log the user's side of this turn before doing anything else — msg.Text
	// carries the full raw text even for a command (see
	// telegram.Client.ParseWebhookPayload), so a complete transcript,
	// commands included, falls out of this one call. Logged for every
	// message type uniformly; only handleTransactionInput's natural-language
	// path actually reads this back as AI context (see BuildContext), but a
	// complete history is the point of storing it at all.
	content := msg.Text
	if msg.IsVoice {
		content = "[voice note]"
	}
	if conv, convErr := s.convSvc.GetOrCreate(ctx, user.ID); convErr != nil {
		logger.Error(logger.SvcConversationFailed, fmt.Sprintf("get or create conversation for user %d", user.ID), convErr)
	} else if appendErr := s.convSvc.AppendMessage(ctx, conv.ID, models.MessageRoleUser, content); appendErr != nil {
		logger.Error(logger.SvcConversationFailed, fmt.Sprintf("log user message for user %d", user.ID), appendErr)
	}

	if err := s.messenger.SendTypingIndicator(ctx, msg.ChatID); err != nil {
		logger.Error(logger.TelegramSendFailed, fmt.Sprintf("send typing indicator to chat %d", msg.ChatID), err)
	}

	switch msg.Command {
	case "/start":
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyWelcome))
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
	case "/recurring":
		s.handleRecurring(ctx, user, msg)
	case "/budget":
		s.handleBudget(ctx, user, msg)
	case "/goal":
		s.handleGoal(ctx, user, msg)
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
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyLanguageUsage))
		return
	}

	if err := s.db.WithContext(ctx).Model(&models.User{}).Where("id = ?", user.ID).Update("language", lang).Error; err != nil {
		logger.Error(logger.SvcUserResolveFailed, fmt.Sprintf("update language for user %d", user.ID), err)
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyLanguageUsage))
		return
	}

	s.reply(ctx, user, i18n.T(lang, i18n.KeyLanguageSet))
}

func (s *Service) handleUndo(ctx context.Context, user *models.User, msg *messaging.IncomingMessage) {
	undoneTx, err := s.txSvc.UndoLast(ctx, user.ID)
	if err != nil {
		if errors.Is(err, apperror.ErrNoUndoableTransaction) {
			s.reply(ctx, user, i18n.T(user.Language, i18n.KeyUndoNone))
			return
		}
		logger.Error(logger.SvcUndoFailed, fmt.Sprintf("undo last transaction for user %d", user.ID), err)
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyUndoFailed))
		return
	}
	s.reply(ctx, user, i18n.T(user.Language, i18n.KeyUndoSuccess,
		undoneTx.Type, i18n.FormatMoney(user.Language, undoneTx.OriginalAmount, undoneTx.OriginalCurrency)))
}

func (s *Service) handleSync(ctx context.Context, user *models.User, msg *messaging.IncomingMessage) {
	if strings.TrimSpace(msg.CommandArgs) == "" {
		// No actual balance to reconcile against — just report the
		// calculated balance, same as the natural-language "what's my
		// balance" path (see handleBalanceQuery).
		s.handleBalanceQuery(ctx, user, msg)
		return
	}

	amount, err := strconv.ParseFloat(msg.CommandArgs, 64)
	if err != nil {
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeySyncUsage))
		return
	}

	rec, err := s.reconSvc.Sync(ctx, user.ID, amount)
	if err != nil {
		logger.Error(logger.SvcSyncFailed, fmt.Sprintf("sync balance for user %d", user.ID), err)
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeySyncFailed))
		return
	}

	if rec.Status == models.ReconMatched {
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeySyncMatched, rec.CalculatedBalance))
		return
	}

	s.reply(ctx, user, i18n.T(user.Language, i18n.KeySyncDiscrepancy,
		rec.Discrepancy, rec.ActualBalance, rec.CalculatedBalance))
}

// handleBalanceQuery answers "what's my balance" with no reconciliation
// side effects (no BalanceReconciliation row, no adjustment) — shared by
// /sync with no argument and the natural-language ai.IntentBalance path, so
// both report the exact same number via the same reply text.
func (s *Service) handleBalanceQuery(ctx context.Context, user *models.User, msg *messaging.IncomingMessage) {
	balance, err := s.reconSvc.Balance(ctx, user.ID)
	if err != nil {
		logger.Error(logger.SvcSyncFailed, fmt.Sprintf("compute balance for user %d", user.ID), err)
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeySyncFailed))
		return
	}
	s.reply(ctx, user, i18n.T(user.Language, i18n.KeyBalance, i18n.FormatMoney(user.Language, balance, user.DefaultCurrency)))
}

// handleRecurring is /recurring's dispatcher: no argument lists the user's
// active recurring transactions with their real ids (so /recurring cancel
// <id> below has something to reference); "cancel <id>" deactivates one.
// List/cancel are plain commands rather than natural language — unlike
// creation, there's no ambiguity to resolve (an id is exact), so a command
// is simpler than teaching the model a lookup tool for this.
func (s *Service) handleRecurring(ctx context.Context, user *models.User, msg *messaging.IncomingMessage) {
	args := strings.Fields(msg.CommandArgs)
	if len(args) == 2 && strings.EqualFold(args[0], "cancel") {
		s.handleRecurringCancel(ctx, user, args[1])
		return
	}
	if len(args) > 0 {
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyRecurringCancelUsage))
		return
	}

	list, err := s.recurSvc.ListActive(ctx, user.ID)
	if err != nil {
		logger.Error(logger.SvcRecurringFailed, fmt.Sprintf("list recurring transactions for user %d", user.ID), err)
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyRecurringCreateFailed))
		return
	}
	if len(list) == 0 {
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyRecurringListEmpty))
		return
	}

	lines := make([]string, 0, len(list))
	for _, rt := range list {
		modeSuffix := ""
		if rt.ReminderOnly {
			modeSuffix = i18n.T(user.Language, i18n.KeyRecurringModeReminder)
		}
		lines = append(lines, i18n.T(user.Language, i18n.KeyRecurringListItem,
			rt.ID, rt.Type, i18n.FormatMoney(user.Language, rt.Amount, rt.Currency), rt.CategoryName, rt.DayOfMonth, rt.NextRunAt.Format("2006-01-02"), modeSuffix))
	}
	s.reply(ctx, user, strings.Join(lines, "\n"))
}

// handleRecurringCancel backs "/recurring cancel <id>". idArg is validated
// as a plain non-negative integer here rather than left to
// recurring.Service.Cancel — a malformed id (typo, or an id copied from
// somewhere else entirely) gets the same usage reply as no id at all,
// instead of an opaque "not found".
func (s *Service) handleRecurringCancel(ctx context.Context, user *models.User, idArg string) {
	id, err := strconv.ParseUint(idArg, 10, 64)
	if err != nil {
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyRecurringCancelUsage))
		return
	}

	rt, err := s.recurSvc.Cancel(ctx, user.ID, uint(id))
	if err != nil {
		if errors.Is(err, apperror.ErrRecurringNotFound) {
			s.reply(ctx, user, i18n.T(user.Language, i18n.KeyRecurringCancelNotFound))
			return
		}
		logger.Error(logger.SvcRecurringFailed, fmt.Sprintf("cancel recurring transaction for user %d", user.ID), err)
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyRecurringCreateFailed))
		return
	}

	s.reply(ctx, user, i18n.T(user.Language, i18n.KeyRecurringCancelSuccess,
		rt.Type, i18n.FormatMoney(user.Language, rt.Amount, rt.Currency), rt.CategoryName))
}

// handleRecurringDelete cancels a recurring transaction or bill reminder
// described by parsed (ai.IntentRecurringDelete — e.g. "batalin netflix"),
// identified via get_recurring_transactions by a chained-tool-calling
// AIClient (see ai.ChainedCorrector). Reuses the exact same
// recurring.Service.Cancel and reply text as the command-based
// /recurring cancel <id> — this is the same action, just reached by
// naming the target instead of looking up its id first.
func (s *Service) handleRecurringDelete(ctx context.Context, user *models.User, parsed *ai.ParsedTransaction) {
	rt, err := s.recurSvc.Cancel(ctx, user.ID, parsed.TargetRecurringTransactionID)
	if err != nil {
		if errors.Is(err, apperror.ErrRecurringNotFound) {
			s.reply(ctx, user, i18n.T(user.Language, i18n.KeyRecurringCancelNotFound))
			return
		}
		logger.Error(logger.SvcRecurringFailed, fmt.Sprintf("delete recurring transaction via natural language for user %d", user.ID), err)
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyRecurringCreateFailed))
		return
	}
	s.reply(ctx, user, i18n.T(user.Language, i18n.KeyRecurringCancelSuccess,
		rt.Type, i18n.FormatMoney(user.Language, rt.Amount, rt.Currency), rt.CategoryName))
}

// recurringUpdatePair detects the "update" pattern a chained-tool-calling
// provider produces when a message describes changing an existing
// recurring transaction/bill reminder (e.g. "ubah netflix jadi 200rb") —
// there's no dedicated update tool, so the model instead chains a
// delete_recurring_transaction call with a create_recurring_transaction/
// create_bill_reminder call in the SAME message (see the explicit
// exception carved into internal/ai/prompt/tool_schema.go's "at most once
// per message" rule for exactly this pair). Returns ok=false unless
// parsedList contains EXACTLY one of each — any other shape (including two
// of the same kind, which shouldn't happen given each tool is still capped
// at one call) falls through to the normal single-intent dispatch instead
// of risking a wrong guess about which pair to treat as an update.
func recurringUpdatePair(parsedList []*ai.ParsedTransaction) (deleted, created *ai.ParsedTransaction, ok bool) {
	for _, pt := range parsedList {
		switch pt.Intent {
		case ai.IntentRecurringDelete:
			if deleted != nil {
				return nil, nil, false
			}
			deleted = pt
		case ai.IntentRecurringCreate:
			if created != nil {
				return nil, nil, false
			}
			created = pt
		}
	}
	return deleted, created, deleted != nil && created != nil
}

// handleRecurringUpdate stitches a delete_recurring_transaction +
// create_recurring_transaction/create_bill_reminder pair (see
// recurringUpdatePair) into one atomic-from-the-user's-view operation: the
// old schedule is canceled, the new one (describing whatever changed —
// amount, day of month, or even switching between auto-record and
// reminder-only) is created, and a single combined reply is sent instead of
// two separate ones. A failure to find/cancel the old schedule (e.g. it was
// already canceled by the time this ran) is logged but does NOT block
// creating the new one — the create is the part the user actually asked
// for; losing track of an already-gone old row is not worth failing over.
func (s *Service) handleRecurringUpdate(ctx context.Context, user *models.User, deleted, created *ai.ParsedTransaction) {
	oldRt, cancelErr := s.recurSvc.Cancel(ctx, user.ID, deleted.TargetRecurringTransactionID)
	if cancelErr != nil && !errors.Is(cancelErr, apperror.ErrRecurringNotFound) {
		logger.Error(logger.SvcRecurringFailed, fmt.Sprintf("cancel old recurring transaction during update for user %d", user.ID), cancelErr)
	}

	newRt, err := s.recurSvc.Create(ctx, user.ID, created)
	if err != nil {
		logger.Error(logger.SvcRecurringFailed, fmt.Sprintf("create updated recurring transaction for user %d", user.ID), err)
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyRecurringCreateFailed))
		return
	}

	replyKey := i18n.KeyRecurringCreated
	if newRt.ReminderOnly {
		replyKey = i18n.KeyBillReminderCreated
	}
	reply := i18n.T(user.Language, replyKey,
		newRt.Type, i18n.FormatMoney(user.Language, newRt.Amount, newRt.Currency), newRt.CategoryName, newRt.DayOfMonth, newRt.NextRunAt.Format("2006-01-02"))

	if cancelErr == nil && oldRt != nil {
		reply += "\n" + i18n.T(user.Language, i18n.KeyRecurringUpdatedFrom, i18n.FormatMoney(user.Language, oldRt.Amount, oldRt.Currency))
	}

	s.reply(ctx, user, reply)
}

// handleBudget is /budget's dispatcher: no argument shows the user's
// current Needs/Wants/Savings allocation; three numeric arguments set a
// new one. A plain command rather than a natural-language AI tool — unlike
// a transaction, there's no ambiguity to parse out of "50 30 20", and
// getting a financial-settings change wrong via AI misparsing would be a
// worse failure mode than requiring exact syntax here.
func (s *Service) handleBudget(ctx context.Context, user *models.User, msg *messaging.IncomingMessage) {
	args := strings.Fields(msg.CommandArgs)
	if len(args) == 0 {
		b, err := s.budgetSvc.CurrentBudget(ctx, user.ID)
		if err != nil {
			logger.Error(logger.SvcBudgetEvalFailed, fmt.Sprintf("get current budget for user %d", user.ID), err)
			s.reply(ctx, user, i18n.T(user.Language, i18n.KeyBudgetSetFailed))
			return
		}
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyBudgetShow, b.NeedsPercent, b.WantsPercent, b.SavingsPercent))
		return
	}

	if len(args) != 3 {
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyBudgetSetUsage))
		return
	}
	needs, errN := strconv.ParseFloat(args[0], 64)
	wants, errW := strconv.ParseFloat(args[1], 64)
	savings, errS := strconv.ParseFloat(args[2], 64)
	if errN != nil || errW != nil || errS != nil {
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyBudgetSetUsage))
		return
	}

	b, err := s.budgetSvc.SetBudget(ctx, user.ID, needs, wants, savings)
	if err != nil {
		if errors.Is(err, apperror.ErrInvalidBudgetPercentages) {
			s.reply(ctx, user, i18n.T(user.Language, i18n.KeyBudgetSetInvalid))
			return
		}
		logger.Error(logger.SvcBudgetEvalFailed, fmt.Sprintf("set budget for user %d", user.ID), err)
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyBudgetSetFailed))
		return
	}

	s.reply(ctx, user, i18n.T(user.Language, i18n.KeyBudgetSetSuccess, b.NeedsPercent, b.WantsPercent, b.SavingsPercent))
}

func (s *Service) handleClaim(ctx context.Context, user *models.User, msg *messaging.IncomingMessage) {
	code := strings.TrimSpace(msg.CommandArgs)
	if code == "" {
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyClaimUsage))
		return
	}

	result, err := reward.Claim(ctx, s.redisClient, msg.ChatID, code, s.cfg.RewardBonusAmount, s.cfg.RewardMaxClaimsPerDay)
	if err != nil {
		logger.Error(logger.RedisOpFailed, fmt.Sprintf("claim reward for chat %d", msg.ChatID), err)
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyClaimInvalidCode))
		return
	}

	switch result {
	case reward.ClaimGranted:
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyClaimSuccess, s.cfg.RewardBonusAmount))
	case reward.ClaimLimitReached:
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyClaimLimitReached))
	default:
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyClaimInvalidCode))
	}
}

func (s *Service) handleDashboard(ctx context.Context, user *models.User, msg *messaging.IncomingMessage) {
	if s.cfg.DashboardBaseURL == "" {
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyDashboardUnavailable))
		return
	}

	token, err := auth.IssueLoginToken(ctx, s.redisClient, user.ID)
	if err != nil {
		logger.Error(logger.RedisOpFailed, fmt.Sprintf("issue dashboard login token for user %d", user.ID), err)
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyDashboardUnavailable))
		return
	}

	link := fmt.Sprintf("%s/auth/verify?token=%s", strings.TrimRight(s.cfg.DashboardBaseURL, "/"), token)
	s.reply(ctx, user, i18n.T(user.Language, i18n.KeyDashboardLink, link))
}

// enforceInputLength rejects a text message longer than MaxInputChars
// before any AI call is made, replying (and returning true) if it's over
// the limit. Voice messages have no text to check and always pass. A
// non-positive MaxInputChars means the check is disabled (matching the
// ProTierDailyQuota: -1 "unlimited" convention elsewhere in this config) —
// treating an unset zero value as "reject everything" would be a silent
// footgun. This used to be an HTTP-middleware-level rejection with no reply
// at all — moved here so the user actually finds out why nothing happened,
// the same fix already applied to daily-quota enforcement.
func (s *Service) enforceInputLength(ctx context.Context, user *models.User, msg *messaging.IncomingMessage) bool {
	if msg.IsVoice || s.cfg.MaxInputChars <= 0 || len(msg.Text) <= s.cfg.MaxInputChars {
		return false
	}
	s.reply(ctx, user, i18n.T(user.Language, i18n.KeyInputTooLong, s.cfg.MaxInputChars))
	return true
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
				s.reply(ctx, user, i18n.T(user.Language, i18n.KeyQuotaExceededWithReward,
					s.cfg.FreeTierDailyQuota, s.cfg.RewardBonusAmount, s.cfg.RewardAffiliateURL, code))
				return true
			} else {
				logger.Error(logger.RedisOpFailed, "issue reward code", iErr)
			}
		}
	}

	s.reply(ctx, user, i18n.T(user.Language, i18n.KeyQuotaExceededNoReward, s.cfg.FreeTierDailyQuota))
	return true
}

func (s *Service) handleTransactionInput(ctx context.Context, user *models.User, msg *messaging.IncomingMessage) {
	if s.enforceInputLength(ctx, user, msg) {
		return
	}

	if s.enforceQuota(ctx, user, msg) {
		return
	}

	var (
		parsedList  []*ai.ParsedTransaction
		usageMeta   *ai.UsageMetadata
		err         error
		source      models.TransactionSource
		raw         string
		requestType string
	)

	preferredLanguage := string(user.Language)

	// history lets a follow-up message ("totalnya 150rb") be understood in
	// context of what was just said ("abis makan di restoran mahal tadi")
	// instead of parsed as if it were the first message ever sent — see
	// conversation.Service.BuildContext. HandleUpdate already logged this
	// turn's own user message before dispatching here, so it's sitting as
	// the newest row already; drop it from Recent so it isn't sent to the
	// AI twice (once as history, once as the actual message being parsed).
	var history ai.ConversationContext
	conv, convErr := s.convSvc.GetOrCreate(ctx, user.ID)
	if convErr != nil {
		logger.Error(logger.SvcConversationFailed, fmt.Sprintf("get conversation for user %d", user.ID), convErr)
	} else if history, convErr = s.convSvc.BuildContext(ctx, conv); convErr != nil {
		logger.Error(logger.SvcConversationFailed, fmt.Sprintf("build conversation context for user %d", user.ID), convErr)
		history = ai.ConversationContext{}
	} else if len(history.Recent) > 0 {
		history.Recent = history.Recent[:len(history.Recent)-1]
	}

	if msg.IsVoice {
		source = models.SourceVoice
		requestType = "audio_transcribe"
		data, mimeType, dlErr := s.messenger.DownloadFile(ctx, msg.VoiceFileID)
		if dlErr != nil {
			logger.Error(logger.TelegramDownloadFailed, fmt.Sprintf("download voice file for user %d", user.ID), dlErr)
			s.reply(ctx, user, i18n.T(user.Language, i18n.KeyVoiceDownloadFailed))
			return
		}
		raw = "[voice note]"
		parsedList, usageMeta, err = s.aiClient.TranscribeAndParseAudio(ctx, data, mimeType, user.DefaultCurrency, preferredLanguage, history)
	} else {
		source = models.SourceText
		requestType = "text_parse"
		raw = msg.Text
		if chained, ok := s.aiClient.(ai.ChainedCorrector); ok {
			// See internal/ai/chain.go: a correction/deletion that doesn't
			// restate every field, or that targets something other than the
			// single most recent transaction/recurring schedule, can't be
			// safely resolved from the message alone, so a
			// chained-tool-calling provider can call get_recent_transactions
			// or get_recurring_transactions first — backed here by the real
			// transaction.Service.RecentTransactions / recurring.Service.ListActive
			// — before deciding what to act on. Falls back to
			// ParseTransactionText below for providers that don't implement
			// this optional capability.
			parsedList, usageMeta, _, err = chained.ParseCorrectionChained(ctx, msg.Text, user.DefaultCurrency, preferredLanguage, s.recentTransactionsLookup(user.ID), s.recurringTransactionsLookup(user.ID), history)
		} else {
			parsedList, usageMeta, err = s.aiClient.ParseTransactionText(ctx, msg.Text, user.DefaultCurrency, preferredLanguage, history)
		}
	}

	if err != nil {
		if errors.Is(err, apperror.ErrProviderAudioUnsupported) {
			// Expected capability gap, not a failure — no error log.
			s.reply(ctx, user, i18n.T(user.Language, i18n.KeyVoiceUnsupported, s.aiClient.ProviderName()))
			return
		}
		logger.Error(logger.AIParseFailed, fmt.Sprintf("AI parse for user %d", user.ID), err)
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyAITrouble, logger.AIParseFailed))
		return
	}
	if len(parsedList) == 0 {
		// Every provider guarantees at least one result on a nil error (see
		// ai.AIClient's doc comment) — this is a defensive fallback, not an
		// expected path.
		logger.Error(logger.AIParseFailed, fmt.Sprintf("AI parse for user %d returned zero results", user.ID), nil)
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyAITrouble, logger.AIParseFailed))
		return
	}

	// A successful AI call counts against today's quota regardless of tier
	// (Redis is only ever checked for free-tier users, but recording it for
	// everyone gives complete audit data) and regardless of whether the
	// result was ambiguous — the API cost was already incurred either way,
	// once, even when the result batches several transactions.
	if qErr := quota.Increment(ctx, s.redisClient, msg.ChatID); qErr != nil {
		logger.Error(logger.RedisOpFailed, fmt.Sprintf("increment quota for chat %d", msg.ChatID), qErr)
	}
	go s.usageSvc.RecordAICall(context.Background(), user.ID, models.AIProvider(s.aiClient.ProviderName()), requestType, usageMeta)

	// Every intent other than "record" is guaranteed singular EXCEPT for
	// one deliberate pair: there's no dedicated "update" tool for a
	// recurring transaction/bill reminder (see
	// internal/ai/prompt/tool_schema.go's exception carved into the
	// otherwise-strict "at most once per message" rule), so an update
	// message ("ubah netflix jadi 200rb") produces BOTH an
	// IntentRecurringDelete and an IntentRecurringCreate result together.
	// That combination must be checked before falling back to inspecting
	// only parsedList[0] for every other (genuinely singular) intent —
	// otherwise the delete would be applied and the create silently
	// dropped, canceling the old schedule with nothing to replace it.
	if deleted, created, ok := recurringUpdatePair(parsedList); ok {
		s.handleRecurringUpdate(ctx, user, deleted, created)
		return
	}

	first := parsedList[0]

	if first.IsAmbiguous {
		s.reply(ctx, user, first.ClarificationQuestion)
		return
	}

	if first.Intent == ai.IntentQuery {
		s.handleSpendingQuery(ctx, user, msg, first)
		return
	}

	if first.Intent == ai.IntentBalance {
		s.handleBalanceQuery(ctx, user, msg)
		return
	}

	if first.Intent == ai.IntentCorrect {
		s.handleTransactionCorrection(ctx, user, msg, first, source, raw)
		return
	}

	if first.Intent == ai.IntentDelete {
		s.handleTransactionDeletion(ctx, user, first)
		return
	}

	if first.Intent == ai.IntentRecurringCreate {
		s.handleRecurringCreate(ctx, user, first)
		return
	}

	if first.Intent == ai.IntentSavingsGoalCreate {
		s.handleSavingsGoalCreate(ctx, user, first)
		return
	}

	if first.Intent == ai.IntentRecurringDelete {
		s.handleRecurringDelete(ctx, user, first)
		return
	}

	s.handleTransactionRecordBatch(ctx, user, msg, parsedList, source, raw)
}

// handleTransactionRecordBatch persists one or more record-intent
// transactions parsed from a single message (e.g. an itemized list: "bensin
// 300k, makan siang 300k, makan malam 100k" yields three) and replies with
// one confirmation line per transaction, followed by budget warnings
// evaluated once for the whole batch rather than once per line — the
// warning reflects the user's post-batch spending total either way, so
// repeating it per line would just be noise. A stray non-record item
// (the model violating the "only record_transaction repeats" instruction)
// is skipped rather than mis-recorded.
func (s *Service) handleTransactionRecordBatch(ctx context.Context, user *models.User, msg *messaging.IncomingMessage, parsedList []*ai.ParsedTransaction, source models.TransactionSource, raw string) {
	var (
		lines      []string
		anyExpense *models.Transaction
	)

	for _, parsed := range parsedList {
		// Empty Intent means IntentRecord (see ai.ParsedTransaction.Intent's
		// doc comment) — every provider test fixture and pre-intent
		// prompt/response relies on this, so it must be treated as record
		// here too, not just at the top-level fallthrough.
		if parsed.Intent != "" && parsed.Intent != ai.IntentRecord {
			logger.Error(logger.SvcCreateTxFailed, fmt.Sprintf("unexpected intent %q in a record batch for user %d, skipping", parsed.Intent, user.ID), nil)
			continue
		}

		createdTx, err := s.txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, parsed, source, raw)
		if err != nil {
			logger.Error(logger.SvcCreateTxFailed, fmt.Sprintf("create transaction for user %d", user.ID), err)
			continue
		}

		lines = append(lines, i18n.T(user.Language, i18n.KeyTransactionRecorded,
			createdTx.Type, i18n.FormatMoney(user.Language, createdTx.OriginalAmount, createdTx.OriginalCurrency), parsed.CategoryName))
		if anyExpense == nil && createdTx.Type == models.TxExpense {
			anyExpense = createdTx
		}
	}

	if len(lines) == 0 {
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyTransactionSaveFailed))
		return
	}

	reply := strings.Join(lines, "\n")
	if anyExpense != nil {
		reply = s.appendBudgetWarnings(ctx, user, anyExpense, reply)
	}

	s.reply(ctx, user, reply)
}

// handleTransactionCorrection replaces the user's most recent transaction
// with the corrected one described by parsed (ai.IntentCorrect — e.g. "eh
// maaf, harusnya 1jt deh untuk reksa dana kemarin, typo") instead of adding
// a second transaction alongside the wrong one. See
// transaction.Service.CorrectLast for the create-before-undo safety
// ordering.
func (s *Service) handleTransactionCorrection(ctx context.Context, user *models.User, msg *messaging.IncomingMessage, parsed *ai.ParsedTransaction, source models.TransactionSource, raw string) {
	createdTx, undoneTx, err := s.txSvc.CorrectLast(ctx, user.ID, user.DefaultCurrency, parsed, source, raw)
	if createdTx == nil {
		logger.Error(logger.SvcCreateTxFailed, fmt.Sprintf("create corrected transaction for user %d", user.ID), err)
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyTransactionSaveFailed))
		return
	}
	if err != nil {
		// The correction itself was recorded (createdTx is real); only
		// reverting the old entry failed. Log it, but don't tell the user
		// their correction failed — from their view it succeeded.
		logger.Error(logger.SvcUndoFailed, fmt.Sprintf("undo previous transaction during correction for user %d", user.ID), err)
	}

	// Nothing existed to correct (e.g. this was the user's very first
	// message) — recording it fresh is still the right outcome, so reply
	// as a normal record rather than claiming a correction happened.
	replyKey := i18n.KeyTransactionRecorded
	if undoneTx != nil {
		replyKey = i18n.KeyTransactionCorrected
	}

	reply := i18n.T(user.Language, replyKey,
		createdTx.Type, i18n.FormatMoney(user.Language, createdTx.OriginalAmount, createdTx.OriginalCurrency), parsed.CategoryName)
	reply = s.appendBudgetWarnings(ctx, user, createdTx, reply)

	s.reply(ctx, user, reply)
}

// handleTransactionDeletion removes one of the user's existing transactions
// described by parsed (ai.IntentDelete — e.g. "hapus transaksi makan siang
// tadi") instead of correcting or adding one. See
// transaction.Service.DeleteTransaction for how parsed.TargetTransactionID
// (identified via get_recent_transactions by a chained-tool-calling
// AIClient, same mechanism as a correction's target) picks which
// transaction, falling back to the most recent one when unset. Reuses the
// exact same reply text as /undo (KeyUndoNone/KeyUndoFailed/KeyUndoSuccess)
// since deleting a specific transaction by name and undoing the last one
// are the same user-visible outcome, just reached a different way.
func (s *Service) handleTransactionDeletion(ctx context.Context, user *models.User, parsed *ai.ParsedTransaction) {
	deletedTx, err := s.txSvc.DeleteTransaction(ctx, user.ID, parsed.TargetTransactionID)
	if err != nil {
		if errors.Is(err, apperror.ErrNoUndoableTransaction) {
			s.reply(ctx, user, i18n.T(user.Language, i18n.KeyUndoNone))
			return
		}
		logger.Error(logger.SvcUndoFailed, fmt.Sprintf("delete transaction for user %d", user.ID), err)
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyUndoFailed))
		return
	}
	s.reply(ctx, user, i18n.T(user.Language, i18n.KeyUndoSuccess,
		deletedTx.Type, i18n.FormatMoney(user.Language, deletedTx.OriginalAmount, deletedTx.OriginalCurrency)))
}

// handleRecurringCreate schedules a new recurring transaction or bill
// reminder described by parsed (ai.IntentRecurringCreate — e.g. "netflix
// 150rb setiap tanggal 5", or "ingetin bayar listrik 500rb tiap tanggal 5"
// with parsed.ReminderOnly set). See recurring.Service.Create for the
// schedule math (never fires on the creation day itself) and
// models.RecurringTransaction.ReminderOnly for the auto-record vs.
// notify-only distinction. Nothing is recorded as a Transaction right now
// either way — only RunRecurringTransactions (an internal/scheduler job,
// see cmd/server/main.go) ever acts on one, once the schedule actually
// comes due.
func (s *Service) handleRecurringCreate(ctx context.Context, user *models.User, parsed *ai.ParsedTransaction) {
	rt, err := s.recurSvc.Create(ctx, user.ID, parsed)
	if err != nil {
		logger.Error(logger.SvcRecurringFailed, fmt.Sprintf("create recurring transaction for user %d", user.ID), err)
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyRecurringCreateFailed))
		return
	}

	replyKey := i18n.KeyRecurringCreated
	if rt.ReminderOnly {
		replyKey = i18n.KeyBillReminderCreated
	}
	s.reply(ctx, user, i18n.T(user.Language, replyKey,
		rt.Type, i18n.FormatMoney(user.Language, rt.Amount, rt.Currency), rt.CategoryName, rt.DayOfMonth, rt.NextRunAt.Format("2006-01-02")))
}

// handleSavingsGoalCreate creates (or replaces — only one goal is active at
// a time, see models.SavingsGoal's doc comment) a savings goal described by
// parsed (ai.IntentSavingsGoalCreate — e.g. "aku mau nabung 10 juta buat
// laptop sampai desember").
func (s *Service) handleSavingsGoalCreate(ctx context.Context, user *models.User, parsed *ai.ParsedTransaction) {
	deadline := parseDeadlineHint(parsed.GoalDeadlineHint)

	goal, err := s.goalSvc.Create(ctx, user.ID, parsed.GoalName, parsed.Amount, parsed.Currency, deadline)
	if err != nil {
		logger.Error(logger.SvcGoalFailed, fmt.Sprintf("create savings goal for user %d", user.ID), err)
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyGoalCreateFailed))
		return
	}

	deadlineClause := ""
	if goal.Deadline != nil {
		deadlineClause = " by " + goal.Deadline.Format("2006-01-02")
	}
	s.reply(ctx, user, i18n.T(user.Language, i18n.KeyGoalCreated,
		i18n.FormatMoney(user.Language, goal.TargetAmount, goal.Currency), goal.Name, deadlineClause))
}

// parseDeadlineHint interprets an IntentSavingsGoalCreate deadline hint —
// same ISO 8601 (YYYY-MM-DD) convention as a transaction's OccurredAtHint —
// into a concrete UTC midnight, or nil if empty/unparseable. Unlike
// resolveOccurredAt (transaction.Service), there's no "assume today"
// fallback: a goal legitimately has no deadline at all, so an empty/bad
// hint just means "no deadline" rather than defaulting to some date.
func parseDeadlineHint(hint string) *time.Time {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return nil
	}
	parsed, err := time.Parse("2006-01-02", hint)
	if err != nil {
		return nil
	}
	parsed = time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, time.UTC)
	return &parsed
}

// handleGoal is /goal's dispatcher: no argument shows progress on the
// user's active goal (or KeyGoalNone if there isn't one); "cancel" cancels
// it. A plain command for viewing/canceling, mirroring /recurring and
// /budget — creation alone goes through natural language (see
// handleSavingsGoalCreate) since a name + amount + optional deadline is far
// more naturally phrased as a sentence than command arguments, unlike
// /budget's exact numeric percentages.
func (s *Service) handleGoal(ctx context.Context, user *models.User, msg *messaging.IncomingMessage) {
	args := strings.Fields(msg.CommandArgs)
	if len(args) == 1 && strings.EqualFold(args[0], "cancel") {
		s.handleGoalCancel(ctx, user)
		return
	}
	if len(args) > 0 {
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyGoalUsage))
		return
	}

	progress, err := s.goalSvc.Progress(ctx, user.ID)
	if err != nil {
		if errors.Is(err, apperror.ErrNoActiveSavingsGoal) {
			s.reply(ctx, user, i18n.T(user.Language, i18n.KeyGoalNone))
			return
		}
		logger.Error(logger.SvcGoalFailed, fmt.Sprintf("get savings goal progress for user %d", user.ID), err)
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyGoalCreateFailed))
		return
	}

	lines := []string{i18n.T(user.Language, i18n.KeyGoalProgress,
		progress.Goal.Name,
		i18n.FormatMoney(user.Language, progress.Saved, progress.Goal.Currency),
		i18n.FormatMoney(user.Language, progress.Goal.TargetAmount, progress.Goal.Currency),
		progress.Saved/progress.Goal.TargetAmount*100)}

	if progress.Saved >= progress.Goal.TargetAmount {
		lines = append(lines, i18n.T(user.Language, i18n.KeyGoalReached))
	} else {
		lines = append(lines, i18n.T(user.Language, i18n.KeyGoalProgressToGo,
			i18n.FormatMoney(user.Language, progress.Goal.TargetAmount-progress.Saved, progress.Goal.Currency)))
	}
	if progress.Goal.Deadline != nil {
		lines = append(lines, i18n.T(user.Language, i18n.KeyGoalProgressDeadline, progress.Goal.Deadline.Format("2006-01-02")))
	}

	s.reply(ctx, user, strings.Join(lines, "\n"))
}

// handleGoalCancel backs "/goal cancel".
func (s *Service) handleGoalCancel(ctx context.Context, user *models.User) {
	goal, err := s.goalSvc.Cancel(ctx, user.ID)
	if err != nil {
		if errors.Is(err, apperror.ErrNoActiveSavingsGoal) {
			s.reply(ctx, user, i18n.T(user.Language, i18n.KeyGoalNone))
			return
		}
		logger.Error(logger.SvcGoalFailed, fmt.Sprintf("cancel savings goal for user %d", user.ID), err)
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyGoalCreateFailed))
		return
	}
	s.reply(ctx, user, i18n.T(user.Language, i18n.KeyGoalCancelSuccess, goal.Name))
}

// appendBudgetWarnings appends any Needs/Wants budget warnings triggered by
// tx to reply — shared by the normal record-success reply and the
// correction-success reply. Only expenses can trip a Needs/Wants guardrail,
// so non-expense types are a no-op.
func (s *Service) appendBudgetWarnings(ctx context.Context, user *models.User, tx *models.Transaction, reply string) string {
	if tx.Type != models.TxExpense {
		return reply
	}
	status, err := s.budgetSvc.EvaluateBudget(ctx, user.ID, user.Language)
	if err != nil {
		logger.Error(logger.SvcBudgetEvalFailed, fmt.Sprintf("evaluate budget for user %d", user.ID), err)
		return reply
	}
	for _, warning := range status.Warnings {
		reply += "\n⚠️ " + warning
	}
	return reply
}

// handleSpendingQuery answers a "how much have I spent" style question
// (ai.IntentQuery) via report.Service instead of recording anything — no
// Transaction row is created. Quota/usage tracking already happened in the
// caller before this branch runs, same as for a recorded transaction, since
// the AI call itself was the cost regardless of which intent it returned.
//
// Only expense totals are reported ("how much did I spend"), not income/
// investment/transfer — matches the question this intent was built to
// answer; a broader "how much did I earn" query is out of scope for now.
//
// A plain total query (no category/bucket filter) is followed by a
// per-category breakdown (TopCategories, capped at 5) rather than just the
// bare number — "habis buat apa saja?" (spent on what?) is effectively the
// same question as "how much did I spend", just expecting more detail in
// the answer, and the breakdown query already existed for other features.
func (s *Service) handleSpendingQuery(ctx context.Context, user *models.User, msg *messaging.IncomingMessage, parsed *ai.ParsedTransaction) {
	periodLabel := i18n.PeriodLabel(user.Language, parsed.QueryPeriod)

	if bucket, ok := resolveQueryBucket(parsed); ok {
		s.handleBucketSpendingQuery(ctx, user, parsed.QueryPeriod, periodLabel, bucket)
		return
	}

	if parsed.QueryCategory != "" {
		total, matched, err := s.reportSvc.CategoryTotal(ctx, user.ID, parsed.QueryPeriod, parsed.QueryCategory)
		if err != nil {
			logger.Error(logger.SvcReportQueryFailed, fmt.Sprintf("category spending query for user %d", user.ID), err)
			s.reply(ctx, user, i18n.T(user.Language, i18n.KeyAITrouble, logger.SvcReportQueryFailed))
			return
		}
		if !matched {
			s.reply(ctx, user, i18n.T(user.Language, i18n.KeyQueryCategoryNotFound, parsed.QueryCategory, periodLabel))
			return
		}
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyQuerySpendingByCategory, i18n.FormatMoney(user.Language, total, user.DefaultCurrency), parsed.QueryCategory, periodLabel))
		return
	}

	totals, _, _, err := s.reportSvc.Summary(ctx, user.ID, parsed.QueryPeriod)
	if err != nil {
		logger.Error(logger.SvcReportQueryFailed, fmt.Sprintf("summary spending query for user %d", user.ID), err)
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyAITrouble, logger.SvcReportQueryFailed))
		return
	}
	expenseTotal := totals[string(models.TxExpense)]
	headline := i18n.T(user.Language, i18n.KeyQuerySpendingTotal, i18n.FormatMoney(user.Language, expenseTotal, user.DefaultCurrency), periodLabel)
	if expenseTotal <= 0 {
		// Nothing to break down — avoid a division by zero below and a
		// breakdown list that would just be empty anyway.
		s.reply(ctx, user, headline)
		return
	}

	topCategories, err := s.reportSvc.TopCategories(ctx, user.ID, parsed.QueryPeriod)
	if err != nil {
		// The breakdown is a nice-to-have on top of the total, not the
		// question's core answer — log and still send the total rather than
		// failing the whole reply over it.
		logger.Error(logger.SvcReportQueryFailed, fmt.Sprintf("top-categories breakdown for user %d", user.ID), err)
		s.reply(ctx, user, headline)
		return
	}

	lines := []string{headline}
	for i, c := range topCategories {
		lines = append(lines, i18n.T(user.Language, i18n.KeyQuerySpendingBreakdownItem,
			i+1, categoryDisplayName(user.Language, c.CategoryName), i18n.FormatMoney(user.Language, c.Total, user.DefaultCurrency), c.Total/expenseTotal*100))
	}
	s.reply(ctx, user, strings.Join(lines, "\n"))
}

// resolveQueryBucket decides whether a query_spending parse is actually
// asking about a whole Needs/Wants/Savings bucket rather than one specific
// category. It checks ParsedTransaction.QueryBucket first (the field meant
// for this), then falls back to treating QueryCategory as a bucket name if
// it happens to match one — a bucket word ("wants", "keinginan") sometimes
// lands in QueryCategory instead of the newer QueryBucket field despite the
// prompt's guidance, and CategoryTotal would never match a real category
// named "wants" anyway, so this fallback costs nothing and avoids a
// needless "category not found" reply for a perfectly answerable question.
func resolveQueryBucket(parsed *ai.ParsedTransaction) (models.BudgetBucket, bool) {
	candidates := []string{parsed.QueryBucket, parsed.QueryCategory}
	for _, raw := range candidates {
		switch models.BudgetBucket(strings.ToLower(strings.TrimSpace(raw))) {
		case models.BucketNeeds:
			return models.BucketNeeds, true
		case models.BucketWants:
			return models.BucketWants, true
		case models.BucketSavings:
			return models.BucketSavings, true
		}
	}
	return "", false
}

// categoryDisplayName covers a real gap, not just cosmetics: category-total
// query rows can come back with an empty CategoryName because
// transaction.Service.CreateAdjustment (the /sync "blind spot" adjustment
// entry) and any other transaction created with no category name at all
// leave Transaction.CategoryID nil, which a LEFT JOIN to categories then
// resolves to a NULL/empty name — rendering as a blank, unexplained line
// like "1. : Rp3.432.000,00 (69% dari total)" instead of saying what that
// spend actually is. Every other category name is shown as-is, in English,
// regardless of language (see TopCategories' own doc comment on that
// convention) — this only substitutes a translated label for the empty case.
func categoryDisplayName(lang models.Language, name string) string {
	if name == "" {
		return i18n.T(lang, i18n.KeyUncategorizedLabel)
	}
	return name
}

// bucketDisplayName renders a BudgetBucket the same way every other
// budget-related message already does — Needs/Wants/Savings kept in English
// even in the Indonesian catalog (see KeyBudgetShow's own ID template) since
// these are the bot's own fixed 50/30/20 vocabulary, not free-text content.
func bucketDisplayName(bucket models.BudgetBucket) string {
	switch bucket {
	case models.BucketNeeds:
		return "Needs"
	case models.BucketWants:
		return "Wants"
	case models.BucketSavings:
		return "Savings"
	default:
		return string(bucket)
	}
}

// handleBucketSpendingQuery answers a "how much did I spend on Wants" style
// question (or a short confirmation resolving to one — see
// resolveQueryBucket) with a per-category breakdown within that bucket, the
// same shape RunCategoryInsights already uses for the proactive monthly
// recap, rather than a single opaque total.
func (s *Service) handleBucketSpendingQuery(ctx context.Context, user *models.User, period, periodLabel string, bucket models.BudgetBucket) {
	breakdown, err := s.reportSvc.BucketSpending(ctx, user.ID, period, bucket)
	if err != nil {
		logger.Error(logger.SvcReportQueryFailed, fmt.Sprintf("bucket spending query for user %d", user.ID), err)
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyAITrouble, logger.SvcReportQueryFailed))
		return
	}

	label := bucketDisplayName(bucket)
	if breakdown.Total <= 0 {
		s.reply(ctx, user, i18n.T(user.Language, i18n.KeyQuerySpendingBucketNone, label, periodLabel))
		return
	}

	lines := []string{i18n.T(user.Language, i18n.KeyQuerySpendingByBucket,
		i18n.FormatMoney(user.Language, breakdown.Total, user.DefaultCurrency), label, periodLabel)}
	for i, c := range breakdown.Categories {
		percent := c.Total / breakdown.Total * 100
		lines = append(lines, i18n.T(user.Language, i18n.KeyQuerySpendingBreakdownItem,
			i+1, categoryDisplayName(user.Language, c.CategoryName), i18n.FormatMoney(user.Language, c.Total, user.DefaultCurrency), percent))
	}
	s.reply(ctx, user, strings.Join(lines, "\n"))
}

// recentTransactionsLookupLimit bounds how many of the user's recent
// transactions get_recent_transactions hands the model as correction
// candidates — enough to cover a batch of several transactions recorded
// from one message plus a few messages before it, without unboundedly
// growing the request every time the tool fires.
const recentTransactionsLookupLimit = 10

// recentTransactionsLookup backs a chained-tool-calling AIClient's
// get_recent_transactions tool (see ai.ChainedCorrector) with the real
// transaction.Service.RecentTransactions, mapped onto the
// provider-agnostic ai.RecentTransaction shape — including the real
// database ID, so the model can echo back exactly which one a correction
// message targets instead of the caller always assuming the most recent.
// An empty slice (not an error) is a valid, expected answer (e.g. this is
// the user's very first message).
func (s *Service) recentTransactionsLookup(userID uint) ai.RecentTransactionsLookup {
	return func(ctx context.Context) ([]*ai.RecentTransaction, error) {
		txs, err := s.txSvc.RecentTransactions(ctx, userID, recentTransactionsLookupLimit)
		if err != nil {
			return nil, err
		}

		results := make([]*ai.RecentTransaction, 0, len(txs))
		for _, tx := range txs {
			categoryName, bucket := "", ""
			if tx.Category != nil {
				categoryName = tx.Category.Name
				bucket = string(tx.Category.Bucket)
			}
			if tx.Bucket != nil {
				bucket = string(*tx.Bucket)
			}

			results = append(results, &ai.RecentTransaction{
				ID:           tx.ID,
				Type:         string(tx.Type),
				Amount:       tx.OriginalAmount,
				Currency:     tx.OriginalCurrency,
				CategoryName: categoryName,
				Bucket:       bucket,
				Description:  tx.Description,
				OccurredAt:   tx.OccurredAt.Format("2006-01-02"),
			})
		}
		return results, nil
	}
}

// recurringTransactionsLookup backs a chained-tool-calling AIClient's
// get_recurring_transactions tool (see ai.ChainedCorrector) with the real
// recurring.Service.ListActive, mapped onto the provider-agnostic
// ai.RecurringCandidate shape — including the real database ID, so the
// model can echo back exactly which recurring transaction or bill reminder
// a cancellation message targets.
func (s *Service) recurringTransactionsLookup(userID uint) ai.RecurringTransactionsLookup {
	return func(ctx context.Context) ([]*ai.RecurringCandidate, error) {
		rows, err := s.recurSvc.ListActive(ctx, userID)
		if err != nil {
			return nil, err
		}

		results := make([]*ai.RecurringCandidate, 0, len(rows))
		for _, rt := range rows {
			results = append(results, &ai.RecurringCandidate{
				ID:           rt.ID,
				Type:         string(rt.Type),
				Amount:       rt.Amount,
				Currency:     rt.Currency,
				CategoryName: rt.CategoryName,
				Description:  rt.Description,
				DayOfMonth:   rt.DayOfMonth,
				ReminderOnly: rt.ReminderOnly,
			})
		}
		return results, nil
	}
}

// reply sends text to user and logs it as the assistant's side of the
// conversation, then opportunistically folds older messages into the
// conversation's rolling summary (see conversation.Service.MaybeSummarize
// — a no-op unless the threshold is actually crossed). Both the logging
// and the summarize check happen for every reply uniformly, commands
// included, since "how many raw messages have accumulated" doesn't care
// what triggered any individual one. Summarization runs in the background
// (its own AI call) so it never adds latency to the user-visible reply.
func (s *Service) reply(ctx context.Context, user *models.User, text string) {
	if err := s.messenger.SendMessage(ctx, user.TelegramChatID, text); err != nil {
		logger.Error(logger.TelegramSendFailed, fmt.Sprintf("send reply to chat %d", user.TelegramChatID), err)
	}

	conv, err := s.convSvc.GetOrCreate(ctx, user.ID)
	if err != nil {
		logger.Error(logger.SvcConversationFailed, fmt.Sprintf("get conversation for user %d", user.ID), err)
		return
	}
	if err := s.convSvc.AppendMessage(ctx, conv.ID, models.MessageRoleAssistant, text); err != nil {
		logger.Error(logger.SvcConversationFailed, fmt.Sprintf("log assistant message for user %d", user.ID), err)
	}

	go func() {
		if err := s.convSvc.MaybeSummarize(context.Background(), conv, string(user.Language)); err != nil {
			logger.Error(logger.SvcConversationFailed, fmt.Sprintf("summarize conversation for user %d", user.ID), err)
		}
	}()
}

// rolloverNudgeTTL bounds how long a "this user was already notified" marker
// lives — long enough to cover the whole rest of the calendar month it was
// set in regardless of how often the scheduler ticks, short enough to never
// carry over into the next month's own (differently-keyed) check. 32 days
// covers every possible month length with margin.
const rolloverNudgeTTL = 32 * 24 * time.Hour

// RunBudgetRolloverNudges messages every active user with
// budget.Service.SuggestRollover's end-of-month surplus suggestion,
// skipping anyone with no surplus (an empty string, not an error) or
// already notified this calendar month — the idempotency guard (keyed by
// user + "YYYY-MM") is what makes it safe to call this on every scheduler
// tick (see internal/scheduler) rather than needing the scheduler itself to
// know about calendar boundaries. Intended to be registered as a
// scheduler.Job's Run, not called from any user-facing path. One user's
// failure (a bad lookup, a Redis hiccup) is logged and skipped rather than
// aborting the whole run — every other user must still get checked.
func (s *Service) RunBudgetRolloverNudges(ctx context.Context) error {
	var users []models.User
	if err := s.db.WithContext(ctx).Where("is_active = ?", true).Find(&users).Error; err != nil {
		return fmt.Errorf("list active users for rollover nudge: %w", err)
	}

	namespace := "budget-rollover:" + time.Now().UTC().Format("2006-01")
	for _, user := range users {
		isNew, err := idempotency.MarkSeen(ctx, s.redisClient, namespace, strconv.FormatUint(uint64(user.ID), 10), rolloverNudgeTTL)
		if err != nil {
			logger.Error(logger.RedisOpFailed, fmt.Sprintf("rollover nudge idempotency check for user %d, skipping this tick", user.ID), err)
			continue
		}
		if !isNew {
			// Already notified this calendar month.
			continue
		}

		msg, err := s.budgetSvc.SuggestRollover(ctx, user.ID, user.Language)
		if err != nil {
			logger.Error(logger.SvcRolloverNudgeFailed, fmt.Sprintf("suggest rollover for user %d", user.ID), err)
			continue
		}
		if msg == "" {
			// No surplus to suggest rolling over — not an error, just
			// nothing to say this month.
			continue
		}

		s.reply(ctx, &user, msg)
	}
	return nil
}

// RunRecurringTransactions acts on every recurring transaction / bill
// reminder whose schedule has come due (see recurring.Service.Due).
// ReminderOnly rows (see models.RecurringTransaction) only get a
// notification — nothing is recorded, since the user wants to log the
// actual payment themselves. Every other row posts as a real Transaction,
// reusing transaction.Service.CreateFromParsed unchanged by reconstructing
// an ai.ParsedTransaction from the stored fields, so category resolution
// and FX conversion behave identically to a normal message-triggered
// record. Intended to be registered as a scheduler.Job's Run, not called
// from any user-facing path. One row's failure (a bad category, a DB
// hiccup) is logged and skipped rather than aborting the whole run — every
// other due one must still get processed.
func (s *Service) RunRecurringTransactions(ctx context.Context) error {
	due, err := s.recurSvc.Due(ctx, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("list due recurring transactions: %w", err)
	}

	for _, rt := range due {
		if rt.ReminderOnly {
			if err := s.recurSvc.Advance(ctx, rt); err != nil {
				logger.Error(logger.SvcRecurringFailed, fmt.Sprintf("advance bill reminder %d after notifying", rt.ID), err)
			}
			reply := i18n.T(rt.User.Language, i18n.KeyBillReminderDue,
				rt.Type, i18n.FormatMoney(rt.User.Language, rt.Amount, rt.Currency), rt.CategoryName)
			s.reply(ctx, &rt.User, reply)
			continue
		}

		parsed := &ai.ParsedTransaction{
			Intent:       ai.IntentRecord,
			Type:         string(rt.Type),
			Amount:       rt.Amount,
			Currency:     rt.Currency,
			CategoryName: rt.CategoryName,
			Description:  rt.Description,
		}
		if rt.Bucket != nil {
			parsed.Bucket = string(*rt.Bucket)
		}

		createdTx, err := s.txSvc.CreateFromParsed(ctx, rt.UserID, rt.User.DefaultCurrency, parsed, models.SourceRecurring, rt.Description)
		if err != nil {
			logger.Error(logger.SvcCreateTxFailed, fmt.Sprintf("post recurring transaction %d for user %d", rt.ID, rt.UserID), err)
			continue
		}

		// The transaction is real regardless of what happens next — never
		// skip advancing the schedule or notifying the user just because
		// one of those two steps might also fail; log loudly instead, since
		// a stuck NextRunAt means this same row would fire again next tick.
		if err := s.recurSvc.Advance(ctx, rt); err != nil {
			logger.Error(logger.SvcRecurringFailed, fmt.Sprintf("advance recurring transaction %d after posting", rt.ID), err)
		}

		reply := i18n.T(rt.User.Language, i18n.KeyRecurringPosted,
			createdTx.Type, i18n.FormatMoney(rt.User.Language, createdTx.OriginalAmount, createdTx.OriginalCurrency), rt.CategoryName)
		reply = s.appendBudgetWarnings(ctx, &rt.User, createdTx, reply)
		s.reply(ctx, &rt.User, reply)
	}
	return nil
}

// categoryInsightTTL mirrors rolloverNudgeTTL's reasoning exactly — long
// enough to cover the rest of the calendar month it was set in regardless
// of how often the scheduler ticks, short enough to never bleed into the
// next month's own (differently-keyed) check.
const categoryInsightTTL = 32 * 24 * time.Hour

// RunCategoryInsights messages every active user with an end-of-month
// breakdown of last month's spending by category (see
// report.Service.MonthlyCategoryInsight), highlighting whichever category
// grew the most since the month before, plus a short AI-generated
// suggestion for what to do about it. Guarded by the same per-user,
// per-calendar-month idempotency pattern as RunBudgetRolloverNudges, so
// it's safe to call on every scheduler tick. The AI suggestion is
// best-effort: the deterministic header/list/warning lines need no AI call
// at all, so if SuggestSavings fails, the recap still sends without that
// one extra line rather than being dropped entirely over it. Intended to
// be registered as a scheduler.Job's Run, not called from any user-facing
// path. One user's failure is logged and skipped rather than aborting the
// whole run — every other user must still get checked.
func (s *Service) RunCategoryInsights(ctx context.Context) error {
	var users []models.User
	if err := s.db.WithContext(ctx).Where("is_active = ?", true).Find(&users).Error; err != nil {
		return fmt.Errorf("list active users for category insights: %w", err)
	}

	namespace := "category-insight:" + time.Now().UTC().Format("2006-01")
	for _, user := range users {
		isNew, err := idempotency.MarkSeen(ctx, s.redisClient, namespace, strconv.FormatUint(uint64(user.ID), 10), categoryInsightTTL)
		if err != nil {
			logger.Error(logger.RedisOpFailed, fmt.Sprintf("category insight idempotency check for user %d, skipping this tick", user.ID), err)
			continue
		}
		if !isNew {
			// Already notified this calendar month.
			continue
		}

		insight, err := s.reportSvc.MonthlyCategoryInsight(ctx, user.ID)
		if err != nil {
			logger.Error(logger.SvcCategoryInsightFailed, fmt.Sprintf("compute category insight for user %d", user.ID), err)
			continue
		}
		if insight == nil {
			// No expenses recorded at all last month — nothing to report.
			continue
		}

		lines := []string{i18n.T(user.Language, i18n.KeyCategoryInsightHeader,
			i18n.FormatMoney(user.Language, insight.TotalExpense, user.DefaultCurrency))}
		for i, c := range insight.TopCategories {
			var percent float64
			if insight.TotalExpense > 0 {
				percent = c.Total / insight.TotalExpense * 100
			}
			lines = append(lines, i18n.T(user.Language, i18n.KeyCategoryInsightItem,
				i+1, categoryDisplayName(user.Language, c.CategoryName), i18n.FormatMoney(user.Language, c.Total, user.DefaultCurrency), percent))
		}
		if insight.IncreasedCategory != "" {
			lines = append(lines, i18n.T(user.Language, i18n.KeyCategoryInsightWarning,
				insight.IncreasedCategory,
				i18n.FormatMoney(user.Language, insight.IncreasedLastMonth, user.DefaultCurrency),
				i18n.FormatMoney(user.Language, insight.IncreasedPriorMonth, user.DefaultCurrency),
				insight.IncreasedPercent))
		}

		suggestion, usageMeta, sugErr := s.aiClient.SuggestSavings(ctx, categoryInsightSummaryForAI(insight), string(user.Language))
		if sugErr != nil {
			logger.Error(logger.AISuggestSavingsFailed, fmt.Sprintf("suggest savings for user %d", user.ID), sugErr)
		} else {
			lines = append(lines, "💡 "+suggestion)
			// System-triggered, not user-initiated — never counted against
			// the interactive daily quota (no quota.Increment call), but
			// still logged for cost visibility like every other real AI call.
			s.usageSvc.RecordAICall(ctx, user.ID, models.AIProvider(s.aiClient.ProviderName()), "savings_suggestion", usageMeta)
		}

		s.reply(ctx, &user, strings.Join(lines, "\n"))
	}
	return nil
}

// categoryInsightSummaryForAI builds the plain-language (English, same
// convention as category names being internal-only) description of insight
// fed to ai.AIClient.SuggestSavings — the model renders its actual reply in
// the user's preferred language regardless of the language this input is
// written in.
func categoryInsightSummaryForAI(insight *report.CategoryInsight) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Total expenses last month: %.2f.\n", insight.TotalExpense)
	b.WriteString("Top categories last month:\n")
	for _, c := range insight.TopCategories {
		var percent float64
		if insight.TotalExpense > 0 {
			percent = c.Total / insight.TotalExpense * 100
		}
		fmt.Fprintf(&b, "- %s: %.2f (%.0f%% of total)\n", categoryDisplayName(models.LanguageEN, c.CategoryName), c.Total, percent)
	}
	if insight.IncreasedCategory != "" {
		fmt.Fprintf(&b, "%s increased the most since the month before: %.2f last month vs %.2f the month before (up %.0f%%).\n",
			insight.IncreasedCategory, insight.IncreasedLastMonth, insight.IncreasedPriorMonth, insight.IncreasedPercent)
	}
	return b.String()
}
