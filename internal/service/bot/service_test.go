package bot_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"aura-saku/internal/ai"
	"aura-saku/internal/apperror"
	"aura-saku/internal/config"
	"aura-saku/internal/db"
	"aura-saku/internal/i18n"
	"aura-saku/internal/messaging"
	"aura-saku/internal/models"
	"aura-saku/internal/quota"
	"aura-saku/internal/service/bot"
	"aura-saku/internal/service/budget"
	"aura-saku/internal/service/conversation"
	"aura-saku/internal/service/fx"
	"aura-saku/internal/service/payment"
	"aura-saku/internal/service/reconciliation"
	"aura-saku/internal/service/recurring"
	"aura-saku/internal/service/report"
	"aura-saku/internal/service/savingsgoal"
	"aura-saku/internal/service/transaction"
	"aura-saku/internal/service/usage"
)

// mockMessenger records every reply sent, keyed by chat ID, so tests can
// assert on exactly what the user would have seen without a real Telegram
// connection.
type mockMessenger struct {
	mu           sync.Mutex
	messages     map[int64][]string
	downloadErr  error
	downloadData []byte
	downloadMime string
}

func newMockMessenger() *mockMessenger {
	return &mockMessenger{messages: map[int64][]string{}}
}

func (m *mockMessenger) ParseWebhookPayload(body []byte) (*messaging.IncomingMessage, error) {
	return nil, errors.New("not used in tests: HandleUpdate is invoked directly")
}

func (m *mockMessenger) SendMessage(ctx context.Context, chatID int64, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages[chatID] = append(m.messages[chatID], text)
	return nil
}

func (m *mockMessenger) SendTypingIndicator(ctx context.Context, chatID int64) error { return nil }

func (m *mockMessenger) DownloadFile(ctx context.Context, fileID string, fallbackMimeType string) ([]byte, string, error) {
	if m.downloadErr != nil {
		return nil, "", m.downloadErr
	}
	return m.downloadData, m.downloadMime, nil
}

func (m *mockMessenger) PlatformName() string { return "mock" }

func (m *mockMessenger) last(chatID int64) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	msgs := m.messages[chatID]
	if len(msgs) == 0 {
		return ""
	}
	return msgs[len(msgs)-1]
}

func (m *mockMessenger) count(chatID int64) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.messages[chatID])
}

// mockAIClient returns a fixed, configurable result for every call and
// counts how many times it was invoked — the key assertion for quota
// gating is that a blocked request never reaches the AI client at all.
// parseResult covers the common single-transaction case without touching
// every existing test; parseResults overrides it for the batch case (a
// message describing several transactions in one AI call).
type mockAIClient struct {
	mu           sync.Mutex
	calls        int
	parseResult  *ai.ParsedTransaction
	parseResults []*ai.ParsedTransaction
	parseErr     error
	// lastHistory captures whatever ConversationContext the most recent
	// call was given — lets a test assert bot.Service actually threaded
	// real prior turns through, not just that it compiles.
	lastHistory ai.ConversationContext

	// suggestSavingsErr, when set, makes SuggestSavings fail — used to
	// verify RunCategoryInsights still sends the deterministic part of its
	// message when the AI suggestion call errors out.
	suggestSavingsErr error
}

func (m *mockAIClient) call(history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	m.mu.Lock()
	m.calls++
	m.lastHistory = history
	m.mu.Unlock()
	if m.parseErr != nil {
		return nil, nil, m.parseErr
	}
	if len(m.parseResults) > 0 {
		return m.parseResults, &ai.UsageMetadata{Model: "mock-model"}, nil
	}
	return []*ai.ParsedTransaction{m.parseResult}, &ai.UsageMetadata{Model: "mock-model"}, nil
}

func (m *mockAIClient) getLastHistory() ai.ConversationContext {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastHistory
}

func (m *mockAIClient) ParseTransactionText(ctx context.Context, userText, defaultCurrency, preferredLanguage string, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	return m.call(history)
}

func (m *mockAIClient) TranscribeAndParseAudio(ctx context.Context, audioBytes []byte, mimeType, defaultCurrency, preferredLanguage string, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	return m.call(history)
}

func (m *mockAIClient) AnalyzeReceiptImage(ctx context.Context, imageBytes []byte, mimeType, defaultCurrency, preferredLanguage, caption string, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	return m.call(history)
}

func (m *mockAIClient) AnswerClarification(ctx context.Context, originalText, clarificationReply, defaultCurrency, preferredLanguage string, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	return m.call(history)
}

// Summarize is a trivial stand-in — no test currently asserts on summary
// content, only that MaybeSummarize's threshold logic fires (or doesn't)
// at the right message count.
func (m *mockAIClient) Summarize(ctx context.Context, priorSummary string, turns []ai.ConversationTurn, preferredLanguage string) (string, *ai.UsageMetadata, error) {
	return "mock summary", &ai.UsageMetadata{Model: "mock-model"}, nil
}

// mockSavingsSuggestion is the fixed text SuggestSavings returns on
// success — a distinctive string so a test can assert it made it into the
// final composed message.
const mockSavingsSuggestion = "mock savings suggestion: cut back on your top category"

func (m *mockAIClient) SuggestSavings(ctx context.Context, spendingSummary, preferredLanguage string) (string, *ai.UsageMetadata, error) {
	if m.suggestSavingsErr != nil {
		return "", nil, m.suggestSavingsErr
	}
	return mockSavingsSuggestion, &ai.UsageMetadata{Model: "mock-model"}, nil
}

func (m *mockAIClient) ProviderName() string { return "mock" }

func (m *mockAIClient) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// testEnv bundles everything an integration test needs against the real
// dev Postgres/Redis (see docker-compose.yml), skipped automatically if
// either is unreachable — matching the pattern already used by
// internal/service/budget and internal/service/reconciliation.
type testEnv struct {
	db        *gorm.DB
	redis     *redis.Client
	messenger *mockMessenger
	ai        *mockAIClient
}

func setupTestEnv(t *testing.T, cfg bot.Config) (*bot.Service, *testEnv) {
	t.Helper()

	appCfg, err := config.Load()
	if err != nil {
		t.Skipf("skipping integration test, config load failed: %v", err)
	}
	gormDB, err := db.NewPostgres(appCfg)
	if err != nil {
		t.Skipf("skipping integration test, postgres not reachable: %v", err)
	}
	if err := db.RunMigrations(gormDB); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	redisClient := redis.NewClient(&redis.Options{Addr: fmt.Sprintf("%s:%s", appCfg.RedisHost, appCfg.RedisPort)})
	if err := redisClient.Ping(context.Background()).Err(); err != nil {
		t.Skipf("skipping integration test, redis not reachable: %v", err)
	}

	messenger := newMockMessenger()
	aiClient := &mockAIClient{}
	txSvc := transaction.NewService(gormDB, fx.NewStaticService())
	budgetSvc := budget.NewService(gormDB)
	reconSvc := reconciliation.NewService(gormDB, txSvc)
	usageSvc := usage.NewService(gormDB)
	reportSvc := report.NewService(gormDB)
	convSvc := conversation.NewService(gormDB, aiClient, conversation.Config{RecentLimit: 10, SummarizeThreshold: 20})
	recurSvc := recurring.NewService(gormDB)
	goalSvc := savingsgoal.NewService(gormDB, budgetSvc)
	// A fixed fake key, deliberately NOT appCfg.MidtransServerKey — tests in
	// this file never exercise CreateCheckout/HandleNotification's actual
	// Midtrans network calls (see the plan's live-sandbox-verification step
	// for that), only paths that return before touching the network. Using
	// a real key here would risk this suite making live API calls the
	// moment a developer's local .env gets real sandbox credentials.
	paymentSvc := payment.NewService(gormDB, redisClient, "test-midtrans-key", false, true)

	svc := bot.NewService(gormDB, redisClient, messenger, aiClient, txSvc, budgetSvc, reconSvc, usageSvc, reportSvc, convSvc, recurSvc, goalSvc, paymentSvc, cfg)

	return svc, &testEnv{db: gormDB, redis: redisClient, messenger: messenger, ai: aiClient}
}

// newChatID returns a unique chat ID per test so parallel/sequential test
// runs never collide on the same User row or Redis quota/reward keys.
func newChatID() int64 {
	return time.Now().UnixNano()
}

func cleanupChat(t *testing.T, env *testEnv, chatID int64) {
	t.Helper()
	ctx := context.Background()

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err == nil {
		var conv models.Conversation
		if err := env.db.Where("user_id = ?", user.ID).First(&conv).Error; err == nil {
			env.db.Unscoped().Where("conversation_id = ?", conv.ID).Delete(&models.Message{})
			env.db.Unscoped().Delete(&conv)
		}
		env.db.Unscoped().Where("user_id = ?", user.ID).Delete(&models.UndoHistory{})
		env.db.Unscoped().Where("user_id = ?", user.ID).Delete(&models.Transaction{})
		env.db.Unscoped().Where("user_id = ?", user.ID).Delete(&models.RecurringTransaction{})
		env.db.Unscoped().Where("user_id = ?", user.ID).Delete(&models.SavingsGoal{})
		env.db.Unscoped().Where("user_id = ?", user.ID).Delete(&models.Budget{})
		env.db.Unscoped().Where("user_id = ?", user.ID).Delete(&models.Subscription{})
		env.db.Unscoped().Where("user_id = ?", user.ID).Delete(&models.BalanceReconciliation{})
		env.db.Unscoped().Where("user_id = ?", user.ID).Delete(&models.Account{})
		env.db.Unscoped().Where("user_id = ?", user.ID).Delete(&models.AIUsageLog{})
		env.db.Unscoped().Where("user_id = ?", user.ID).Delete(&models.QuotaUsage{})
		env.db.Unscoped().Delete(&user)
	}

	keys, _ := env.redis.Keys(ctx, fmt.Sprintf("aura:*:%d*", chatID)).Result()
	if len(keys) > 0 {
		env.redis.Del(ctx, keys...)
	}
}

func TestHandleUpdate_Start(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 3})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })

	svc.HandleUpdate(context.Background(), &messaging.IncomingMessage{ChatID: chatID, Command: "/start"})

	want := i18n.T(models.LanguageID, i18n.KeyWelcome)
	if got := env.messenger.last(chatID); got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

func TestHandleUpdate_Language(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 3})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	// Invalid arg replies with usage instructions, in the user's current language.
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/language", CommandArgs: "fr"})
	wantUsage := i18n.T(models.LanguageID, i18n.KeyLanguageUsage)
	if got := env.messenger.last(chatID); got != wantUsage {
		t.Errorf("invalid language reply = %q, want %q", got, wantUsage)
	}

	// Valid arg switches language and confirms in the new language.
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/language", CommandArgs: "en"})
	wantSet := i18n.T(models.LanguageEN, i18n.KeyLanguageSet)
	if got := env.messenger.last(chatID); got != wantSet {
		t.Errorf("set language reply = %q, want %q", got, wantSet)
	}

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if user.Language != models.LanguageEN {
		t.Errorf("user.Language = %q, want %q", user.Language, models.LanguageEN)
	}
}

func TestHandleUpdate_UndoNone(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 3})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })

	svc.HandleUpdate(context.Background(), &messaging.IncomingMessage{ChatID: chatID, Command: "/undo"})

	want := i18n.T(models.LanguageID, i18n.KeyUndoNone)
	if got := env.messenger.last(chatID); got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

func TestHandleUpdate_DashboardUnavailableWhenUnconfigured(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 3, DashboardBaseURL: ""})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })

	svc.HandleUpdate(context.Background(), &messaging.IncomingMessage{ChatID: chatID, Command: "/dashboard"})

	want := i18n.T(models.LanguageID, i18n.KeyDashboardUnavailable)
	if got := env.messenger.last(chatID); got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

func TestHandleUpdate_DashboardIssuesLoginLink(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 3, DashboardBaseURL: "https://dash.example.com"})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })

	svc.HandleUpdate(context.Background(), &messaging.IncomingMessage{ChatID: chatID, Command: "/dashboard"})

	got := env.messenger.last(chatID)
	if !strings.Contains(got, "https://dash.example.com/auth/verify?token=") {
		t.Errorf("reply = %q, want it to contain the dashboard login link", got)
	}
}

