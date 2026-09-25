// Package i18n holds the bot's reply message catalog and the small amount
// of logic for detecting/parsing a user's preferred language. It's
// deliberately a flat key->template map rather than a full i18n framework —
// this bot only needs two languages and a handful of messages.
package i18n

import (
	"fmt"
	"strings"

	"aura-saku/internal/models"
)

// DetectFromTelegramCode maps a raw Telegram language_code (e.g. "id",
// "en-US", or empty — many clients don't send one) to a supported
// Language. Empty defaults to Indonesian (the primary market); any
// recognized-but-unsupported locale (ja, fr, ...) falls back to English as
// the more universal choice for a genuinely non-Indonesian user.
func DetectFromTelegramCode(code string) models.Language {
	code = strings.ToLower(strings.TrimSpace(code))
	switch {
	case code == "":
		return models.LanguageID
	case strings.HasPrefix(code, "id"):
		return models.LanguageID
	case strings.HasPrefix(code, "en"):
		return models.LanguageEN
	default:
		return models.LanguageEN
	}
}

// ParseCommandInput validates a /language command argument.
func ParseCommandInput(input string) (models.Language, bool) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "id", "indonesia", "bahasa", "bahasa indonesia":
		return models.LanguageID, true
	case "en", "english", "inggris":
		return models.LanguageEN, true
	default:
		return "", false
	}
}

// Name returns the human-readable language name used inside the AI system
// prompt instruction (e.g. "write this in %s").
func Name(lang models.Language) string {
	if lang == models.LanguageID {
		return "Bahasa Indonesia"
	}
	return "English"
}

// Message keys. Every key must exist in every language's catalog entry
// below — T() falls back to English for a missing translation, but that
// fallback existing is a safety net, not something to rely on.
const (
	KeyWelcome                 = "welcome"
	KeyUndoNone                = "undo_none"
	KeyUndoFailed              = "undo_failed"
	KeyUndoSuccess             = "undo_success"
	KeySyncUsage               = "sync_usage"
	KeySyncFailed              = "sync_failed"
	KeySyncMatched             = "sync_matched"
	KeySyncDiscrepancy         = "sync_discrepancy"
	KeyVoiceDownloadFailed     = "voice_download_failed"
	KeyVoiceUnsupported        = "voice_unsupported"
	KeyAITrouble               = "ai_trouble"
	KeyTransactionSaveFailed   = "transaction_save_failed"
	KeyTransactionRecorded     = "transaction_recorded"
	KeyTransactionCorrected    = "transaction_corrected"
	KeyBalance                 = "balance"
	KeyQuerySpendingTotal      = "query_spending_total"
	KeyQuerySpendingByCategory = "query_spending_by_category"
	KeyQueryCategoryNotFound   = "query_category_not_found"
	KeyLanguageUsage           = "language_usage"
	KeyLanguageSet             = "language_set"
	KeyInputTooLong            = "input_too_long"

	KeyBudgetNoIncome      = "budget_no_income"
	KeyBudgetNeedsExceeded = "budget_needs_exceeded"
	KeyBudgetWantsExceeded = "budget_wants_exceeded"
	KeyBudgetWantsPace     = "budget_wants_pace"
	KeyBudgetRollover      = "budget_rollover"

	KeyQuotaExceededWithReward = "quota_exceeded_with_reward"
	KeyQuotaExceededNoReward   = "quota_exceeded_no_reward"
	KeyClaimUsage              = "claim_usage"
	KeyClaimSuccess            = "claim_success"
	KeyClaimInvalidCode        = "claim_invalid_code"
	KeyClaimLimitReached       = "claim_limit_reached"

	KeyDashboardLink        = "dashboard_link"
	KeyDashboardUnavailable = "dashboard_unavailable"
)

