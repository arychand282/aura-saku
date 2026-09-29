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
	"aura-saku/internal/service/reconciliation"
	"aura-saku/internal/service/recurring"
	"aura-saku/internal/service/report"
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

func (m *mockMessenger) DownloadFile(ctx context.Context, fileID string) ([]byte, string, error) {
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

func (m *mockAIClient) AnswerClarification(ctx context.Context, originalText, clarificationReply, defaultCurrency, preferredLanguage string, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	return m.call(history)
}

// Summarize is a trivial stand-in — no test currently asserts on summary
// content, only that MaybeSummarize's threshold logic fires (or doesn't)
// at the right message count.
func (m *mockAIClient) Summarize(ctx context.Context, priorSummary string, turns []ai.ConversationTurn, preferredLanguage string) (string, *ai.UsageMetadata, error) {
	return "mock summary", &ai.UsageMetadata{Model: "mock-model"}, nil
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

	svc := bot.NewService(gormDB, redisClient, messenger, aiClient, txSvc, budgetSvc, reconSvc, usageSvc, reportSvc, convSvc, recurSvc, cfg)

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
		env.db.Unscoped().Where("user_id = ?", user.ID).Delete(&models.Budget{})
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