// TestHandleUpdate_Budget_ShowSetAndValidate covers /budget end to end: no
// argument shows the default 50/30/20 split, three valid arguments replace
// it (and EvaluateBudget picks up the change immediately), and invalid
// input (wrong argument count, non-numeric, doesn't sum to 100) is rejected
// without changing the stored allocation.
func TestHandleUpdate_Budget_ShowSetAndValidate(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	// No arguments -> shows the auto-created default.
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/budget"})
	got := env.messenger.last(chatID)
	if !strings.Contains(got, "50") || !strings.Contains(got, "30") || !strings.Contains(got, "20") {
		t.Errorf("show reply = %q, want it to mention the default 50/30/20 split", got)
	}

	// Wrong argument count -> usage message.
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/budget", CommandArgs: "50 30"})
	wantUsage := i18n.T(models.LanguageID, i18n.KeyBudgetSetUsage)
	if got := env.messenger.last(chatID); got != wantUsage {
		t.Errorf("wrong-arg-count reply = %q, want %q", got, wantUsage)
	}

	// Non-numeric argument -> usage message.
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/budget", CommandArgs: "fifty 30 20"})
	if got := env.messenger.last(chatID); got != wantUsage {
		t.Errorf("non-numeric reply = %q, want %q", got, wantUsage)
	}

	// Doesn't sum to 100 -> invalid message, not silently accepted.
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/budget", CommandArgs: "50 30 30"})
	wantInvalid := i18n.T(models.LanguageID, i18n.KeyBudgetSetInvalid)
	if got := env.messenger.last(chatID); got != wantInvalid {
		t.Errorf("sum!=100 reply = %q, want %q", got, wantInvalid)
	}

	// Valid set -> success, and takes effect immediately.
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/budget", CommandArgs: "60 20 20"})
	setReply := env.messenger.last(chatID)
	if !strings.Contains(setReply, "60") {
		t.Errorf("set reply = %q, want it to confirm the new 60%% Needs allocation", setReply)
	}

	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/budget"})
	showReply := env.messenger.last(chatID)
	if !strings.Contains(showReply, "60") {
		t.Errorf("show reply after set = %q, want it to reflect the new 60%% Needs allocation", showReply)
	}

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	var activeCount int64
	env.db.Model(&models.Budget{}).Where("user_id = ? AND is_active = ?", user.ID, true).Count(&activeCount)
	if activeCount != 1 {
		t.Errorf("active budget row count = %d, want exactly 1 (the invalid attempts must not have created extra rows)", activeCount)
	}
}

func TestHandleUpdate_ClaimInvalidCode(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 3, RewardMaxClaimsPerDay: 1, RewardBonusAmount: 3})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })

	// No code was ever issued, so any code claimed is invalid.
	svc.HandleUpdate(context.Background(), &messaging.IncomingMessage{ChatID: chatID, Command: "/claim", CommandArgs: "BOGUS123"})

	want := i18n.T(models.LanguageID, i18n.KeyClaimInvalidCode)
	if got := env.messenger.last(chatID); got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

// TestHandleUpdate_TransactionInput_Success covers the default (non-command)
// routing path: unambiguous AI parse -> a real Transaction row is persisted
// and the user gets a confirmation reply.
func TestHandleUpdate_TransactionInput_Success(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 3})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })

	env.ai.parseResult = &ai.ParsedTransaction{
		Type:         "expense",
		Amount:       25000,
		Currency:     "IDR",
		CategoryName: "Food",
		Description:  "Lunch",
	}

	svc.HandleUpdate(context.Background(), &messaging.IncomingMessage{ChatID: chatID, Text: "lunch 25000"})

	if got := env.ai.callCount(); got != 1 {
		t.Fatalf("AI client calls = %d, want 1", got)
	}

	got := env.messenger.last(chatID)
	if !strings.Contains(got, i18n.FormatMoney(models.LanguageID, 25000, "IDR")) {
		t.Errorf("reply = %q, want it to mention the amount", got)
	}

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	var count int64
	env.db.Model(&models.Transaction{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 1 {
		t.Errorf("transaction count = %d, want 1", count)
	}
}

// TestHandleUpdate_ConversationHistory_ThreadedIntoSecondCall covers the
// actual plumbing for "continue multi-round with users without losing
// context": the AI call for a second message must be given the first
// message and its reply as history, and neither the current message nor
// anything from a DIFFERENT user's conversation should leak in.
func TestHandleUpdate_ConversationHistory_ThreadedIntoSecondCall(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "expense", Amount: 50000, Currency: "IDR", CategoryName: "food", Description: "makan di restoran mahal",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "abis makan di restoran mahal tadi"})

	// The very first call for a brand-new conversation must see no history
	// at all — nothing to leak from a conversation that didn't exist yet.
	if got := env.ai.getLastHistory(); len(got.Recent) != 0 {
		t.Errorf("first message's history.Recent = %+v, want empty (nothing came before it)", got.Recent)
	}

	firstReply := env.messenger.last(chatID)

	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "expense", Amount: 150000, Currency: "IDR", CategoryName: "food", Description: "makan di restoran mahal",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "totalnya 150rb"})

	history := env.ai.getLastHistory()
	if len(history.Recent) != 2 {
		t.Fatalf("second message's history.Recent has %d turns, want 2 (first user message + its assistant reply)", len(history.Recent))
	}
	if history.Recent[0].Role != "user" || history.Recent[0].Content != "abis makan di restoran mahal tadi" {
		t.Errorf("history.Recent[0] = %+v, want the first user message", history.Recent[0])
	}
	if history.Recent[1].Role != "assistant" || history.Recent[1].Content != firstReply {
		t.Errorf("history.Recent[1] = %+v, want the bot's actual first reply %q", history.Recent[1], firstReply)
	}
	// The current message ("totalnya 150rb") must NOT appear in its own
	// history — it's already passed as userText, including it again would
	// double-count it in the AI's context.
	for _, turn := range history.Recent {
		if turn.Content == "totalnya 150rb" {
			t.Errorf("history.Recent contains the current message itself: %+v", history.Recent)
		}
	}
}

// TestHandleUpdate_TransactionInput_MultipleRecordsInOneMessage reproduces
// the reported bug: a single message listing several distinct transactions
// ("pengeluaran tadi: bensin mobil 300k, makan siang 300k, makan malam
// 100k") must record all of them from one AI call, not just the first —
// exactly the scenario ai.AIClient's ParseTransactionText now returns a
// slice for (see internal/ai/prompt/tool_schema.go's "call record_transaction
// once per transaction" instruction).
func TestHandleUpdate_TransactionInput_MultipleRecordsInOneMessage(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 3})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })

	env.ai.parseResults = []*ai.ParsedTransaction{
		{Type: "expense", Amount: 300_000, Currency: "IDR", CategoryName: "transport", Description: "bensin mobil"},
		{Type: "expense", Amount: 300_000, Currency: "IDR", CategoryName: "food", Description: "makan siang"},
		{Type: "expense", Amount: 100_000, Currency: "IDR", CategoryName: "food", Description: "makan malam"},
	}

	svc.HandleUpdate(context.Background(), &messaging.IncomingMessage{
		ChatID: chatID,
		Text:   "pengeluaran tadi:\nbensin mobile 300k\nmakan siang 300k\nmakan malam 100k",
	})

	// The whole batch must come from a single AI call — that's the entire
	// point (previously this took 3 separate messages/calls, or silently
	// dropped 2 of the 3 with only one round trip).
	if got := env.ai.callCount(); got != 1 {
		t.Fatalf("AI client calls = %d, want 1", got)
	}

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	var count int64
	env.db.Model(&models.Transaction{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 3 {
		t.Errorf("transaction count = %d, want 3 (one per line item, not just the first)", count)
	}

	var total float64
	env.db.Model(&models.Transaction{}).Where("user_id = ?", user.ID).Select("COALESCE(SUM(original_amount), 0)").Scan(&total)
	if total != 700_000 {
		t.Errorf("sum of recorded amounts = %.2f, want 700000.00", total)
	}

	got := env.messenger.last(chatID)
	for _, want := range []string{
		i18n.FormatMoney(models.LanguageID, 300_000, "IDR"),
		i18n.FormatMoney(models.LanguageID, 100_000, "IDR"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("reply = %q, want it to mention %s", got, want)
		}
	}
}

// TestHandleUpdate_TransactionInput_Ambiguous covers the AI asking a
// clarifying question instead of returning a parsed result: no transaction
// should be created, and the clarification question is relayed verbatim.
func TestHandleUpdate_TransactionInput_Ambiguous(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 3})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })

	env.ai.parseResult = &ai.ParsedTransaction{
		IsAmbiguous:           true,
		ClarificationQuestion: "How much did you spend?",
	}

	svc.HandleUpdate(context.Background(), &messaging.IncomingMessage{ChatID: chatID, Text: "i bought something"})

	if got := env.messenger.last(chatID); got != "How much did you spend?" {
		t.Errorf("reply = %q, want the clarification question verbatim", got)
	}

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	var count int64
	env.db.Model(&models.Transaction{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 0 {
		t.Errorf("transaction count = %d, want 0 for an ambiguous parse", count)
	}
}

// TestHandleUpdate_TransactionInput_VoiceUnsupported covers a provider that
// can't transcribe audio: the user gets a clear message, not a generic
// "trouble" error.
func TestHandleUpdate_TransactionInput_VoiceUnsupported(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 3})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })

	env.ai.parseErr = apperror.ErrProviderAudioUnsupported
	env.messenger.downloadData = []byte("fake audio")
	env.messenger.downloadMime = "audio/ogg"

	svc.HandleUpdate(context.Background(), &messaging.IncomingMessage{ChatID: chatID, IsVoice: true, VoiceFileID: "file123"})

	want := i18n.T(models.LanguageID, i18n.KeyVoiceUnsupported, "mock")
	if got := env.messenger.last(chatID); got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

// TestHandleUpdate_TransactionInput_TooLong covers the too-long-input
// guardrail: an over-limit text message must never reach the AI client,
// and the user must get an actual reply explaining why — this replaced a
// silent HTTP-middleware-level rejection the user never saw at all.
func TestHandleUpdate_TransactionInput_TooLong(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 3, MaxInputChars: 10})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })

	svc.HandleUpdate(context.Background(), &messaging.IncomingMessage{ChatID: chatID, Text: "this message is way over the ten character limit"})

	if got := env.ai.callCount(); got != 0 {
		t.Errorf("AI calls for over-limit input = %d, want 0 (blocked)", got)
	}

	want := i18n.T(models.LanguageID, i18n.KeyInputTooLong, 10)
	if got := env.messenger.last(chatID); got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

// TestHandleUpdate_TransactionInput_VoiceBypassesLengthCheck confirms voice
// messages are never subject to the text-length guardrail, since they have
// no text to measure until after transcription.
func TestHandleUpdate_TransactionInput_VoiceBypassesLengthCheck(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 3, MaxInputChars: 10})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })

	env.ai.parseResult = &ai.ParsedTransaction{Type: "expense", Amount: 1000, Currency: "IDR", CategoryName: "Food", Description: "x"}
	env.messenger.downloadData = []byte("fake audio")
	env.messenger.downloadMime = "audio/ogg"

	svc.HandleUpdate(context.Background(), &messaging.IncomingMessage{ChatID: chatID, IsVoice: true, VoiceFileID: "file123"})

	if got := env.ai.callCount(); got != 1 {
		t.Errorf("AI calls for voice message = %d, want 1 (not blocked by text length)", got)
	}
}

// TestHandleUpdate_TransactionInput_Photo covers a receipt photo being
// recorded the same way a text message would be — Source is "photo" and
// RawInput carries the placeholder + caption (never the actual image
// bytes), mirroring how a voice note's RawInput is "[voice note]" rather
// than the transcript or audio bytes.
func TestHandleUpdate_TransactionInput_Photo(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 3})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })

	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "expense", Amount: 150_000, Currency: "IDR", CategoryName: "groceries", Description: "Indomaret: beras, telur, susu",
	}
	env.messenger.downloadData = []byte("fake jpeg bytes")
	env.messenger.downloadMime = "image/jpeg"

	svc.HandleUpdate(context.Background(), &messaging.IncomingMessage{ChatID: chatID, IsPhoto: true, PhotoFileID: "photo123", Caption: "buat kantor"})

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	var tx models.Transaction
	if err := env.db.Where("user_id = ?", user.ID).First(&tx).Error; err != nil {
		t.Fatalf("load recorded transaction: %v", err)
	}
	if tx.Source != models.SourcePhoto {
		t.Errorf("Source = %q, want %q", tx.Source, models.SourcePhoto)
	}
	if tx.RawInput != "[receipt photo]: buat kantor" {
		t.Errorf("RawInput = %q, want the placeholder + caption, never the actual image bytes", tx.RawInput)
	}

	wantLine := i18n.T(models.LanguageID, i18n.KeyTransactionRecordedWithDescription,
		"expense", i18n.FormatMoney(models.LanguageID, 150_000, "IDR"), "groceries", "Indomaret: beras, telur, susu")
	if got := env.messenger.last(chatID); !strings.HasPrefix(got, wantLine) {
		t.Errorf("reply = %q, want it to start with %q (the receipt's own description, not just the bare amount/category)", got, wantLine)
	}
}

