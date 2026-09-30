package conversation_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"

	"aura-saku/internal/ai"
	"aura-saku/internal/config"
	"aura-saku/internal/db"
	"aura-saku/internal/models"
	"aura-saku/internal/service/conversation"
)

// stubAIClient only needs to satisfy ai.AIClient for conversation.NewService
// to compile — every test here exercises Summarize (the only method this
// package actually calls), stubbed to return a fixed, inspectable summary.
type stubAIClient struct{ summarizeCalls int }

func (s *stubAIClient) ParseTransactionText(ctx context.Context, userText, defaultCurrency, preferredLanguage string, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	return nil, nil, fmt.Errorf("not used in this package's tests")
}
func (s *stubAIClient) TranscribeAndParseAudio(ctx context.Context, audioBytes []byte, mimeType, defaultCurrency, preferredLanguage string, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	return nil, nil, fmt.Errorf("not used in this package's tests")
}
func (s *stubAIClient) AnalyzeReceiptImage(ctx context.Context, imageBytes []byte, mimeType, defaultCurrency, preferredLanguage, caption string, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	return nil, nil, fmt.Errorf("not used in this package's tests")
}
func (s *stubAIClient) AnswerClarification(ctx context.Context, originalText, clarificationReply, defaultCurrency, preferredLanguage string, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	return nil, nil, fmt.Errorf("not used in this package's tests")
}
func (s *stubAIClient) ProviderName() string { return "stub" }
func (s *stubAIClient) Summarize(ctx context.Context, priorSummary string, turns []ai.ConversationTurn, preferredLanguage string) (string, *ai.UsageMetadata, error) {
	s.summarizeCalls++
	return fmt.Sprintf("summary covering %d turns (prior: %q)", len(turns), priorSummary), &ai.UsageMetadata{Model: "stub"}, nil
}
func (s *stubAIClient) SuggestSavings(ctx context.Context, spendingSummary string, preferredLanguage string) (string, *ai.UsageMetadata, error) {
	return "stub savings suggestion", &ai.UsageMetadata{Model: "stub"}, nil
}

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("skipping integration test, config load failed: %v", err)
	}
	gormDB, err := db.NewPostgres(cfg)
	if err != nil {
		t.Skipf("skipping integration test, postgres not reachable: %v", err)
	}
	if err := db.RunMigrations(gormDB); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return gormDB
}

// newTestUser inserts a minimal real User row (Conversation.UserID has a
// not-null FK-like relationship in practice) and returns its ID, cleaning
// up everything it touched afterward.
func newTestUser(t *testing.T, gormDB *gorm.DB) uint {
	t.Helper()
	user := models.User{TelegramChatID: time.Now().UnixNano(), DefaultCurrency: "IDR", Language: models.LanguageID, Tier: models.TierFree}
	if err := gormDB.Create(&user).Error; err != nil {
		t.Fatalf("create test user: %v", err)
	}
	t.Cleanup(func() {
		var conv models.Conversation
		if err := gormDB.Where("user_id = ?", user.ID).First(&conv).Error; err == nil {
			gormDB.Unscoped().Where("conversation_id = ?", conv.ID).Delete(&models.Message{})
			gormDB.Unscoped().Delete(&conv)
		}
		gormDB.Unscoped().Delete(&user)
	})
	return user.ID
}