var catalog = map[models.Language]map[string]string{
	models.LanguageEN: {
		KeyWelcome:                 "Welcome to Aura-Saku! Send me something like \"lunch 25000\" to record an expense, or a voice note. Use /language en or /language id to change my reply language anytime.",
		KeyUndoNone:                "You don't have any transactions to undo yet.",
		KeyUndoFailed:              "Something went wrong undoing your last transaction.",
		KeyUndoSuccess:             "Undone: %s of %.2f %s.",
		KeySyncUsage:               "Usage: /sync <actual balance>, e.g. /sync 1500000",
		KeySyncFailed:              "Something went wrong syncing your balance.",
		KeySyncMatched:             "Balance synced — matches your recorded balance of %.2f.",
		KeySyncDiscrepancy:         "Found a %.2f discrepancy (your balance: %.2f, recorded: %.2f). I've logged an adjustment entry to keep things accurate — use /undo if that's wrong.",
		KeyVoiceDownloadFailed:     "Sorry, I couldn't download your voice note. Please try again.",
		KeyVoiceUnsupported:        "Voice notes aren't supported by the current AI provider (%s). Please type your transaction instead, or ask an admin to switch AI_PROVIDER.",
		KeyAITrouble:               "Sorry, I had trouble processing that. Please try again in a moment. If it keeps happening, report error code %s to the developer.",
		KeyTransactionSaveFailed:   "Something went wrong saving your transaction.",
		KeyTransactionRecorded:     "Recorded: %s of %.2f %s for \"%s\".",
		KeyTransactionCorrected:    "Corrected: %s of %.2f %s for \"%s\" (previous entry undone).",
		KeyBalance:                 "Your current balance is %.2f %s.",
		KeyQuerySpendingTotal:      "You've spent %.2f %s %s.",
		KeyQuerySpendingByCategory: "You've spent %.2f %s on %s %s.",
		KeyQueryCategoryNotFound:   "I couldn't find a category matching \"%s\" — no spending recorded under that name %s.",
		KeyLanguageUsage:           "Usage: /language <en|id>, e.g. /language id",
		KeyLanguageSet:             "Language set to English.",
		KeyInputTooLong:            "That message is too long (max %d characters). Please shorten it and send it again — splitting it across multiple messages won't work either, since each one is read on its own.",

		KeyBudgetNoIncome:      "No income recorded yet this period — log your income (e.g. \"salary 5000000\") to enable budget limit tracking.",
		KeyBudgetNeedsExceeded: "Needs spending has exceeded your %.0f%% allocation: %.2f of %.2f.",
		KeyBudgetWantsExceeded: "Wants spending has exceeded your %.0f%% allocation: %.2f of %.2f.",
		KeyBudgetWantsPace:     "Your Wants spending pace is projected to reach %.2f by month end, above your %.2f allocation — consider slowing down.",
		KeyBudgetRollover:      "You had a surplus of %.2f last month — consider moving it into savings or investments.",

		KeyQuotaExceededWithReward: "You've used all %d free prompts for today. 🚀 Upgrade to Pro for unlimited use, or earn %d more today: %s\nAfter visiting, send /claim %s here to unlock them.",
		KeyQuotaExceededNoReward:   "You've used all %d free prompts for today. 🚀 Upgrade to Pro for unlimited use, or try again tomorrow.",
		KeyClaimUsage:              "Usage: /claim <code> — you'll get a code when you hit your daily limit.",
		KeyClaimSuccess:            "🎉 You've unlocked %d bonus prompts for today!",
		KeyClaimInvalidCode:        "That code isn't valid or has expired. You'll get a fresh one next time you hit your daily limit.",
		KeyClaimLimitReached:       "You've already claimed your bonus for today. Try again tomorrow!",

		KeyDashboardLink:        "Here's your one-time dashboard login link (valid for 10 minutes): %s",
		KeyDashboardUnavailable: "The dashboard isn't available yet. Please check back later.",
	},
	models.LanguageID: {
		KeyWelcome:                 "Selamat datang di Aura-Saku! Kirim pesan seperti \"makan siang 25000\" untuk mencatat pengeluaran, atau kirim voice note. Gunakan /language en atau /language id untuk mengganti bahasa balasanku kapan saja.",
		KeyUndoNone:                "Kamu belum punya transaksi yang bisa dibatalkan.",
		KeyUndoFailed:              "Terjadi kesalahan saat membatalkan transaksi terakhirmu.",
		KeyUndoSuccess:             "Dibatalkan: %s sebesar %.2f %s.",
		KeySyncUsage:               "Cara pakai: /sync <saldo sebenarnya>, contoh: /sync 1500000",
		KeySyncFailed:              "Terjadi kesalahan saat menyinkronkan saldomu.",
		KeySyncMatched:             "Saldo cocok — sesuai dengan catatan sebesar %.2f.",
		KeySyncDiscrepancy:         "Ditemukan selisih %.2f (saldomu: %.2f, tercatat: %.2f). Aku sudah mencatat entri penyesuaian agar catatan tetap akurat — gunakan /undo jika itu salah.",
		KeyVoiceDownloadFailed:     "Maaf, aku tidak bisa mengunduh voice note-mu. Silakan coba lagi.",
		KeyVoiceUnsupported:        "Voice note tidak didukung oleh AI provider saat ini (%s). Silakan ketik transaksimu, atau minta admin mengganti AI_PROVIDER.",
		KeyAITrouble:               "Maaf, aku kesulitan memproses pesan itu. Silakan coba lagi sebentar. Jika terus terjadi, laporkan kode error %s ke developer.",
		KeyTransactionSaveFailed:   "Terjadi kesalahan saat menyimpan transaksimu.",
		KeyTransactionRecorded:     "Tercatat: %s sebesar %.2f %s untuk \"%s\".",
		KeyTransactionCorrected:    "Dikoreksi: %s sebesar %.2f %s untuk \"%s\" (entri sebelumnya dibatalkan).",
		KeyBalance:                 "Saldo kamu saat ini adalah %.2f %s.",
		KeyQuerySpendingTotal:      "Kamu sudah menghabiskan %.2f %s %s.",
		KeyQuerySpendingByCategory: "Kamu sudah menghabiskan %.2f %s untuk %s %s.",
		KeyQueryCategoryNotFound:   "Aku tidak menemukan kategori yang cocok dengan \"%s\" — tidak ada pengeluaran tercatat dengan nama itu %s.",
		KeyLanguageUsage:           "Cara pakai: /language <en|id>, contoh: /language id",
		KeyLanguageSet:             "Bahasa diatur ke Bahasa Indonesia.",
		KeyInputTooLong:            "Pesan itu terlalu panjang (maks %d karakter). Coba persingkat dan kirim ulang ya — dipecah jadi beberapa pesan juga tidak akan membantu, karena masing-masing dibaca sendiri-sendiri.",

		KeyBudgetNoIncome:      "Belum ada pemasukan tercatat periode ini — catat pemasukanmu (misal \"gaji 5000000\") untuk mengaktifkan pelacakan batas anggaran.",
		KeyBudgetNeedsExceeded: "Pengeluaran Needs sudah melebihi alokasi %.0f%%: %.2f dari %.2f.",
		KeyBudgetWantsExceeded: "Pengeluaran Wants sudah melebihi alokasi %.0f%%: %.2f dari %.2f.",
		KeyBudgetWantsPace:     "Laju pengeluaran Wants-mu diperkirakan mencapai %.2f di akhir bulan, melebihi alokasi %.2f — pertimbangkan untuk mengurangi pengeluaran.",
		KeyBudgetRollover:      "Kamu punya surplus %.2f bulan lalu — pertimbangkan untuk memindahkannya ke tabungan atau investasi.",

		KeyQuotaExceededWithReward: "Kamu sudah pakai semua %d jatah gratis hari ini. 🚀 Upgrade ke Pro untuk pemakaian tanpa batas, atau dapatkan %d tambahan hari ini: %s\nSetelah mengunjungi link itu, kirim /claim %s di sini untuk membukanya.",
		KeyQuotaExceededNoReward:   "Kamu sudah pakai semua %d jatah gratis hari ini. 🚀 Upgrade ke Pro untuk pemakaian tanpa batas, atau coba lagi besok.",
		KeyClaimUsage:              "Cara pakai: /claim <kode> — kamu akan dapat kode saat mencapai batas harian.",
		KeyClaimSuccess:            "🎉 Kamu mendapat %d prompt bonus untuk hari ini!",
		KeyClaimInvalidCode:        "Kode itu tidak valid atau sudah kedaluwarsa. Kamu akan dapat kode baru saat mencapai batas harian lagi.",
		KeyClaimLimitReached:       "Kamu sudah klaim bonus untuk hari ini. Coba lagi besok!",

		KeyDashboardLink:        "Ini link login dashboard sekali pakai (berlaku 10 menit): %s",
		KeyDashboardUnavailable: "Dashboard belum tersedia. Silakan coba lagi nanti.",
	},
}