// TestHandleUpdate_TransactionInput_PhotoDownloadFailed covers a Telegram
// download failure: the user gets KeyReceiptDownloadFailed and nothing is
// recorded, mirroring the voice-note download-failure path exactly.
func TestHandleUpdate_TransactionInput_PhotoDownloadFailed(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 3})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })

	env.messenger.downloadErr = fmt.Errorf("telegram getFile failed")

	svc.HandleUpdate(context.Background(), &messaging.IncomingMessage{ChatID: chatID, IsPhoto: true, PhotoFileID: "photo123"})

	want := i18n.T(models.LanguageID, i18n.KeyReceiptDownloadFailed)
	if got := env.messenger.last(chatID); got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
	if got := env.ai.callCount(); got != 0 {
		t.Errorf("AI calls after a download failure = %d, want 0", got)
	}
}

// TestHandleUpdate_TransactionInput_PhotoAmbiguous covers an unreadable or
// non-receipt photo: the model's ambiguous result must produce the
// clarification question, not a hallucinated transaction.
func TestHandleUpdate_TransactionInput_PhotoAmbiguous(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 3})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })

	env.ai.parseResult = &ai.ParsedTransaction{IsAmbiguous: true, ClarificationQuestion: "I couldn't read that receipt — how much was it?"}
	env.messenger.downloadData = []byte("fake blurry jpeg")
	env.messenger.downloadMime = "image/jpeg"

	svc.HandleUpdate(context.Background(), &messaging.IncomingMessage{ChatID: chatID, IsPhoto: true, PhotoFileID: "photo123"})

	if got := env.messenger.last(chatID); got != "I couldn't read that receipt — how much was it?" {
		t.Errorf("reply = %q, want the clarification question verbatim", got)
	}

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	var count int64
	env.db.Model(&models.Transaction{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 0 {
		t.Errorf("transaction count = %d, want 0 for an ambiguous parse", count)
	}
}

// TestHandleUpdate_TransactionInput_PhotoBypassesLengthCheck confirms a
// photo with no caption is never subject to the text-length guardrail
// (there's no text at all to measure), mirroring voice's own bypass.
func TestHandleUpdate_TransactionInput_PhotoBypassesLengthCheck(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 3, MaxInputChars: 10})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })

	env.ai.parseResult = &ai.ParsedTransaction{Type: "expense", Amount: 1000, Currency: "IDR", CategoryName: "Food", Description: "x"}
	env.messenger.downloadData = []byte("fake jpeg")
	env.messenger.downloadMime = "image/jpeg"

	svc.HandleUpdate(context.Background(), &messaging.IncomingMessage{ChatID: chatID, IsPhoto: true, PhotoFileID: "photo123"})

	if got := env.ai.callCount(); got != 1 {
		t.Errorf("AI calls for a captionless photo = %d, want 1 (not blocked by text length)", got)
	}
}

// TestHandleUpdate_TransactionInput_PhotoCaptionTooLong confirms a photo's
// CAPTION is still subject to the length guardrail, unlike the photo itself
// — a very long caption is real user-typed text like any other message.
func TestHandleUpdate_TransactionInput_PhotoCaptionTooLong(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 3, MaxInputChars: 10})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })

	svc.HandleUpdate(context.Background(), &messaging.IncomingMessage{ChatID: chatID, IsPhoto: true, PhotoFileID: "photo123", Caption: "this caption is way over the ten character limit"})

	if got := env.ai.callCount(); got != 0 {
		t.Errorf("AI calls for an over-limit caption = %d, want 0 (blocked)", got)
	}
	want := i18n.T(models.LanguageID, i18n.KeyInputTooLong, 10)
	if got := env.messenger.last(chatID); got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

// TestHandleUpdate_SpendingQuery_OverallTotal covers the "how much have I
// spent" query intent with no category filter: the AI's query-intent parse
// must never create a Transaction row, and the reply must total only the
// expense(s) seeded beforehand.
func TestHandleUpdate_SpendingQuery_OverallTotal(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	// Seed one real expense to query against.
	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "expense", Amount: 25000, Currency: "IDR", CategoryName: "Food", Description: "Lunch",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "lunch 25000"})

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent:      ai.IntentQuery,
		QueryPeriod: ai.PeriodMonthly,
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "berapa aku udah ngeluarin duit bulan ini?"})

	got := env.messenger.last(chatID)
	want := i18n.FormatMoney(models.LanguageID, 25000, "IDR")
	if !strings.Contains(got, want) {
		t.Errorf("reply = %q, want it to contain the total %s", got, want)
	}

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	var count int64
	env.db.Model(&models.Transaction{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 1 {
		t.Errorf("transaction count = %d, want 1 (only the seeded one — the query itself must not record anything)", count)
	}
}

// TestHandleUpdate_SpendingQuery_OverallTotal_IncludesCategoryBreakdown
// covers "habis buat apa saja?" (spent on what?) style follow-ups to a plain
// total query: since there's no category/bucket filter, the reply must list
// each category's own amount underneath the headline total, not just the
// single number — reusing the same TopCategories query the monthly recap
// already relies on.
func TestHandleUpdate_SpendingQuery_OverallTotal_IncludesCategoryBreakdown(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "expense", Amount: 300_000, Currency: "IDR", CategoryName: "Food", Description: "groceries",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "belanja 300000"})

	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "expense", Amount: 100_000, Currency: "IDR", CategoryName: "Transport", Description: "gas",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "bensin 100000"})

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent:      ai.IntentQuery,
		QueryPeriod: ai.PeriodMonthly,
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "habis buat apa saja?"})

	got := env.messenger.last(chatID)
	wantTotal := i18n.FormatMoney(models.LanguageID, 400_000, "IDR")
	if !strings.Contains(got, wantTotal) {
		t.Errorf("reply = %q, want it to contain the overall total %s", got, wantTotal)
	}
	if !strings.Contains(got, "food") || !strings.Contains(got, "transport") {
		t.Errorf("reply = %q, want it to list both categories (food, transport)", got)
	}
	if !strings.Contains(got, i18n.FormatMoney(models.LanguageID, 300_000, "IDR")) ||
		!strings.Contains(got, i18n.FormatMoney(models.LanguageID, 100_000, "IDR")) {
		t.Errorf("reply = %q, want it to show each category's own amount", got)
	}
}

// TestHandleUpdate_SpendingQuery_OverallTotal_UncategorizedGetsALabel covers
// a real gap found from a live chat: a transaction with no category at all
// (e.g. transaction.Service.CreateAdjustment's /sync "blind spot" entry,
// reproduced here via a normal record with an empty CategoryName, which
// resolveCategory treats identically — nil CategoryID) must not render as a
// blank, unexplained breakdown line like "1. : Rp500.000,00 (...)" — it
// needs the "Uncategorized"/"Tanpa kategori" label instead.
func TestHandleUpdate_SpendingQuery_OverallTotal_UncategorizedGetsALabel(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "expense", Amount: 500_000, Currency: "IDR", CategoryName: "", Description: "blind spot adjustment",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "adjustment 500000"})

	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "expense", Amount: 100_000, Currency: "IDR", CategoryName: "food",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "makan 100000"})

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent:      ai.IntentQuery,
		QueryPeriod: ai.PeriodMonthly,
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "pengeluaranku bulan ini apa saja?"})

	got := env.messenger.last(chatID)
	if !strings.Contains(got, i18n.T(models.LanguageID, i18n.KeyUncategorizedLabel)) {
		t.Errorf("reply = %q, want it to label the uncategorized transaction as %q instead of a blank line", got, i18n.T(models.LanguageID, i18n.KeyUncategorizedLabel))
	}
	if !strings.Contains(got, i18n.FormatMoney(models.LanguageID, 500_000, "IDR")) {
		t.Errorf("reply = %q, want it to still show the uncategorized transaction's amount", got)
	}
	// "1. : Rp..." — a colon directly preceded by whitespace/line-start with
	// nothing before it — is exactly the blank line seen in the reported bug.
	if strings.Contains(got, "1. : ") || strings.Contains(got, "2. : ") {
		t.Errorf("reply = %q, must not contain a blank category name in the breakdown", got)
	}
}

// TestHandleUpdate_SpendingQuery_CategoryTotal covers the optional category
// filter, including that the match is case-insensitive against the stored
// category name.
func TestHandleUpdate_SpendingQuery_CategoryTotal(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "expense", Amount: 40000, Currency: "IDR", CategoryName: "Food", Description: "Dinner",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "dinner 40000"})

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent:        ai.IntentQuery,
		QueryPeriod:   ai.PeriodMonthly,
		QueryCategory: "food", // lowercase — stored category is "Food"
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "berapa aku keluar buat makan bulan ini?"})

	got := env.messenger.last(chatID)
	want := i18n.FormatMoney(models.LanguageID, 40000, "IDR")
	if !strings.Contains(got, want) {
		t.Errorf("reply = %q, want it to contain the total %s", got, want)
	}

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	var count int64
	env.db.Model(&models.Transaction{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 1 {
		t.Errorf("transaction count = %d, want 1 (only the seeded one)", count)
	}
}

// TestHandleUpdate_SpendingQuery_CategoryNotFound covers a category filter
// that matches nothing: the bot must say so explicitly rather than silently
// reporting a misleading zero (which would look identical to "you spent
// nothing in a real category").
func TestHandleUpdate_SpendingQuery_CategoryNotFound(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent:        ai.IntentQuery,
		QueryPeriod:   ai.PeriodMonthly,
		QueryCategory: "zzz-not-a-real-category-zzz",
	}
	svc.HandleUpdate(context.Background(), &messaging.IncomingMessage{ChatID: chatID, Text: "berapa aku keluar buat zzz bulan ini?"})

	want := i18n.T(models.LanguageID, i18n.KeyQueryCategoryNotFound, "zzz-not-a-real-category-zzz", i18n.PeriodLabel(models.LanguageID, ai.PeriodMonthly))
	if got := env.messenger.last(chatID); got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

// TestHandleUpdate_SpendingQuery_BucketBreakdown covers the bug found while
// testing a real chat: the bot offers to break down spending by a budget
// bucket (e.g. "Wants exceeded 30%, want a breakdown?"), the user confirms
// with a short "iya tolong", and the reply must be the actual per-category
// breakdown WITHIN that bucket — not a generic total-spend reply. Since
// mockAIClient doesn't do real classification, this exercises the
// resolveQueryBucket/handleBucketSpendingQuery path directly by setting
// QueryBucket the way the fixed prompt now instructs the model to for such
// a confirmation, and confirms an unrelated Needs expense is excluded.
func TestHandleUpdate_SpendingQuery_BucketBreakdown(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "expense", Amount: 200_000, Currency: "IDR", CategoryName: "entertainment", Bucket: "wants", Description: "nonton",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "nonton 200000"})

	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "expense", Amount: 100_000, Currency: "IDR", CategoryName: "shopping", Bucket: "wants", Description: "sepatu",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "beli sepatu 100000"})

	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "expense", Amount: 500_000, Currency: "IDR", CategoryName: "rent", Bucket: "needs", Description: "sewa",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "sewa 500000"})

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent:      ai.IntentQuery,
		QueryPeriod: ai.PeriodMonthly,
		QueryBucket: "wants",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "iya tolong"})

	got := env.messenger.last(chatID)
	wantTotal := i18n.FormatMoney(models.LanguageID, 300_000, "IDR")
	if !strings.Contains(got, wantTotal) {
		t.Errorf("reply = %q, want it to contain the Wants total %s (excluding the Needs rent expense)", got, wantTotal)
	}
	if !strings.Contains(got, "entertainment") || !strings.Contains(got, "shopping") {
		t.Errorf("reply = %q, want it to list both Wants categories (entertainment, shopping)", got)
	}
	if strings.Contains(got, "rent") {
		t.Errorf("reply = %q, must not include the Needs-bucketed rent expense", got)
	}
	if strings.Contains(got, i18n.FormatMoney(models.LanguageID, 500_000, "IDR")) {
		t.Errorf("reply = %q, must not mention the rent amount", got)
	}
}

// TestHandleUpdate_SpendingQuery_BucketBreakdown_CategoryFieldFallback covers
// the safety net in resolveQueryBucket: some parses put the bucket word in
// QueryCategory instead of the newer QueryBucket field, and "wants" would
// never match a real category name via CategoryTotal anyway, so it must
// still resolve to the bucket breakdown rather than a "category not found"
// reply.
func TestHandleUpdate_SpendingQuery_BucketBreakdown_CategoryFieldFallback(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "expense", Amount: 150_000, Currency: "IDR", CategoryName: "entertainment", Bucket: "wants",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "nonton 150000"})

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent:        ai.IntentQuery,
		QueryPeriod:   ai.PeriodMonthly,
		QueryCategory: "wants",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "keinginan aku berapa bulan ini?"})

	got := env.messenger.last(chatID)
	want := i18n.FormatMoney(models.LanguageID, 150_000, "IDR")
	if !strings.Contains(got, want) {
		t.Errorf("reply = %q, want it to contain the Wants total %s, not a \"category not found\" reply", got, want)
	}
	if strings.Contains(got, "tidak menemukan kategori") {
		t.Errorf("reply = %q, must not fall back to the category-not-found reply for a bucket name", got)
	}
}

