package i18n

import (
	"strings"
	"testing"

	"aura-saku/internal/models"
)

func TestT_RendersBothLanguages(t *testing.T) {
	id := T(models.LanguageID, KeyUndoNone)
	if !strings.Contains(id, "belum punya transaksi") {
		t.Fatalf("expected Indonesian undo_none message, got: %q", id)
	}

	en := T(models.LanguageEN, KeyUndoNone)
	if !strings.Contains(en, "don't have any transactions") {
		t.Fatalf("expected English undo_none message, got: %q", en)
	}
}

func TestT_FormatsArgs(t *testing.T) {
	got := T(models.LanguageID, KeySyncMatched, 1500000.0)
	want := "Saldo cocok — sesuai dengan catatan sebesar 1500000.00."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestT_FallsBackToEnglishForMissingKey(t *testing.T) {
	got := T(models.Language("fr"), KeyWelcome)
	if !strings.Contains(got, "Welcome to Aura-Saku") {
		t.Fatalf("expected English fallback for unsupported language, got: %q", got)
	}
}

func TestFormatMoney(t *testing.T) {
	cases := []struct {
		name     string
		lang     models.Language
		amount   float64
		currency string
		want     string
	}{
		{"ID IDR uses Rp prefix and dot/comma grouping", models.LanguageID, 2_950_000, "IDR", "Rp2.950.000,00"},
		{"ID non-IDR keeps currency code but still ID-groups the number", models.LanguageID, 2_950_000, "USD", "2.950.000,00 USD"},
		{"ID small amount under 1000 has no grouping separator", models.LanguageID, 500, "IDR", "Rp500,00"},
		{"ID negative amount keeps the sign in front of Rp", models.LanguageID, -25_000, "IDR", "-Rp25.000,00"},
		{"ID zero", models.LanguageID, 0, "IDR", "Rp0,00"},
		{"ID rounds to 2 decimals", models.LanguageID, 1234.567, "IDR", "Rp1.234,57"},
		{"EN keeps the original plain format, unaffected by this change", models.LanguageEN, 2_950_000, "IDR", "2950000.00 IDR"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FormatMoney(c.lang, c.amount, c.currency); got != c.want {
				t.Errorf("FormatMoney(%v, %v, %q) = %q, want %q", c.lang, c.amount, c.currency, got, c.want)
			}
		})
	}
}

func TestDetectFromTelegramCode(t *testing.T) {
	cases := map[string]models.Language{
		"":      models.LanguageID,
		"id":    models.LanguageID,
		"id-ID": models.LanguageID,
		"en":    models.LanguageEN,
		"en-US": models.LanguageEN,
		"fr":    models.LanguageEN,
		"ja":    models.LanguageEN,
	}
	for code, want := range cases {
		if got := DetectFromTelegramCode(code); got != want {
			t.Errorf("DetectFromTelegramCode(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestParseCommandInput(t *testing.T) {
	if lang, ok := ParseCommandInput("id"); !ok || lang != models.LanguageID {
		t.Errorf("ParseCommandInput(id) = %q, %v", lang, ok)
	}
	if lang, ok := ParseCommandInput("EN"); !ok || lang != models.LanguageEN {
		t.Errorf("ParseCommandInput(EN) = %q, %v", lang, ok)
	}
	if _, ok := ParseCommandInput("french"); ok {
		t.Error("expected ParseCommandInput(french) to fail")
	}
}

// TestCatalogCompleteness ensures every key used in code exists in both
// language catalogs, so a typo'd or forgotten translation fails the build
// rather than silently falling back to English (or the raw key) at runtime.
func TestCatalogCompleteness(t *testing.T) {
	keys := []string{
		KeyWelcome, KeyUndoNone, KeyUndoFailed, KeyUndoSuccess,
		KeySyncUsage, KeySyncFailed, KeySyncMatched, KeySyncDiscrepancy,
		KeyVoiceDownloadFailed, KeyVoiceUnsupported, KeyReceiptDownloadFailed, KeyAITrouble,
		KeyTransactionSaveFailed, KeyTransactionRecorded, KeyTransactionCorrected,
		KeyTransactionRecordedWithDescription, KeyTransactionCorrectedWithDescription,
		KeyBalance, KeyQuerySpendingTotal, KeyQuerySpendingByCategory, KeyQueryCategoryNotFound,
		KeyQuerySpendingByBucket, KeyQuerySpendingBreakdownItem, KeyQuerySpendingScopeNone, KeyUncategorizedLabel,
		KeyQuerySpendingListHeader, KeyQuerySpendingListHeaderAll, KeyQuerySpendingListItem,
		KeyQuerySpendingListMore, KeyQuerySpendingListEmptyAll, KeyPeriodLabelSince,
		KeyQuerySpendingListHeaderAllTypes, KeyQuerySpendingListItemWithType, KeyQuerySpendingListEmptyAllTypes,
		KeyQueryTypeTotal, KeyQueryAllTypesHeader, KeyQueryAllTypesItem,
		KeyLanguageUsage, KeyLanguageSet,
		KeyBudgetNoIncome, KeyBudgetNeedsExceeded, KeyBudgetWantsExceeded,
		KeyBudgetWantsPace, KeyBudgetRollover, KeySurplusNone, KeyDailyTransactionReminder, KeyDailyTransactionRecordedAck,
		KeyBudgetSetUsage, KeyBudgetSetInvalid, KeyBudgetSetFailed, KeyBudgetSetSuccess, KeyBudgetShow,
		KeyBudgetShowAgainstIncome, KeyBudgetShowAgainstHypothetical, KeyBudgetShowNoIncome,
		KeyQuotaExceededWithReward, KeyQuotaExceededNoReward,
		KeyClaimUsage, KeyClaimSuccess, KeyClaimInvalidCode, KeyClaimLimitReached,
		KeyDashboardLink, KeyDashboardUnavailable,
		KeyRecurringCreated, KeyRecurringCreateFailed, KeyRecurringPosted,
		KeyRecurringListEmpty, KeyRecurringListItem, KeyRecurringModeReminder,
		KeyRecurringCancelUsage, KeyRecurringCancelSuccess, KeyRecurringCancelNotFound,
		KeyRecurringUpdated, KeyBillReminderUpdated,
		KeyBillReminderCreated, KeyBillReminderDue,
		KeyCategoryInsightHeader, KeyCategoryInsightItem, KeyCategoryInsightWarning,
		KeyGoalUsage, KeyGoalCreated, KeyGoalReplaced, KeyGoalCreateFailed, KeyGoalNone,
		KeyGoalProgress, KeyGoalProgressToGo, KeyGoalProgressDeadline, KeyGoalReached, KeyGoalCancelSuccess,
		KeySubscribeLink, KeySubscribePending, KeySubscribeUnavailable, KeySubscribeSuccess, KeySubscribeFailed,
		KeySubscriptionExpired, KeyMySubscriptionPro, KeyMySubscriptionFree,
		KeyAccountBalance, KeyAccountNotFound, KeyTransferRecorded, KeySameAccountTransfer, KeyTransferFailed,
		KeyAccountsListEmpty, KeyAccountsListItem, KeyAccountsListTotal,
	}
	for _, lang := range []models.Language{models.LanguageEN, models.LanguageID} {
		for _, key := range keys {
			if _, ok := catalog[lang][key]; !ok {
				t.Errorf("catalog[%q] is missing key %q", lang, key)
			}
		}
	}
}
