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
		KeyVoiceDownloadFailed, KeyVoiceUnsupported, KeyAITrouble,
		KeyTransactionSaveFailed, KeyTransactionRecorded,
		KeyLanguageUsage, KeyLanguageSet,
		KeyBudgetNoIncome, KeyBudgetNeedsExceeded, KeyBudgetWantsExceeded,
		KeyBudgetWantsPace, KeyBudgetRollover,
		KeyQuotaExceededWithReward, KeyQuotaExceededNoReward,
		KeyClaimUsage, KeyClaimSuccess, KeyClaimInvalidCode, KeyClaimLimitReached,
	}
	for _, lang := range []models.Language{models.LanguageEN, models.LanguageID} {
		for _, key := range keys {
			if _, ok := catalog[lang][key]; !ok {
				t.Errorf("catalog[%q] is missing key %q", lang, key)
			}
		}
	}
}