// TestHandleUpdate_SpendingQuery_ListByCategory covers "beli apa aja buat
// food bulan ini?" (what did I buy for food this month?) style requests —
// ai.QueryModeList — which must reply with the individual transactions
// (date, category, amount, description), not a total.
func TestHandleUpdate_SpendingQuery_ListByCategory(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "expense", Amount: 30_000, Currency: "IDR", CategoryName: "food", Description: "nasi goreng",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "nasi goreng 30000"})

	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "expense", Amount: 20_000, Currency: "IDR", CategoryName: "food", Description: "es teh",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "es teh 20000"})

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent: ai.IntentQuery, QueryPeriod: ai.PeriodMonthly, QueryCategory: "food", QueryMode: ai.QueryModeList,
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "untuk food beli apa aja bulan ini?"})

	got := env.messenger.last(chatID)
	if !strings.Contains(got, "nasi goreng") || !strings.Contains(got, "es teh") {
		t.Errorf("reply = %q, want it to list both purchases by description", got)
	}
	if !strings.Contains(got, i18n.FormatMoney(models.LanguageID, 30_000, "IDR")) ||
		!strings.Contains(got, i18n.FormatMoney(models.LanguageID, 20_000, "IDR")) {
		t.Errorf("reply = %q, want it to show each item's own amount, not a total", got)
	}
	wantTotal := i18n.FormatMoney(models.LanguageID, 50_000, "IDR")
	if strings.Contains(got, wantTotal) {
		t.Errorf("reply = %q, must not collapse to the combined total %s", got, wantTotal)
	}
}

// TestHandleUpdate_SpendingQuery_ListPagination covers the requested
// pagination behavior: a list capped at 10 items with a "+N more" note, and
// a follow-up "lanjutkan" (QueryListMore=true, same period/category) resumes
// from the server-side cursor instead of repeating the first page or
// requiring the model to compute an offset itself.
func TestHandleUpdate_SpendingQuery_ListPagination(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 20})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	for i := 1; i <= 12; i++ {
		env.ai.parseResult = &ai.ParsedTransaction{
			Type: "expense", Amount: float64(i) * 1000, Currency: "IDR", CategoryName: "food",
			Description: fmt.Sprintf("item %d", i),
		}
		svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: fmt.Sprintf("food item %d, %d000", i, i)})
	}

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent: ai.IntentQuery, QueryPeriod: ai.PeriodMonthly, QueryCategory: "food", QueryMode: ai.QueryModeList,
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "listing pengeluaran food bulan ini"})

	firstPage := env.messenger.last(chatID)
	if !strings.Contains(firstPage, "item 12") || !strings.Contains(firstPage, "item 3") {
		t.Errorf("firstPage = %q, want the 10 most recent items (12 down to 3)", firstPage)
	}
	if strings.Contains(firstPage, "item 1)") || strings.Contains(firstPage, "item 2)") {
		t.Errorf("firstPage = %q, must not already include items 1 or 2 (those are on page 2)", firstPage)
	}
	if !strings.Contains(firstPage, i18n.T(models.LanguageID, i18n.KeyQuerySpendingListMore, 2)) {
		t.Errorf("firstPage = %q, want a \"+2 more\" style note for the 2 remaining items", firstPage)
	}

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	t.Cleanup(func() { env.redis.Del(context.Background(), fmt.Sprintf("aura:spendinglistcursor:%d", user.ID)) })

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent: ai.IntentQuery, QueryPeriod: ai.PeriodMonthly, QueryCategory: "food", QueryMode: ai.QueryModeList, QueryListMore: true,
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "lanjutkan"})

	secondPage := env.messenger.last(chatID)
	if !strings.Contains(secondPage, "item 2") || !strings.Contains(secondPage, "item 1") {
		t.Errorf("secondPage = %q, want the remaining items 2 and 1", secondPage)
	}
	if strings.Contains(secondPage, "item 12") || strings.Contains(secondPage, "item 3") {
		t.Errorf("secondPage = %q, must not repeat items already shown on the first page", secondPage)
	}
}

// TestHandleUpdate_SpendingQuery_UncategorizedList reproduces a real chat
// bug: after seeing "1. 30 Sep — Tanpa kategori: Rp3.432.000,00 (...)" in a
// list reply, the user's follow-up ("yang tanpa kategori bisa jelasin lagi
// habis buat apa?") had the model guess QueryCategory="none" — which
// ILIKE-matched no real category, so the bot flatly denied having any such
// expense ("Kamu belum ada pengeluaran untuk none bulan ini.") even though
// it had just shown one. isUncategorizedQuery must recognize "none" (and
// the Indonesian label itself) and answer with the actual uncategorized
// transaction, using the translated label rather than the raw guess.
func TestHandleUpdate_SpendingQuery_UncategorizedList(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "expense", Amount: 3_432_000, Currency: "IDR", CategoryName: "", Description: "Balance reconciliation adjustment (blind spot)",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "adjustment 3432000"})

	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "expense", Amount: 100_000, Currency: "IDR", CategoryName: "food", Description: "roti",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "roti 100000"})

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent: ai.IntentQuery, QueryPeriod: ai.PeriodMonthly, QueryCategory: "none", QueryMode: ai.QueryModeList,
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "yang tanpa kategori bisa jelasin lagi habis buat apa?"})

	got := env.messenger.last(chatID)
	wantLabel := i18n.T(models.LanguageID, i18n.KeyUncategorizedLabel)
	if !strings.Contains(got, wantLabel) {
		t.Errorf("reply = %q, want it to use the translated label %q instead of the raw \"none\"", got, wantLabel)
	}
	if strings.Contains(got, "untuk none ") {
		t.Errorf("reply = %q, must not leak the raw \"none\" value", got)
	}
	if !strings.Contains(got, i18n.FormatMoney(models.LanguageID, 3_432_000, "IDR")) {
		t.Errorf("reply = %q, want it to show the uncategorized transaction's actual amount, not deny it exists", got)
	}
	if strings.Contains(got, "roti") {
		t.Errorf("reply = %q, must not include the categorized food expense", got)
	}
}

// TestHandleUpdate_SpendingQuery_UncategorizedTotal covers the same
// recognition for a total-mode (non-list) follow-up, e.g. "berapa total yang
// tanpa kategori bulan ini?" — must sum only the uncategorized transactions
// and label them naturally rather than reporting a category-not-found error
// for the literal word the model guessed.
func TestHandleUpdate_SpendingQuery_UncategorizedTotal(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "expense", Amount: 500_000, Currency: "IDR", CategoryName: "",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "adjustment 500000"})

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent: ai.IntentQuery, QueryPeriod: ai.PeriodMonthly, QueryCategory: "tanpa kategori",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "berapa total yang tanpa kategori bulan ini?"})

	got := env.messenger.last(chatID)
	wantLabel := i18n.T(models.LanguageID, i18n.KeyUncategorizedLabel)
	want := i18n.FormatMoney(models.LanguageID, 500_000, "IDR")
	if !strings.Contains(got, want) || !strings.Contains(got, wantLabel) {
		t.Errorf("reply = %q, want it to report %s for %q", got, want, wantLabel)
	}
	if strings.Contains(got, "tidak menemukan kategori") {
		t.Errorf("reply = %q, must not fall back to the category-not-found reply", got)
	}
}

// TestHandleUpdate_Subscribe_InactiveToggle covers the MIDTRANS_ACTIVE kill
// switch: even with a valid-looking server key configured, active=false
// must make /subscribe (and its natural-language equivalent) reply
// KeySubscribeUnavailable — never attempt a checkout — so an operator can
// pause new subscriptions without touching real credentials. Builds its own
// bot.Service (rather than setupTestEnv, which fixes active=true for every
// other test in this file) since this is the one test that needs the
// toggle off.
func TestHandleUpdate_Subscribe_InactiveToggle(t *testing.T) {
	appCfg, err := config.Load()
	if err != nil {
		t.Skipf("skipping integration test, config load failed: %v", err)
	}
	gormDB, err := db.NewPostgres(appCfg)
	if err != nil {
		t.Skipf("skipping integration test, postgres not reachable: %v", err)
	}
	if err := db.RunMigrations(gormDB); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	redisClient := redis.NewClient(&redis.Options{Addr: fmt.Sprintf("%s:%s", appCfg.RedisHost, appCfg.RedisPort)})
	if err := redisClient.Ping(context.Background()).Err(); err != nil {
		t.Skipf("skipping integration test, redis not reachable: %v", err)
	}

	messenger := newMockMessenger()
	aiClient := &mockAIClient{}
	txSvc := transaction.NewService(gormDB, fx.NewStaticService())
	budgetSvc := budget.NewService(gormDB)
	reconSvc := reconciliation.NewService(gormDB, txSvc)
	usageSvc := usage.NewService(gormDB)
	reportSvc := report.NewService(gormDB)
	convSvc := conversation.NewService(gormDB, aiClient, conversation.Config{RecentLimit: 10, SummarizeThreshold: 20})
	recurSvc := recurring.NewService(gormDB)
	goalSvc := savingsgoal.NewService(gormDB, budgetSvc)
	// A real-looking key with active=false — proves the toggle blocks
	// availability independently of whether credentials are present.
	paymentSvc := payment.NewService(gormDB, redisClient, "test-midtrans-key", false, false)
	svc := bot.NewService(gormDB, redisClient, messenger, aiClient, txSvc, budgetSvc, reconSvc, usageSvc, reportSvc, convSvc, recurSvc, goalSvc, paymentSvc, bot.Config{FreeTierDailyQuota: 5})
	env := &testEnv{db: gormDB, redis: redisClient, messenger: messenger, ai: aiClient}

	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/subscribe"})
	got := env.messenger.last(chatID)
	want := i18n.T(models.LanguageID, i18n.KeySubscribeUnavailable)
	if got != want {
		t.Errorf("/subscribe reply = %q, want %q (MIDTRANS_ACTIVE=false must block even with a key present)", got, want)
	}

	env.ai.parseResult = &ai.ParsedTransaction{Intent: ai.IntentSubscribe}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "aku mau upgrade ke pro dong"})
	got = env.messenger.last(chatID)
	if got != want {
		t.Errorf("natural-language subscribe reply = %q, want %q (the toggle must also block the natural-language path)", got, want)
	}
}

// TestHandleUpdate_Subscribe_AlreadyPending covers /subscribe's
// double-checkout guard: a pending order_id already cached for the user
// must produce KeySubscribePending, never a second checkout link, and
// never touch User.Tier.
func TestHandleUpdate_Subscribe_AlreadyPending(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	// Seed a user first (a plain record keeps this test independent of any
	// particular AI intent).
	env.ai.parseResult = &ai.ParsedTransaction{Type: "expense", Amount: 1000, Currency: "IDR", CategoryName: "food"}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "makan 1000"})

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	pendingKey := fmt.Sprintf("aura:paymentpending:%d", user.ID)
	if err := env.redis.Set(ctx, pendingKey, "sub-existing-order", time.Hour).Err(); err != nil {
		t.Fatalf("seed pending checkout key: %v", err)
	}
	t.Cleanup(func() { env.redis.Del(context.Background(), pendingKey) })

	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/subscribe"})

	got := env.messenger.last(chatID)
	want := i18n.T(models.LanguageID, i18n.KeySubscribePending)
	if got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}

	var reloaded models.User
	env.db.First(&reloaded, user.ID)
	if reloaded.Tier != models.TierFree {
		t.Errorf("Tier = %q, want it untouched (still free) since no payment ever succeeded", reloaded.Tier)
	}
}

// TestHandleUpdate_Subscribe_NaturalLanguage covers the natural-language
// path to the exact same double-checkout guard as the /subscribe command
// above, confirming ai.IntentSubscribe dispatches to the identical handler
// rather than a separate code path.
func TestHandleUpdate_Subscribe_NaturalLanguage(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{Type: "expense", Amount: 1000, Currency: "IDR", CategoryName: "food"}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "makan 1000"})

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	pendingKey := fmt.Sprintf("aura:paymentpending:%d", user.ID)
	if err := env.redis.Set(ctx, pendingKey, "sub-existing-order", time.Hour).Err(); err != nil {
		t.Fatalf("seed pending checkout key: %v", err)
	}
	t.Cleanup(func() { env.redis.Del(context.Background(), pendingKey) })

	env.ai.parseResult = &ai.ParsedTransaction{Intent: ai.IntentSubscribe}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "aku mau upgrade ke pro dong"})

	got := env.messenger.last(chatID)
	want := i18n.T(models.LanguageID, i18n.KeySubscribePending)
	if got != want {
		t.Errorf("reply = %q, want %q (natural-language subscribe must reach the same handler as /subscribe)", got, want)
	}
}