func TestGetOrCreate_IsIdempotentPerUser(t *testing.T) {
	gormDB := setupTestDB(t)
	userID := newTestUser(t, gormDB)
	svc := conversation.NewService(gormDB, &stubAIClient{}, conversation.Config{RecentLimit: 10, SummarizeThreshold: 20})

	first, err := svc.GetOrCreate(context.Background(), userID)
	if err != nil {
		t.Fatalf("first GetOrCreate: %v", err)
	}
	second, err := svc.GetOrCreate(context.Background(), userID)
	if err != nil {
		t.Fatalf("second GetOrCreate: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("GetOrCreate returned different conversation IDs (%d, %d) for the same user — one continuous conversation per user is the whole point", first.ID, second.ID)
	}
}

func TestBuildContext_ReturnsRecentMessagesInChronologicalOrder(t *testing.T) {
	gormDB := setupTestDB(t)
	userID := newTestUser(t, gormDB)
	svc := conversation.NewService(gormDB, &stubAIClient{}, conversation.Config{RecentLimit: 3, SummarizeThreshold: 100})
	ctx := context.Background()

	conv, err := svc.GetOrCreate(ctx, userID)
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	// Insert 5 messages; RecentLimit=3 should return only the newest 3,
	// oldest-first.
	turns := []struct {
		role    models.MessageRole
		content string
	}{
		{models.MessageRoleUser, "msg1"},
		{models.MessageRoleAssistant, "reply1"},
		{models.MessageRoleUser, "msg2"},
		{models.MessageRoleAssistant, "reply2"},
		{models.MessageRoleUser, "msg3"},
	}
	for _, tt := range turns {
		if err := svc.AppendMessage(ctx, conv.ID, tt.role, tt.content); err != nil {
			t.Fatalf("AppendMessage(%q): %v", tt.content, err)
		}
	}

	got, err := svc.BuildContext(ctx, conv)
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}
	if len(got.Recent) != 3 {
		t.Fatalf("len(Recent) = %d, want 3 (RecentLimit)", len(got.Recent))
	}
	wantContents := []string{"msg2", "reply2", "msg3"}
	for i, want := range wantContents {
		if got.Recent[i].Content != want {
			t.Errorf("Recent[%d].Content = %q, want %q (chronological order)", i, got.Recent[i].Content, want)
		}
	}
	if got.Summary != "" {
		t.Errorf("Summary = %q, want empty (nothing summarized yet)", got.Summary)
	}
}

func TestMaybeSummarize_FoldsOldestMessagesOnceThresholdCrossed(t *testing.T) {
	gormDB := setupTestDB(t)
	userID := newTestUser(t, gormDB)
	stub := &stubAIClient{}
	// RecentLimit=2, SummarizeThreshold=4: once more than 4 unsummarized
	// messages exist, fold the oldest (count-2) into Summary, keeping the
	// newest 2 raw.
	svc := conversation.NewService(gormDB, stub, conversation.Config{RecentLimit: 2, SummarizeThreshold: 4})
	ctx := context.Background()

	conv, err := svc.GetOrCreate(ctx, userID)
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	for i := 1; i <= 4; i++ {
		if err := svc.AppendMessage(ctx, conv.ID, models.MessageRoleUser, fmt.Sprintf("msg%d", i)); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}
	if err := svc.MaybeSummarize(ctx, conv, "id"); err != nil {
		t.Fatalf("MaybeSummarize (at threshold, should be a no-op): %v", err)
	}
	if stub.summarizeCalls != 0 {
		t.Errorf("Summarize was called %d times at exactly the threshold, want 0 (threshold is exclusive)", stub.summarizeCalls)
	}

	// Cross the threshold: a 5th message makes 5 > SummarizeThreshold(4).
	if err := svc.AppendMessage(ctx, conv.ID, models.MessageRoleUser, "msg5"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if err := svc.MaybeSummarize(ctx, conv, "id"); err != nil {
		t.Fatalf("MaybeSummarize (past threshold): %v", err)
	}
	if stub.summarizeCalls != 1 {
		t.Fatalf("Summarize was called %d times past the threshold, want 1", stub.summarizeCalls)
	}

	reloaded, err := svc.GetOrCreate(ctx, userID)
	if err != nil {
		t.Fatalf("reload conversation: %v", err)
	}
	if reloaded.Summary == "" {
		t.Error("Conversation.Summary is empty after MaybeSummarize crossed the threshold")
	}
	if reloaded.SummarizedThroughMessageID == nil {
		t.Fatal("SummarizedThroughMessageID is nil after summarizing")
	}

	got, err := svc.BuildContext(ctx, conv)
	if err != nil {
		t.Fatalf("BuildContext after summarize: %v", err)
	}
	if len(got.Recent) != 2 {
		t.Errorf("len(Recent) after summarize = %d, want 2 (RecentLimit) — the rest should have moved into Summary", len(got.Recent))
	}
	if got.Recent[len(got.Recent)-1].Content != "msg5" {
		t.Errorf("newest Recent message = %q, want %q", got.Recent[len(got.Recent)-1].Content, "msg5")
	}
	if got.Summary == "" {
		t.Error("BuildContext returned an empty Summary after summarization")
	}
}