// periodLabels renders a period keyword ("daily"|"weekly"|"monthly"|"yearly")
// as a short natural-language phrase for embedding in a reply, e.g. "this
// month" / "bulan ini" — used by the bot's spending-query intent reply.
var periodLabels = map[string]map[models.Language]string{
	"daily":   {models.LanguageEN: "today", models.LanguageID: "hari ini"},
	"weekly":  {models.LanguageEN: "this week", models.LanguageID: "minggu ini"},
	"monthly": {models.LanguageEN: "this month", models.LanguageID: "bulan ini"},
	"yearly":  {models.LanguageEN: "this year", models.LanguageID: "tahun ini"},
}

// PeriodLabel renders period as a natural-language phrase in lang, falling
// back to the raw period string for an unrecognized value rather than
// panicking or returning empty.
func PeriodLabel(lang models.Language, period string) string {
	if label, ok := periodLabels[period][lang]; ok {
		return label
	}
	return period
}

// T renders a message in the given language, falling back to English if the
// language or key isn't in the catalog (and to the raw key itself if even
// English is missing it, so a missing translation never panics).
func T(lang models.Language, key string, args ...any) string {
	tmpl, ok := catalog[lang][key]
	if !ok {
		tmpl, ok = catalog[models.LanguageEN][key]
		if !ok {
			return key
		}
	}
	if len(args) == 0 {
		return tmpl
	}
	return fmt.Sprintf(tmpl, args...)
}