// TestHandleUpdate_MySubscription_Free covers /mysubscription for a free
// user (the default) via both the slash command and its natural-language
// equivalent, both converging on the same reply.
func TestHandleUpdate_MySubscription_Free(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/mysubscription"})
	got := env.messenger.last(chatID)
	want := i18n.T(models.LanguageID, i18n.KeyMySubscriptionFree)
	if got != want {
		t.Errorf("/mysubscription reply = %q, want %q", got, want)
	}

	env.ai.parseResult = &ai.ParsedTransaction{Intent: ai.IntentSubscriptionStatus}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "aku pro ga sih?"})
	got = env.messenger.last(chatID)
	if got != want {
		t.Errorf("natural-language status reply = %q, want %q (same as /mysubscription)", got, want)
	}
}

// TestHandleUpdate_MySubscription_Pro covers a Pro user's /mysubscription
// reply showing the real renewal date from their active Subscription row.
func TestHandleUpdate_MySubscription_Pro(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{Type: "expense", Amount: 1000, Currency: "IDR", CategoryName: "food"}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "makan 1000"})

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if err := env.db.Model(&models.User{}).Where("id = ?", user.ID).Update("tier", models.TierPro).Error; err != nil {
		t.Fatalf("set user to pro: %v", err)
	}
	// Deliberately fixed and close to a UTC day boundary (23:30 UTC) rather
	// than time.Now() — pgx decodes a timestamptz column back into the
	// reading process's own time.Local, not UTC (verified empirically; the
	// absolute instant is correct, only the Location label differs), so a
	// value this close to midnight UTC reliably exposes a missing .UTC()
	// call on any machine east of UTC (this is exactly how the bug this
	// test now guards against was originally found: a flaky failure right
	// at a real UTC midnight). Using time.Now() here would only catch the
	// bug on the ~1-in-24 test runs that happen to straddle midnight.
	expiresAt := time.Date(2026, 10, 20, 23, 30, 0, 0, time.UTC)
	sub := &models.Subscription{
		UserID: user.ID, Tier: models.TierPro, Status: models.SubStatusActive,
		StartedAt: time.Now().UTC(), ExpiresAt: &expiresAt, PaymentRef: "sub-test-order",
	}
	if err := env.db.Create(sub).Error; err != nil {
		t.Fatalf("seed active subscription: %v", err)
	}
	t.Cleanup(func() { env.db.Unscoped().Delete(sub) })

	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/mysubscription"})
	got := env.messenger.last(chatID)
	want := i18n.T(models.LanguageID, i18n.KeyMySubscriptionPro, "2026-10-20")
	if got != want {
		t.Errorf("reply = %q, want %q (the UTC calendar date, regardless of the test machine's own local timezone)", got, want)
	}
}

// TestHandleUpdate_Sync_NoArgumentShowsBalance covers /sync with no
// argument reporting the calculated balance directly, instead of falling
// into the usage-instructions reply.
func TestHandleUpdate_Sync_NoArgumentShowsBalance(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "income", Amount: 500_000, Currency: "IDR", CategoryName: "salary",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "gaji 500000"})

	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/sync"})

	want := i18n.T(models.LanguageID, i18n.KeyBalance, i18n.FormatMoney(models.LanguageID, 500_000, "IDR"))
	if got := env.messenger.last(chatID); got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

// TestHandleUpdate_Sync_InvalidArgumentStillShowsUsage guards against the
// no-argument check above accidentally swallowing a genuinely invalid
// (non-numeric, non-empty) argument — that case must still show usage
// instructions, not silently fall back to a balance reply.
func TestHandleUpdate_Sync_InvalidArgumentStillShowsUsage(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })

	svc.HandleUpdate(context.Background(), &messaging.IncomingMessage{ChatID: chatID, Command: "/sync", CommandArgs: "notanumber"})

	want := i18n.T(models.LanguageID, i18n.KeySyncUsage)
	if got := env.messenger.last(chatID); got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

// TestHandleUpdate_BalanceQuery_NaturalLanguage covers the AI recognizing a
// balance question (ai.IntentBalance) and answering with the same message
// the /sync-with-no-argument path uses.
func TestHandleUpdate_BalanceQuery_NaturalLanguage(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "income", Amount: 300_000, Currency: "IDR", CategoryName: "salary",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "gaji 300000"})

	env.ai.parseResult = &ai.ParsedTransaction{Intent: ai.IntentBalance}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "berapa saldo aku sekarang?"})

	want := i18n.T(models.LanguageID, i18n.KeyBalance, i18n.FormatMoney(models.LanguageID, 300_000, "IDR"))
	if got := env.messenger.last(chatID); got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	var count int64
	env.db.Model(&models.Transaction{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 1 {
		t.Errorf("transaction count = %d, want 1 (the balance query itself must not record anything)", count)
	}
}

// TestHandleUpdate_Correction_ReplacesLastTransaction reproduces the
// reported bug: a correction message ("eh maaf, harusnya 1jt deh...typo")
// must replace the previous wrong transaction, not add a second one
// alongside it — exactly one non-deleted transaction should remain, at the
// corrected amount, with the original soft-deleted via the same
// UndoHistory("undone") mechanism /undo already uses.
func TestHandleUpdate_Correction_ReplacesLastTransaction(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "investment", Amount: 2_000_000, Currency: "IDR", CategoryName: "mutual fund",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "masukin ke reksa dana lusa kemarin 2jut"})

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent: ai.IntentCorrect, Type: "investment", Amount: 1_000_000, Currency: "IDR", CategoryName: "mutual fund",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "eh maaf, harusnya 1jt deh untuk reksa dana kemarin, typo"})

	got := env.messenger.last(chatID)
	wantAmount := i18n.FormatMoney(models.LanguageID, 1_000_000, "IDR")
	if !strings.Contains(got, wantAmount) {
		t.Errorf("reply = %q, want it to mention the corrected amount %s", got, wantAmount)
	}

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}

	var activeCount int64
	env.db.Model(&models.Transaction{}).Where("user_id = ?", user.ID).Count(&activeCount)
	if activeCount != 1 {
		t.Fatalf("active transaction count = %d, want 1 (the correction must replace, not add)", activeCount)
	}

	var activeTx models.Transaction
	if err := env.db.Where("user_id = ?", user.ID).First(&activeTx).Error; err != nil {
		t.Fatalf("load active transaction: %v", err)
	}
	if activeTx.ConvertedAmount != 1_000_000 {
		t.Errorf("active transaction amount = %.2f, want 1000000.00", activeTx.ConvertedAmount)
	}

	var deletedCount int64
	env.db.Unscoped().Model(&models.Transaction{}).Where("user_id = ? AND deleted_at IS NOT NULL", user.ID).Count(&deletedCount)
	if deletedCount != 1 {
		t.Errorf("soft-deleted transaction count = %d, want 1 (the original 2,000,000 entry)", deletedCount)
	}

	var undoneCount int64
	env.db.Model(&models.UndoHistory{}).Where("user_id = ? AND action = ?", user.ID, "undone").Count(&undoneCount)
	if undoneCount != 1 {
		t.Errorf("UndoHistory(undone) count = %d, want 1", undoneCount)
	}
}

// TestHandleUpdate_Correction_TargetsSpecificTransactionNotJustLatest
// reproduces the reported bug: after recording SEVERAL transactions (e.g. a
// batch from one itemized message), a correction referring to one of the
// earlier ones by category/amount used to always revert the most recent
// transaction instead — a chained-tool-calling AIClient is supposed to
// identify the actual target via get_recent_transactions and echo it back
// as ParsedTransaction.TargetTransactionID (see ai.ChainedCorrector,
// transaction.Service.CorrectLast). mockAIClient doesn't implement
// ChainedCorrector, so this exercises the same target-honoring code path
// bot.Service and transaction.Service run regardless of which AIClient
// method produced the TargetTransactionID.
func TestHandleUpdate_Correction_TargetsSpecificTransactionNotJustLatest(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	// Batch of 3, oldest to newest: bensin (300k), makan siang (300k), makan
	// malam (100k).
	env.ai.parseResults = []*ai.ParsedTransaction{
		{Type: "expense", Amount: 300_000, Currency: "IDR", CategoryName: "transport", Description: "bensin"},
		{Type: "expense", Amount: 300_000, Currency: "IDR", CategoryName: "food", Description: "makan siang"},
		{Type: "expense", Amount: 100_000, Currency: "IDR", CategoryName: "food", Description: "makan malam"},
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "bensin 300k, makan siang 300k, makan malam 100k"})
	env.ai.parseResults = nil

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	var lunch models.Transaction
	if err := env.db.Where("user_id = ? AND description = ?", user.ID, "makan siang").First(&lunch).Error; err != nil {
		t.Fatalf("load lunch transaction: %v", err)
	}

	// Correct the LUNCH transaction specifically (not the most recent,
	// "makan malam") by targeting its real ID, as a chained-tool-calling
	// provider's get_recent_transactions match would.
	env.ai.parseResult = &ai.ParsedTransaction{
		Intent: ai.IntentCorrect, Type: "expense", Amount: 350_000, Currency: "IDR", CategoryName: "food", Description: "makan siang",
		TargetTransactionID: lunch.ID,
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "eh yang makan siang tadi salah, harusnya 350rb"})

	got := env.messenger.last(chatID)
	wantAmount := i18n.FormatMoney(models.LanguageID, 350_000, "IDR")
	if !strings.Contains(got, wantAmount) {
		t.Errorf("reply = %q, want it to mention the corrected amount %s", got, wantAmount)
	}

	var activeCount int64
	env.db.Model(&models.Transaction{}).Where("user_id = ?", user.ID).Count(&activeCount)
	if activeCount != 3 {
		t.Fatalf("active transaction count = %d, want 3 (lunch replaced, the other two untouched)", activeCount)
	}

	// The dinner transaction (the most recent one before the correction)
	// must be untouched — this is exactly the bug being guarded against.
	var dinner models.Transaction
	if err := env.db.Where("user_id = ? AND description = ?", user.ID, "makan malam").First(&dinner).Error; err != nil {
		t.Fatalf("dinner transaction should still exist untouched: %v", err)
	}
	if dinner.ConvertedAmount != 100_000 {
		t.Errorf("dinner amount = %.2f, want unchanged 100000.00 (correction must not have touched it)", dinner.ConvertedAmount)
	}

	// The bensin transaction must also be untouched.
	var fuel models.Transaction
	if err := env.db.Where("user_id = ? AND description = ?", user.ID, "bensin").First(&fuel).Error; err != nil {
		t.Fatalf("fuel transaction should still exist untouched: %v", err)
	}

	// The lunch transaction must be soft-deleted and replaced by a new one
	// at the corrected amount.
	var deletedLunch models.Transaction
	if err := env.db.Unscoped().Where("id = ?", lunch.ID).First(&deletedLunch).Error; err != nil {
		t.Fatalf("load original lunch transaction: %v", err)
	}
	if !deletedLunch.DeletedAt.Valid {
		t.Errorf("original lunch transaction (id %d) should be soft-deleted, was not", lunch.ID)
	}

	var correctedLunch models.Transaction
	if err := env.db.Where("user_id = ? AND description = ? AND id != ?", user.ID, "makan siang", lunch.ID).First(&correctedLunch).Error; err != nil {
		t.Fatalf("load corrected lunch transaction: %v", err)
	}
	if correctedLunch.ConvertedAmount != 350_000 {
		t.Errorf("corrected lunch amount = %.2f, want 350000.00", correctedLunch.ConvertedAmount)
	}
}

// TestHandleUpdate_Deletion_TargetsSpecificTransactionNotJustLatest covers
// the new natural-language delete capability (ai.IntentDelete): "hapus
// transaksi X" must remove the specific transaction X refers to, not always
// the most recent one — the same targeting mechanism as correction (see
// ai.ChainedCorrector's get_recent_transactions,
// transaction.Service.DeleteTransaction). mockAIClient doesn't implement
// ChainedCorrector, so this exercises the target-honoring dispatch/service
// code directly, same as
// TestHandleUpdate_Correction_TargetsSpecificTransactionNotJustLatest.
func TestHandleUpdate_Deletion_TargetsSpecificTransactionNotJustLatest(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResults = []*ai.ParsedTransaction{
		{Type: "expense", Amount: 300_000, Currency: "IDR", CategoryName: "transport", Description: "bensin"},
		{Type: "expense", Amount: 300_000, Currency: "IDR", CategoryName: "food", Description: "makan siang"},
		{Type: "expense", Amount: 100_000, Currency: "IDR", CategoryName: "food", Description: "makan malam"},
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "bensin 300k, makan siang 300k, makan malam 100k"})
	env.ai.parseResults = nil

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	var fuel models.Transaction
	if err := env.db.Where("user_id = ? AND description = ?", user.ID, "bensin").First(&fuel).Error; err != nil {
		t.Fatalf("load fuel transaction: %v", err)
	}

	// Delete the FUEL transaction specifically (not the most recent, "makan
	// malam") by targeting its real ID, as a chained-tool-calling provider's
	// get_recent_transactions match would.
	env.ai.parseResult = &ai.ParsedTransaction{Intent: ai.IntentDelete, TargetTransactionID: fuel.ID}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "hapus transaksi bensin tadi"})

	got := env.messenger.last(chatID)
	wantAmount := i18n.FormatMoney(models.LanguageID, 300_000, "IDR")
	if !strings.Contains(got, wantAmount) {
		t.Errorf("reply = %q, want it to mention the deleted amount %s", got, wantAmount)
	}

	var activeCount int64
	env.db.Model(&models.Transaction{}).Where("user_id = ?", user.ID).Count(&activeCount)
	if activeCount != 2 {
		t.Fatalf("active transaction count = %d, want 2 (fuel deleted, lunch and dinner untouched)", activeCount)
	}

	var deletedFuel models.Transaction
	if err := env.db.Unscoped().Where("id = ?", fuel.ID).First(&deletedFuel).Error; err != nil {
		t.Fatalf("load fuel transaction: %v", err)
	}
	if !deletedFuel.DeletedAt.Valid {
		t.Errorf("targeted fuel transaction (id %d) should be soft-deleted, was not", fuel.ID)
	}

	// Lunch and dinner (neither targeted, and dinner is the actual most
	// recent one) must both still be active — this is exactly the bug
	// class being guarded against.
	for _, description := range []string{"makan siang", "makan malam"} {
		var tx models.Transaction
		if err := env.db.Where("user_id = ? AND description = ?", user.ID, description).First(&tx).Error; err != nil {
			t.Errorf("%s transaction should still exist untouched: %v", description, err)
		}
	}
}

// TestHandleUpdate_QuotaGating_BlocksWithoutReward is the core guardrail
// this test suite exists to protect: once a free-tier user has used their
// daily quota, the AI client must never be called again, and the user gets
// the no-reward exceeded message when no affiliate link is configured.
func TestHandleUpdate_QuotaGating_BlocksWithoutReward(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 1})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{Type: "expense", Amount: 1000, Currency: "IDR", CategoryName: "Food", Description: "x"}

	// First message consumes the only quota slot.
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "coffee 1000"})
	if got := env.ai.callCount(); got != 1 {
		t.Fatalf("AI calls after 1st message = %d, want 1", got)
	}

	// Second message should be blocked before ever reaching the AI client.
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "coffee 1000"})
	if got := env.ai.callCount(); got != 1 {
		t.Errorf("AI calls after quota exceeded = %d, want still 1 (blocked)", got)
	}

	want := i18n.T(models.LanguageID, i18n.KeyQuotaExceededNoReward, 1)
	if got := env.messenger.last(chatID); got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

// TestHandleUpdate_QuotaGating_OffersRewardWhenConfigured mirrors the
// blocked case above, but with an affiliate link configured: the user
// should be offered a claimable bonus-quota code instead of a flat "no".
func TestHandleUpdate_QuotaGating_OffersRewardWhenConfigured(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{
		FreeTierDailyQuota:    1,
		RewardAffiliateURL:    "https://example.com/aff",
		RewardBonusAmount:     3,
		RewardMaxClaimsPerDay: 1,
	})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{Type: "expense", Amount: 1000, Currency: "IDR", CategoryName: "Food", Description: "x"}

	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "coffee 1000"})
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "coffee 1000"})

	if got := env.ai.callCount(); got != 1 {
		t.Errorf("AI calls after quota exceeded = %d, want still 1 (blocked)", got)
	}

	got := env.messenger.last(chatID)
	if !strings.Contains(got, "https://example.com/aff") {
		t.Errorf("reply = %q, want it to contain the affiliate link", got)
	}

	// The claim code offered in the reply should actually be claimable.
	quotaCountBefore, err := quota.CountToday(ctx, env.redis, chatID)
	if err != nil {
		t.Fatalf("read quota count: %v", err)
	}
	if quotaCountBefore != 1 {
		t.Fatalf("quota count = %d, want 1 (blocked message doesn't consume quota)", quotaCountBefore)
	}
}

// TestHandleUpdate_QuotaGating_ProTierNeverBlocked confirms the Pro tier
// bypasses the daily cap entirely, regardless of how many prompts were
// already used today.
func TestHandleUpdate_QuotaGating_ProTierNeverBlocked(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 1})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{Type: "expense", Amount: 1000, Currency: "IDR", CategoryName: "Food", Description: "x"}

	// Seed the user as Pro tier before sending anything (get-or-create then update).
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/start"})
	if err := env.db.Model(&models.User{}).Where("telegram_chat_id = ?", chatID).Update("tier", models.TierPro).Error; err != nil {
		t.Fatalf("upgrade user to pro: %v", err)
	}

	for i := 0; i < 3; i++ {
		svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "coffee 1000"})
	}

	if got := env.ai.callCount(); got != 3 {
		t.Errorf("AI calls for pro-tier user = %d, want 3 (never blocked)", got)
	}
}

// TestRunBudgetRolloverNudges covers the first job registered on
// internal/scheduler (see cmd/server/main.go): a user with a real surplus
// from LAST calendar month gets proactively messaged once, and a second
// call within the same calendar month is a no-op (the idempotency guard
// keyed by user+month) — without that guard, a scheduler ticking daily
// would otherwise resend the same nudge every single day of the month.
func TestRunBudgetRolloverNudges(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	now := time.Now().UTC()
	lastMonth15th := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 14)
	lastMonthHint := lastMonth15th.Format("2006-01-02")

	// Income 2,000,000 and expense 500,000, both dated last calendar month
	// via OccurredAtHint -> a 1,500,000 surplus SuggestRollover should pick up.
	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "income", Amount: 2_000_000, Currency: "IDR", CategoryName: "salary", OccurredAtHint: lastMonthHint,
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "gaji bulan lalu 2000000"})

	env.ai.parseResult = &ai.ParsedTransaction{
		Type: "expense", Amount: 500_000, Currency: "IDR", CategoryName: "food", OccurredAtHint: lastMonthHint,
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "makan bulan lalu 500000"})

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	countBefore := env.messenger.count(chatID)

	// The idempotency key is global (not scoped to this test's chatID/user
	// pair alone in a way we can namespace away), so clean it up regardless
	// of which of this test's two RunBudgetRolloverNudges calls below ends
	// up setting it.
	monthKey := "budget-rollover:" + now.Format("2006-01")
	t.Cleanup(func() {
		env.redis.Del(ctx, fmt.Sprintf("aura:seen:%s:%d", monthKey, user.ID))
	})

	if err := svc.RunBudgetRolloverNudges(ctx); err != nil {
		t.Fatalf("RunBudgetRolloverNudges (first call): %v", err)
	}

	got := env.messenger.last(chatID)
	if !strings.Contains(got, "1500000.00") {
		t.Errorf("nudge reply = %q, want it to mention the 1,500,000 surplus", got)
	}
	if countAfterFirst := env.messenger.count(chatID); countAfterFirst != countBefore+1 {
		t.Fatalf("message count after first RunBudgetRolloverNudges = %d, want %d (exactly one nudge sent)", countAfterFirst, countBefore+1)
	}

	// A second call in the same calendar month must NOT send again — this
	// is the exact guarantee that makes it safe to call on every scheduler
	// tick instead of requiring the scheduler to know about calendar
	// boundaries.
	if err := svc.RunBudgetRolloverNudges(ctx); err != nil {
		t.Fatalf("RunBudgetRolloverNudges (second call): %v", err)
	}
	if countAfterSecond := env.messenger.count(chatID); countAfterSecond != countBefore+1 {
		t.Errorf("message count after a second same-month RunBudgetRolloverNudges = %d, want still %d (no duplicate nudge)", countAfterSecond, countBefore+1)
	}
}

// TestHandleUpdate_RecurringCreate_NaturalLanguage covers the new
// create_recurring_transaction intent end to end: a message describing a
// repeating transaction schedules a RecurringTransaction row instead of
// recording a Transaction right away, with NextRunAt in the future.
func TestHandleUpdate_RecurringCreate_NaturalLanguage(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent: ai.IntentRecurringCreate, Type: "expense", Amount: 150_000, Currency: "IDR",
		CategoryName: "subscription", Description: "netflix", DayOfMonth: 5,
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "netflix 150000 setiap tanggal 5"})

	got := env.messenger.last(chatID)
	wantAmount := i18n.FormatMoney(models.LanguageID, 150_000, "IDR")
	if !strings.Contains(got, wantAmount) {
		t.Errorf("reply = %q, want it to mention the amount %s", got, wantAmount)
	}

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}

	var txCount int64
	env.db.Model(&models.Transaction{}).Where("user_id = ?", user.ID).Count(&txCount)
	if txCount != 0 {
		t.Errorf("transaction count = %d, want 0 (creating a recurring schedule must not record anything right away)", txCount)
	}

	var rt models.RecurringTransaction
	if err := env.db.Where("user_id = ?", user.ID).First(&rt).Error; err != nil {
		t.Fatalf("load recurring transaction: %v", err)
	}
	if rt.DayOfMonth != 5 {
		t.Errorf("DayOfMonth = %d, want 5", rt.DayOfMonth)
	}
	if !rt.IsActive {
		t.Error("expected the newly created recurring transaction to be active")
	}
	if !rt.NextRunAt.After(time.Now().UTC()) {
		t.Errorf("NextRunAt = %v, want strictly in the future", rt.NextRunAt)
	}
}

// TestHandleUpdate_Recurring_ListAndCancel covers the /recurring command's
// two paths: listing shows an active recurring transaction with its real
// id, and canceling by that id removes it from the list (soft-deactivates,
// doesn't hard-delete) — with an empty-list reply once nothing is left.
func TestHandleUpdate_Recurring_ListAndCancel(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	// No recurring transactions yet.
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/recurring"})
	want := i18n.T(models.LanguageID, i18n.KeyRecurringListEmpty)
	if got := env.messenger.last(chatID); got != want {
		t.Errorf("empty list reply = %q, want %q", got, want)
	}

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent: ai.IntentRecurringCreate, Type: "expense", Amount: 800_000, Currency: "IDR",
		CategoryName: "rent", Description: "kos", DayOfMonth: 1,
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "sewa kos 800000 tiap tanggal 1"})

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	var rt models.RecurringTransaction
	if err := env.db.Where("user_id = ?", user.ID).First(&rt).Error; err != nil {
		t.Fatalf("load recurring transaction: %v", err)
	}

	// Listing now shows exactly this one, with its real id.
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/recurring"})
	listReply := env.messenger.last(chatID)
	wantAmount := i18n.FormatMoney(models.LanguageID, 800_000, "IDR")
	if !strings.Contains(listReply, wantAmount) || !strings.Contains(listReply, "rent") {
		t.Errorf("list reply = %q, want it to mention the amount %s and category %q", listReply, wantAmount, "rent")
	}

	// Cancel by a malformed id -> usage message, row untouched.
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/recurring", CommandArgs: "cancel notanumber"})
	wantUsage := i18n.T(models.LanguageID, i18n.KeyRecurringCancelUsage)
	if got := env.messenger.last(chatID); got != wantUsage {
		t.Errorf("malformed cancel reply = %q, want %q", got, wantUsage)
	}

	// Cancel a nonexistent id -> not-found message.
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/recurring", CommandArgs: "cancel 999999999"})
	wantNotFound := i18n.T(models.LanguageID, i18n.KeyRecurringCancelNotFound)
	if got := env.messenger.last(chatID); got != wantNotFound {
		t.Errorf("nonexistent cancel reply = %q, want %q", got, wantNotFound)
	}

	// Cancel the real one -> success, then the list is empty again.
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/recurring", CommandArgs: fmt.Sprintf("cancel %d", rt.ID)})
	cancelReply := env.messenger.last(chatID)
	if !strings.Contains(cancelReply, wantAmount) {
		t.Errorf("cancel success reply = %q, want it to mention the amount %s", cancelReply, wantAmount)
	}

	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/recurring"})
	if got := env.messenger.last(chatID); got != want {
		t.Errorf("list reply after canceling the only one = %q, want the empty-list message %q", got, want)
	}

	var stillActive int64
	env.db.Model(&models.RecurringTransaction{}).Where("id = ? AND is_active = ?", rt.ID, true).Count(&stillActive)
	if stillActive != 0 {
		t.Error("canceled recurring transaction should no longer be active")
	}
	var stillExists int64
	env.db.Model(&models.RecurringTransaction{}).Where("id = ?", rt.ID).Count(&stillExists)
	if stillExists != 1 {
		t.Error("canceling should deactivate the row, not hard-delete it")
	}
}

// TestRunRecurringTransactions_PostsWhenDueAndAdvancesSchedule covers the
// scheduler job end to end: a due recurring transaction gets posted as a
// real Transaction, the user is notified, and its schedule advances so the
// same tick never posts it twice.
func TestRunRecurringTransactions_PostsWhenDueAndAdvancesSchedule(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent: ai.IntentRecurringCreate, Type: "expense", Amount: 150_000, Currency: "IDR",
		CategoryName: "subscription", Description: "netflix", DayOfMonth: 5,
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "netflix 150000 setiap tanggal 5"})

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	var rt models.RecurringTransaction
	if err := env.db.Where("user_id = ?", user.ID).First(&rt).Error; err != nil {
		t.Fatalf("load recurring transaction: %v", err)
	}
	originalNextRunAt := rt.NextRunAt

	// Force it due right now without waiting for a real month boundary.
	past := time.Now().UTC().Add(-1 * time.Hour)
	if err := env.db.Model(&models.RecurringTransaction{}).Where("id = ?", rt.ID).Update("next_run_at", past).Error; err != nil {
		t.Fatalf("backdate NextRunAt: %v", err)
	}

	countBefore := env.messenger.count(chatID)
	if err := svc.RunRecurringTransactions(ctx); err != nil {
		t.Fatalf("RunRecurringTransactions: %v", err)
	}

	var txCount int64
	env.db.Model(&models.Transaction{}).Where("user_id = ?", user.ID).Count(&txCount)
	if txCount != 1 {
		t.Fatalf("transaction count after the job ran = %d, want 1 (the due recurring transaction posted)", txCount)
	}

	var postedTx models.Transaction
	if err := env.db.Where("user_id = ?", user.ID).First(&postedTx).Error; err != nil {
		t.Fatalf("load posted transaction: %v", err)
	}
	if postedTx.Source != models.SourceRecurring {
		t.Errorf("posted transaction Source = %q, want %q", postedTx.Source, models.SourceRecurring)
	}
	if postedTx.ConvertedAmount != 150_000 {
		t.Errorf("posted transaction amount = %.2f, want 150000.00", postedTx.ConvertedAmount)
	}

	if got := env.messenger.count(chatID); got != countBefore+1 {
		t.Fatalf("message count after the job ran = %d, want %d (user notified exactly once)", got, countBefore+1)
	}
	notifyReply := env.messenger.last(chatID)
	wantAmount := i18n.FormatMoney(models.LanguageID, 150_000, "IDR")
	if !strings.Contains(notifyReply, wantAmount) {
		t.Errorf("notification reply = %q, want it to mention the amount %s", notifyReply, wantAmount)
	}

	var rtAfter models.RecurringTransaction
	if err := env.db.First(&rtAfter, rt.ID).Error; err != nil {
		t.Fatalf("reload recurring transaction: %v", err)
	}
	wantNext := past.AddDate(0, 1, 0)
	if !rtAfter.NextRunAt.Equal(wantNext) {
		t.Errorf("NextRunAt after posting = %v, want exactly one month after the backdated value (%v)", rtAfter.NextRunAt, wantNext)
	}
	if rtAfter.NextRunAt.Equal(originalNextRunAt) {
		t.Error("NextRunAt should have moved forward from the backdated value, not stayed at the original schedule")
	}

	// A second run in the same tick-cycle must not post again — the
	// schedule already moved past "now".
	if err := svc.RunRecurringTransactions(ctx); err != nil {
		t.Fatalf("RunRecurringTransactions (second call): %v", err)
	}
	env.db.Model(&models.Transaction{}).Where("user_id = ?", user.ID).Count(&txCount)
	if txCount != 1 {
		t.Errorf("transaction count after a second RunRecurringTransactions call = %d, want still 1 (no duplicate posting)", txCount)
	}
}

// TestHandleUpdate_BillReminderCreate_NaturalLanguage covers the
// create_bill_reminder intent: a message asking to be REMINDED about a bill
// (not auto-recorded) schedules a RecurringTransaction with ReminderOnly
// set, and the confirmation reply is the reminder-specific one, not the
// auto-record one.
func TestHandleUpdate_BillReminderCreate_NaturalLanguage(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent: ai.IntentRecurringCreate, Type: "expense", Amount: 500_000, Currency: "IDR",
		CategoryName: "utilities", Description: "listrik", DayOfMonth: 5, ReminderOnly: true,
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "ingetin bayar listrik 500000 tiap tanggal 5"})

	got := env.messenger.last(chatID)
	wantAmount := i18n.FormatMoney(models.LanguageID, 500_000, "IDR")
	// The confirmation shows the user's own description ("listrik") rather
	// than the generic CategoryName ("utilities") whenever one was parsed —
	// see recurringLabel's doc comment.
	if !strings.Contains(got, wantAmount) || !strings.Contains(got, "listrik") {
		t.Errorf("reply = %q, want it to mention the amount %s and description", got, wantAmount)
	}
	// "kabari aku kalau sudah dibayar" only appears in KeyBillReminderCreated
	// (the reminder confirmation), never in KeyRecurringCreated (the
	// auto-record confirmation) — asserting on it catches the wrong branch
	// firing.
	if !strings.Contains(got, "kabari aku kalau sudah dibayar") {
		t.Errorf("reply = %q, want the bill-reminder confirmation (KeyBillReminderCreated), not the auto-record one", got)
	}

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	var rt models.RecurringTransaction
	if err := env.db.Where("user_id = ?", user.ID).First(&rt).Error; err != nil {
		t.Fatalf("load recurring transaction: %v", err)
	}
	if !rt.ReminderOnly {
		t.Error("expected ReminderOnly = true for a bill reminder created via create_bill_reminder")
	}

	// Listing should flag it as a reminder.
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/recurring"})
	listReply := env.messenger.last(chatID)
	wantModeSuffix := i18n.T(models.LanguageID, i18n.KeyRecurringModeReminder)
	if !strings.Contains(listReply, wantModeSuffix) {
		t.Errorf("list reply = %q, want it to mention %q for a reminder-only row", listReply, wantModeSuffix)
	}
}

// TestRunRecurringTransactions_ReminderOnlyNeverPostsATransaction covers
// the other half of RunRecurringTransactions' branch: a due bill reminder
// notifies the user and advances its own schedule exactly like an
// auto-posting recurring transaction does, but must NEVER create a
// Transaction row — the user is expected to log the actual payment
// themselves.
func TestRunRecurringTransactions_ReminderOnlyNeverPostsATransaction(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent: ai.IntentRecurringCreate, Type: "expense", Amount: 500_000, Currency: "IDR",
		CategoryName: "utilities", Description: "listrik", DayOfMonth: 5, ReminderOnly: true,
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "ingetin bayar listrik 500000 tiap tanggal 5"})

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	var rt models.RecurringTransaction
	if err := env.db.Where("user_id = ?", user.ID).First(&rt).Error; err != nil {
		t.Fatalf("load recurring transaction: %v", err)
	}

	past := time.Now().UTC().Add(-1 * time.Hour)
	if err := env.db.Model(&models.RecurringTransaction{}).Where("id = ?", rt.ID).Update("next_run_at", past).Error; err != nil {
		t.Fatalf("backdate NextRunAt: %v", err)
	}

	countBefore := env.messenger.count(chatID)
	if err := svc.RunRecurringTransactions(ctx); err != nil {
		t.Fatalf("RunRecurringTransactions: %v", err)
	}

	var txCount int64
	env.db.Model(&models.Transaction{}).Where("user_id = ?", user.ID).Count(&txCount)
	if txCount != 0 {
		t.Errorf("transaction count after a due bill reminder fired = %d, want 0 (a reminder must never auto-record)", txCount)
	}

	if got := env.messenger.count(chatID); got != countBefore+1 {
		t.Fatalf("message count after the job ran = %d, want %d (user notified exactly once)", got, countBefore+1)
	}
	notifyReply := env.messenger.last(chatID)
	wantAmount := i18n.FormatMoney(models.LanguageID, 500_000, "IDR")
	if !strings.Contains(notifyReply, wantAmount) {
		t.Errorf("reminder notification = %q, want it to mention the amount %s", notifyReply, wantAmount)
	}

	var rtAfter models.RecurringTransaction
	if err := env.db.First(&rtAfter, rt.ID).Error; err != nil {
		t.Fatalf("reload recurring transaction: %v", err)
	}
	wantNext := past.AddDate(0, 1, 0)
	if !rtAfter.NextRunAt.Equal(wantNext) {
		t.Errorf("NextRunAt after the reminder fired = %v, want exactly one month after the backdated value (%v) — the schedule must still advance even though nothing was recorded", rtAfter.NextRunAt, wantNext)
	}
}

// TestRunCategoryInsights covers the third scheduler job (see
// cmd/server/main.go): a user with real category spending from LAST
// calendar month gets a proactive recap — top categories, a side-by-side
// comparison for whichever category grew the most since the month before,
// and an AI-generated suggestion appended — exactly once, with a second
// same-month call being a no-op (same idempotency guard as
// RunBudgetRolloverNudges/RunRecurringTransactions).
func TestRunCategoryInsights(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	now := time.Now().UTC()
	lastMonthHint := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 9).Format("2006-01-02")
	priorMonthHint := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -2, 9).Format("2006-01-02")

	// Last month: food 1,200,000 and entertainment 600,000. Prior month:
	// entertainment 200,000 -> entertainment tripled (+200%), food is a
	// brand-new category with no baseline.
	env.ai.parseResult = &ai.ParsedTransaction{Type: "expense", Amount: 1_200_000, Currency: "IDR", CategoryName: "food", OccurredAtHint: lastMonthHint}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "makan bulan lalu 1200000"})

	env.ai.parseResult = &ai.ParsedTransaction{Type: "expense", Amount: 600_000, Currency: "IDR", CategoryName: "entertainment", OccurredAtHint: lastMonthHint}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "nonton bulan lalu 600000"})

	env.ai.parseResult = &ai.ParsedTransaction{Type: "expense", Amount: 200_000, Currency: "IDR", CategoryName: "entertainment", OccurredAtHint: priorMonthHint}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "nonton dua bulan lalu 200000"})

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	countBefore := env.messenger.count(chatID)

	monthKey := "category-insight:" + now.Format("2006-01")
	t.Cleanup(func() {
		env.redis.Del(ctx, fmt.Sprintf("aura:seen:%s:%d", monthKey, user.ID))
	})

	if err := svc.RunCategoryInsights(ctx); err != nil {
		t.Fatalf("RunCategoryInsights (first call): %v", err)
	}

	got := env.messenger.last(chatID)
	wantTotal := i18n.FormatMoney(models.LanguageID, 1_800_000, "IDR")
	if !strings.Contains(got, wantTotal) {
		t.Errorf("reply = %q, want it to mention the total %s", got, wantTotal)
	}
	if !strings.Contains(got, "food") || !strings.Contains(got, "entertainment") {
		t.Errorf("reply = %q, want it to list both categories", got)
	}
	wantLastMonthAmount := i18n.FormatMoney(models.LanguageID, 600_000, "IDR")
	wantPriorMonthAmount := i18n.FormatMoney(models.LanguageID, 200_000, "IDR")
	if !strings.Contains(got, wantLastMonthAmount) || !strings.Contains(got, wantPriorMonthAmount) {
		t.Errorf("reply = %q, want it to show BOTH months' actual amounts for the increased category (%s and %s), not just the percentage", got, wantLastMonthAmount, wantPriorMonthAmount)
	}
	if !strings.Contains(got, "200") {
		t.Errorf("reply = %q, want it to mention the 200%% increase", got)
	}
	if !strings.Contains(got, mockSavingsSuggestion) {
		t.Errorf("reply = %q, want it to include the AI-generated suggestion", got)
	}
	if countAfterFirst := env.messenger.count(chatID); countAfterFirst != countBefore+1 {
		t.Fatalf("message count after first RunCategoryInsights = %d, want %d (exactly one recap sent)", countAfterFirst, countBefore+1)
	}

	// A second call in the same calendar month must NOT send again.
	if err := svc.RunCategoryInsights(ctx); err != nil {
		t.Fatalf("RunCategoryInsights (second call): %v", err)
	}
	if countAfterSecond := env.messenger.count(chatID); countAfterSecond != countBefore+1 {
		t.Errorf("message count after a second same-month RunCategoryInsights = %d, want still %d (no duplicate recap)", countAfterSecond, countBefore+1)
	}
}

// TestRunCategoryInsights_AIFailureStillSendsDeterministicPart covers the
// resilience contract: if SuggestSavings errors out, the factual part of
// the recap (which needs no AI call at all) must still send — a savings
// tip is a nice-to-have, not something worth dropping the whole recap over.
func TestRunCategoryInsights_AIFailureStillSendsDeterministicPart(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	now := time.Now().UTC()
	lastMonthHint := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 9).Format("2006-01-02")

	env.ai.parseResult = &ai.ParsedTransaction{Type: "expense", Amount: 500_000, Currency: "IDR", CategoryName: "food", OccurredAtHint: lastMonthHint}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "makan bulan lalu 500000"})
	env.ai.suggestSavingsErr = errors.New("simulated AI outage")

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	monthKey := "category-insight:" + now.Format("2006-01")
	t.Cleanup(func() {
		env.redis.Del(ctx, fmt.Sprintf("aura:seen:%s:%d", monthKey, user.ID))
	})

	if err := svc.RunCategoryInsights(ctx); err != nil {
		t.Fatalf("RunCategoryInsights: %v", err)
	}

	got := env.messenger.last(chatID)
	wantAmount := i18n.FormatMoney(models.LanguageID, 500_000, "IDR")
	if !strings.Contains(got, wantAmount) {
		t.Errorf("reply = %q, want the deterministic recap to still send despite the AI failure", got)
	}
	if strings.Contains(got, mockSavingsSuggestion) {
		t.Errorf("reply = %q, want no suggestion line when SuggestSavings failed", got)
	}
}

// TestHandleUpdate_SavingsGoal_CreateProgressAndReach covers the new
// create_savings_goal intent end to end: natural-language creation, /goal
// showing progress computed from real savings/investment activity, and the
// goal-reached message once enough has been saved.
func TestHandleUpdate_SavingsGoal_CreateProgressAndReach(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	// No goal yet -> /goal reports none.
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/goal"})
	wantNone := i18n.T(models.LanguageID, i18n.KeyGoalNone)
	if got := env.messenger.last(chatID); got != wantNone {
		t.Errorf("no-goal reply = %q, want %q", got, wantNone)
	}

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent: ai.IntentSavingsGoalCreate, GoalName: "laptop baru", Amount: 10_000_000, Currency: "IDR",
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "aku mau nabung 10 juta buat laptop baru"})

	createReply := env.messenger.last(chatID)
	wantTarget := i18n.FormatMoney(models.LanguageID, 10_000_000, "IDR")
	if !strings.Contains(createReply, wantTarget) || !strings.Contains(createReply, "laptop baru") {
		t.Errorf("create reply = %q, want it to mention the target %s and name", createReply, wantTarget)
	}

	// Progress with nothing saved yet -> 0%.
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/goal"})
	progressReply := env.messenger.last(chatID)
	if !strings.Contains(progressReply, "laptop baru") || !strings.Contains(progressReply, "0%") {
		t.Errorf("progress reply = %q, want it to mention the goal name and 0%%", progressReply)
	}

	// Record an investment (counts as savings) covering half the target.
	env.ai.parseResult = &ai.ParsedTransaction{Type: "investment", Amount: 5_000_000, Currency: "IDR", CategoryName: "mutual fund"}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "invest 5000000"})

	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/goal"})
	halfwayReply := env.messenger.last(chatID)
	wantSaved := i18n.FormatMoney(models.LanguageID, 5_000_000, "IDR")
	if !strings.Contains(halfwayReply, wantSaved) || !strings.Contains(halfwayReply, "50%") {
		t.Errorf("halfway progress reply = %q, want it to mention %s saved and 50%%", halfwayReply, wantSaved)
	}

	// Record another investment reaching the full target.
	env.ai.parseResult = &ai.ParsedTransaction{Type: "investment", Amount: 5_000_000, Currency: "IDR", CategoryName: "mutual fund"}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "invest 5000000 lagi"})

	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/goal"})
	reachedReply := env.messenger.last(chatID)
	wantReached := i18n.T(models.LanguageID, i18n.KeyGoalReached)
	if !strings.Contains(reachedReply, wantReached) {
		t.Errorf("reached reply = %q, want it to include %q", reachedReply, wantReached)
	}
}

// TestHandleUpdate_SavingsGoal_ReplaceAndCancel covers creating a second
// goal (replacing the first, not stacking) and /goal cancel.
func TestHandleUpdate_SavingsGoal_ReplaceAndCancel(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{Intent: ai.IntentSavingsGoalCreate, GoalName: "laptop", Amount: 10_000_000, Currency: "IDR"}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "nabung 10 juta buat laptop"})

	env.ai.parseResult = &ai.ParsedTransaction{Intent: ai.IntentSavingsGoalCreate, GoalName: "liburan", Amount: 3_000_000, Currency: "IDR"}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "ganti rencana, nabung 3 juta buat liburan"})

	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/goal"})
	progressReply := env.messenger.last(chatID)
	if !strings.Contains(progressReply, "liburan") || strings.Contains(progressReply, "laptop") {
		t.Errorf("progress reply = %q, want only the replacement goal (liburan), not the original (laptop)", progressReply)
	}

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	var activeCount int64
	env.db.Model(&models.SavingsGoal{}).Where("user_id = ? AND is_active = ?", user.ID, true).Count(&activeCount)
	if activeCount != 1 {
		t.Errorf("active goal count = %d, want exactly 1", activeCount)
	}

	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/goal", CommandArgs: "cancel"})
	cancelReply := env.messenger.last(chatID)
	if !strings.Contains(cancelReply, "liburan") {
		t.Errorf("cancel reply = %q, want it to name the canceled goal", cancelReply)
	}

	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Command: "/goal"})
	wantNone := i18n.T(models.LanguageID, i18n.KeyGoalNone)
	if got := env.messenger.last(chatID); got != wantNone {
		t.Errorf("post-cancel progress reply = %q, want %q", got, wantNone)
	}
}

// TestHandleUpdate_RecurringDelete_NaturalLanguage covers the new
// delete_recurring_transaction intent (ai.IntentRecurringDelete): a natural-
// language cancellation targeting a SPECIFIC recurring transaction by id
// (as a chained-tool-calling provider's get_recurring_transactions match
// would supply) must cancel exactly that one, leaving a second active
// recurring transaction untouched — mockAIClient doesn't implement
// ChainedCorrector, so this exercises the same target-honoring
// dispatch/service code regardless of which AIClient method produced the
// TargetRecurringTransactionID, mirroring
// TestHandleUpdate_Correction_TargetsSpecificTransactionNotJustLatest.
func TestHandleUpdate_RecurringDelete_NaturalLanguage(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent: ai.IntentRecurringCreate, Type: "expense", Amount: 150_000, Currency: "IDR",
		CategoryName: "entertainment", Description: "netflix", DayOfMonth: 5,
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "netflix 150000 setiap tanggal 5"})

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent: ai.IntentRecurringCreate, Type: "expense", Amount: 800_000, Currency: "IDR",
		CategoryName: "rent", Description: "sewa kos", DayOfMonth: 1,
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "sewa kos 800000 tiap tanggal 1"})

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	var netflix models.RecurringTransaction
	if err := env.db.Where("user_id = ? AND description = ?", user.ID, "netflix").First(&netflix).Error; err != nil {
		t.Fatalf("load netflix recurring transaction: %v", err)
	}

	// Cancel by natural language, targeting netflix specifically (not the
	// most recently created one, "sewa kos").
	env.ai.parseResult = &ai.ParsedTransaction{Intent: ai.IntentRecurringDelete, TargetRecurringTransactionID: netflix.ID}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "batalin netflix"})

	got := env.messenger.last(chatID)
	wantAmount := i18n.FormatMoney(models.LanguageID, 150_000, "IDR")
	if !strings.Contains(got, wantAmount) {
		t.Errorf("reply = %q, want it to mention the canceled amount %s", got, wantAmount)
	}

	var netflixAfter models.RecurringTransaction
	if err := env.db.First(&netflixAfter, netflix.ID).Error; err != nil {
		t.Fatalf("reload netflix recurring transaction: %v", err)
	}
	if netflixAfter.IsActive {
		t.Error("netflix recurring transaction should be deactivated after natural-language cancellation")
	}

	var rentCount int64
	env.db.Model(&models.RecurringTransaction{}).Where("user_id = ? AND description = ? AND is_active = ?", user.ID, "sewa kos", true).Count(&rentCount)
	if rentCount != 1 {
		t.Error("the rent recurring transaction should still be active — this is exactly the bug class being guarded against")
	}

	// Canceling an id that doesn't belong to this user (or doesn't exist)
	// must report "not found", not silently succeed.
	env.ai.parseResult = &ai.ParsedTransaction{Intent: ai.IntentRecurringDelete, TargetRecurringTransactionID: 999_999_999}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "batalin yang gak ada"})
	wantNotFound := i18n.T(models.LanguageID, i18n.KeyRecurringCancelNotFound)
	if got := env.messenger.last(chatID); got != wantNotFound {
		t.Errorf("nonexistent-id cancel reply = %q, want %q", got, wantNotFound)
	}
}

// TestHandleUpdate_RecurringUpdate_DeleteAndCreatePair covers the "update"
// pattern: there's no dedicated update tool, so a chained-tool-calling
// provider represents "ubah netflix jadi 200rb" as BOTH an
// IntentRecurringDelete and an IntentRecurringCreate result from the SAME
// message (confirmed live against the real Gemini API). bot.Service must
// treat that pair as one atomic update — cancel the old schedule AND
// create the new one — rather than only looking at the first result and
// silently dropping the second (which would cancel netflix with nothing to
// replace it). A second, unrelated recurring transaction must stay
// untouched throughout.
func TestHandleUpdate_RecurringUpdate_DeleteAndCreatePair(t *testing.T) {
	svc, env := setupTestEnv(t, bot.Config{FreeTierDailyQuota: 5})
	chatID := newChatID()
	t.Cleanup(func() { cleanupChat(t, env, chatID) })
	ctx := context.Background()

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent: ai.IntentRecurringCreate, Type: "expense", Amount: 150_000, Currency: "IDR",
		CategoryName: "entertainment", Description: "netflix", DayOfMonth: 5,
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "netflix 150000 setiap tanggal 5"})

	env.ai.parseResult = &ai.ParsedTransaction{
		Intent: ai.IntentRecurringCreate, Type: "expense", Amount: 800_000, Currency: "IDR",
		CategoryName: "rent", Description: "sewa kos", DayOfMonth: 1,
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "sewa kos 800000 tiap tanggal 1"})

	var user models.User
	if err := env.db.Where("telegram_chat_id = ?", chatID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	var netflix models.RecurringTransaction
	if err := env.db.Where("user_id = ? AND description = ?", user.ID, "netflix").First(&netflix).Error; err != nil {
		t.Fatalf("load netflix recurring transaction: %v", err)
	}

	// Simulate the chained-tool-calling response to "ubah netflix jadi
	// 200rb": both a delete (targeting netflix's real id) and a create
	// (describing the same schedule with the new amount) in one result set.
	env.ai.parseResults = []*ai.ParsedTransaction{
		{Intent: ai.IntentRecurringDelete, TargetRecurringTransactionID: netflix.ID},
		{Intent: ai.IntentRecurringCreate, Type: "expense", Amount: 200_000, Currency: "IDR", CategoryName: "entertainment", Description: "netflix", DayOfMonth: 5},
	}
	svc.HandleUpdate(ctx, &messaging.IncomingMessage{ChatID: chatID, Text: "ubah netflix jadi 200rb"})
	env.ai.parseResults = nil

	got := env.messenger.last(chatID)
	wantNewAmount := i18n.FormatMoney(models.LanguageID, 200_000, "IDR")
	wantOldAmount := i18n.FormatMoney(models.LanguageID, 150_000, "IDR")
	if !strings.Contains(got, wantNewAmount) {
		t.Errorf("update reply = %q, want it to mention the new amount %s", got, wantNewAmount)
	}
	if !strings.Contains(got, wantOldAmount) {
		t.Errorf("update reply = %q, want it to mention the previous amount %s for contrast", got, wantOldAmount)
	}

	var netflixOld models.RecurringTransaction
	if err := env.db.First(&netflixOld, netflix.ID).Error; err != nil {
		t.Fatalf("reload original netflix row: %v", err)
	}
	if netflixOld.IsActive {
		t.Error("the original netflix recurring transaction should be deactivated after an update, not left active")
	}

	var activeNetflix models.RecurringTransaction
	if err := env.db.Where("user_id = ? AND description = ? AND is_active = ?", user.ID, "netflix", true).First(&activeNetflix).Error; err != nil {
		t.Fatalf("load the new active netflix recurring transaction: %v", err)
	}
	if activeNetflix.Amount != 200_000 {
		t.Errorf("new active netflix amount = %.2f, want 200000.00", activeNetflix.Amount)
	}
	if activeNetflix.ID == netflix.ID {
		t.Error("the update should have created a NEW row, not reactivated/mutated the old one")
	}

	var rentCount int64
	env.db.Model(&models.RecurringTransaction{}).Where("user_id = ? AND description = ? AND is_active = ?", user.ID, "sewa kos", true).Count(&rentCount)
	if rentCount != 1 {
		t.Error("the unrelated rent recurring transaction should still be active and untouched by netflix's update")
	}
}
