// Package i18n holds the bot's reply message catalog and the small amount
// of logic for detecting/parsing a user's preferred language. It's
// deliberately a flat key->template map rather than a full i18n framework —
// this bot only needs two languages and a handful of messages.
package i18n

import (
	"fmt"
	"math"
	"strconv"
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
	KeyWelcome               = "welcome"
	KeyUndoNone              = "undo_none"
	KeyUndoFailed            = "undo_failed"
	KeyUndoSuccess           = "undo_success"
	KeySyncUsage             = "sync_usage"
	KeySyncFailed            = "sync_failed"
	KeySyncMatched           = "sync_matched"
	KeySyncDiscrepancy       = "sync_discrepancy"
	KeyVoiceDownloadFailed   = "voice_download_failed"
	KeyVoiceUnsupported      = "voice_unsupported"
	KeyReceiptDownloadFailed = "receipt_download_failed"
	KeyAITrouble             = "ai_trouble"
	KeyTransactionSaveFailed = "transaction_save_failed"
	KeyTransactionRecorded   = "transaction_recorded"
	KeyTransactionCorrected  = "transaction_corrected"
	// KeyTransactionRecordedWithDescription/KeyTransactionCorrectedWithDescription
	// are used instead of the plain versions above whenever the parsed
	// transaction has a non-empty Description — a receipt photo's own
	// summary of what was purchased is genuinely useful to show, not just
	// the bare type/amount/category, and any other source's description
	// (when the model provides one) benefits the same way.
	KeyTransactionRecordedWithDescription  = "transaction_recorded_with_description"
	KeyTransactionCorrectedWithDescription = "transaction_corrected_with_description"
	KeyBalance                             = "balance"
	KeyQuerySpendingTotal                  = "query_spending_total"
	KeyQuerySpendingByCategory             = "query_spending_by_category"
	KeyQueryCategoryNotFound               = "query_category_not_found"
	KeyQuerySpendingByBucket               = "query_spending_by_bucket"
	KeyQuerySpendingBreakdownItem          = "query_spending_bucket_item"
	KeyQuerySpendingScopeNone              = "query_spending_bucket_none"
	KeyUncategorizedLabel                  = "uncategorized_label"
	KeyQuerySpendingListHeader             = "query_spending_list_header"
	KeyQuerySpendingListHeaderAll          = "query_spending_list_header_all"
	KeyQuerySpendingListItem               = "query_spending_list_item"
	KeyQuerySpendingListMore               = "query_spending_list_more"
	KeyQuerySpendingListEmptyAll           = "query_spending_list_empty_all"
	KeyPeriodLabelSince                    = "period_label_since"
	KeyQuerySpendingListHeaderAllTypes     = "query_spending_list_header_all_types"
	KeyQuerySpendingListItemWithType       = "query_spending_list_item_with_type"
	KeyQuerySpendingListEmptyAllTypes      = "query_spending_list_empty_all_types"
	KeyQueryTypeTotal                      = "query_type_total"
	KeyQueryAllTypesHeader                 = "query_all_types_header"
	KeyQueryAllTypesItem                   = "query_all_types_item"
	KeyLanguageUsage                       = "language_usage"
	KeyLanguageSet                         = "language_set"
	KeyInputTooLong                        = "input_too_long"

	KeyBudgetNoIncome                = "budget_no_income"
	KeyBudgetNeedsExceeded           = "budget_needs_exceeded"
	KeyBudgetWantsExceeded           = "budget_wants_exceeded"
	KeyBudgetWantsPace               = "budget_wants_pace"
	KeyBudgetRollover                = "budget_rollover"
	KeySurplusNone                   = "surplus_none"
	KeyDailyTransactionReminder      = "daily_transaction_reminder"
	KeyDailyTransactionRecordedAck   = "daily_transaction_recorded_ack"
	KeyBudgetSetUsage                = "budget_set_usage"
	KeyBudgetSetInvalid              = "budget_set_invalid"
	KeyBudgetSetFailed               = "budget_set_failed"
	KeyBudgetSetSuccess              = "budget_set_success"
	KeyBudgetShow                    = "budget_show"
	KeyBudgetShowAgainstIncome       = "budget_show_against_income"
	KeyBudgetShowAgainstHypothetical = "budget_show_against_hypothetical"
	KeyBudgetShowNoIncome            = "budget_show_no_income"

	KeyQuotaExceededWithReward = "quota_exceeded_with_reward"
	KeyQuotaExceededNoReward   = "quota_exceeded_no_reward"
	KeyClaimUsage              = "claim_usage"
	KeyClaimSuccess            = "claim_success"
	KeyClaimInvalidCode        = "claim_invalid_code"
	KeyClaimLimitReached       = "claim_limit_reached"

	KeyDashboardLink        = "dashboard_link"
	KeyDashboardUnavailable = "dashboard_unavailable"

	KeyRecurringCreated        = "recurring_created"
	KeyRecurringCreateFailed   = "recurring_create_failed"
	KeyRecurringPosted         = "recurring_posted"
	KeyRecurringListEmpty      = "recurring_list_empty"
	KeyRecurringListItem       = "recurring_list_item"
	KeyRecurringModeReminder   = "recurring_mode_reminder"
	KeyRecurringCancelUsage    = "recurring_cancel_usage"
	KeyRecurringCancelSuccess  = "recurring_cancel_success"
	KeyRecurringCancelNotFound = "recurring_cancel_not_found"
	KeyRecurringUpdated        = "recurring_updated"
	KeyBillReminderUpdated     = "bill_reminder_updated"

	KeyBillReminderCreated = "bill_reminder_created"
	KeyBillReminderDue     = "bill_reminder_due"

	KeyCategoryInsightHeader  = "category_insight_header"
	KeyCategoryInsightItem    = "category_insight_item"
	KeyCategoryInsightWarning = "category_insight_warning"

	KeyGoalUsage            = "goal_usage"
	KeyGoalCreated          = "goal_created"
	KeyGoalReplaced         = "goal_replaced"
	KeyGoalCreateFailed     = "goal_create_failed"
	KeyGoalNone             = "goal_none"
	KeyGoalProgress         = "goal_progress"
	KeyGoalProgressToGo     = "goal_progress_to_go"
	KeyGoalProgressDeadline = "goal_progress_deadline"
	KeyGoalReached          = "goal_reached"
	KeyGoalCancelSuccess    = "goal_cancel_success"

	KeySubscribeLink        = "subscribe_link"
	KeySubscribePending     = "subscribe_pending"
	KeySubscribeUnavailable = "subscribe_unavailable"
	KeySubscribeSuccess     = "subscribe_success"
	KeySubscribeFailed      = "subscribe_failed"
	KeySubscriptionExpired  = "subscription_expired"
	KeyMySubscriptionPro    = "my_subscription_pro"
	KeyMySubscriptionFree   = "my_subscription_free"
)

var catalog = map[models.Language]map[string]string{
	models.LanguageEN: {
		KeyWelcome:                             "Welcome to Aura-Saku! Send me something like \"lunch 25000\" to record an expense, or a voice note. Use /language en or /language id to change my reply language anytime.",
		KeyUndoNone:                            "You don't have any transactions to undo yet.",
		KeyUndoFailed:                          "Something went wrong undoing your last transaction.",
		KeyUndoSuccess:                         "Undone: %s of %s.",
		KeySyncUsage:                           "Usage: /sync <actual balance>, e.g. /sync 1500000",
		KeySyncFailed:                          "Something went wrong syncing your balance.",
		KeySyncMatched:                         "Balance synced — matches your recorded balance of %.2f.",
		KeySyncDiscrepancy:                     "Found a %.2f discrepancy (your balance: %.2f, recorded: %.2f). I've logged an adjustment entry to keep things accurate — use /undo if that's wrong.",
		KeyVoiceDownloadFailed:                 "Sorry, I couldn't download your voice note. Please try again.",
		KeyVoiceUnsupported:                    "Voice notes aren't supported by the current AI provider (%s). Please type your transaction instead, or ask an admin to switch AI_PROVIDER.",
		KeyReceiptDownloadFailed:               "Sorry, I couldn't download your receipt photo. Please try again.",
		KeyAITrouble:                           "Sorry, I had trouble processing that. Please try again in a moment. If it keeps happening, report error code %s to the developer.",
		KeyTransactionSaveFailed:               "Something went wrong saving your transaction.",
		KeyTransactionRecorded:                 "Recorded: %s of %s for \"%s\".",
		KeyTransactionCorrected:                "Corrected: %s of %s for \"%s\" (previous entry undone).",
		KeyTransactionRecordedWithDescription:  "Recorded: %s of %s for \"%s\" — %s.",
		KeyTransactionCorrectedWithDescription: "Corrected: %s of %s for \"%s\" — %s (previous entry undone).",
		KeyBalance:                             "Your current balance is %s.",
		KeyQuerySpendingTotal:                  "You've spent %s %s.",
		KeyQuerySpendingByCategory:             "You've spent %s on %s %s.",
		KeyQueryCategoryNotFound:               "I couldn't find a category matching \"%s\" — no spending recorded under that name %s.",
		KeyQuerySpendingByBucket:               "You've spent %s on %s %s:",
		KeyQuerySpendingBreakdownItem:          "%d. %s: %s (%.0f%% of total)",
		KeyQuerySpendingScopeNone:              "You haven't spent anything on %s %s.",
		KeyUncategorizedLabel:                  "Uncategorized",
		KeyQuerySpendingListHeader:             "Here's what you spent on %s %s:",
		KeyQuerySpendingListHeaderAll:          "Here's what you spent %s:",
		KeyQuerySpendingListItem:               "%d. %s — %s: %s (%s)",
		KeyQuerySpendingListMore:               "...and %d more — say \"show me more\" to see the rest.",
		KeyQuerySpendingListEmptyAll:           "You don't have any expenses %s.",
		KeyPeriodLabelSince:                    "since %s",
		KeyQuerySpendingListHeaderAllTypes:     "Here's your transaction history %s:",
		KeyQuerySpendingListItemWithType:       "%d. %s — [%s] %s: %s (%s)",
		KeyQuerySpendingListEmptyAllTypes:      "You don't have any transactions %s.",
		KeyQueryTypeTotal:                      "Your total %s %s: %s.",
		KeyQueryAllTypesHeader:                 "Here's your transaction summary %s:",
		KeyQueryAllTypesItem:                   "%s: %s",
		KeyLanguageUsage:                       "Usage: /language <en|id>, e.g. /language id",
		KeyLanguageSet:                         "Language set to English.",
		KeyInputTooLong:                        "That message is too long (max %d characters). Please shorten it and send it again — splitting it across multiple messages won't work either, since each one is read on its own.",

		KeyBudgetNoIncome:                "No income recorded yet this period — log your income (e.g. \"salary 5000000\") to enable budget limit tracking.",
		KeyBudgetNeedsExceeded:           "Needs spending has exceeded your %.0f%% allocation: %.2f of %.2f.",
		KeyBudgetWantsExceeded:           "Wants spending has exceeded your %.0f%% allocation: %.2f of %.2f.",
		KeyBudgetWantsPace:               "Your Wants spending pace is projected to reach %.2f by month end, above your %.2f allocation — consider slowing down.",
		KeyBudgetRollover:                "You had a surplus of %s last month — consider moving it into savings or investments.",
		KeySurplusNone:                   "You didn't have a surplus last month — your expenses matched or exceeded your income.",
		KeyDailyTransactionReminder:      "👋 Just a heads up — you haven't recorded any transactions today. Don't forget to log your spending before the day ends!",
		KeyDailyTransactionRecordedAck:   "🎉 Nice, you've recorded %d transaction(s) today! Keep up the habit.",
		KeyBudgetSetUsage:                "Usage: /budget <needs%> <wants%> <savings%> — must sum to 100, e.g. /budget 50 30 20. Send /budget with no arguments to see your current allocation.",
		KeyBudgetSetInvalid:              "Those percentages must each be zero or more and sum to exactly 100.",
		KeyBudgetSetFailed:               "Something went wrong updating your budget allocation.",
		KeyBudgetSetSuccess:              "✅ Budget updated: %.0f%% Needs, %.0f%% Wants, %.0f%% Savings — effective this period.",
		KeyBudgetShow:                    "Your current budget allocation: %.0f%% Needs, %.0f%% Wants, %.0f%% Savings. Use /budget <needs> <wants> <savings> to change it.",
		KeyBudgetShowAgainstIncome:       "Based on your most recent income (%s): Needs %.0f%% = %s, Wants %.0f%% = %s, Savings %.0f%% = %s.",
		KeyBudgetShowAgainstHypothetical: "If your income were %s: Needs %.0f%% = %s, Wants %.0f%% = %s, Savings %.0f%% = %s.",
		KeyBudgetShowNoIncome:            "You don't have any income recorded yet to base this on. Record an income transaction first, or just ask for your budget split as plain percentages.",

		KeyQuotaExceededWithReward: "You've used all %d free prompts for today. 🚀 Send /subscribe to upgrade to Pro for unlimited use, or earn %d more today: %s\nAfter visiting, send /claim %s here to unlock them.",
		KeyQuotaExceededNoReward:   "You've used all %d free prompts for today. 🚀 Send /subscribe to upgrade to Pro for unlimited use, or try again tomorrow.",
		KeyClaimUsage:              "Usage: /claim <code> — you'll get a code when you hit your daily limit.",
		KeyClaimSuccess:            "🎉 You've unlocked %d bonus prompts for today!",
		KeyClaimInvalidCode:        "That code isn't valid or has expired. You'll get a fresh one next time you hit your daily limit.",
		KeyClaimLimitReached:       "You've already claimed your bonus for today. Try again tomorrow!",

		KeyDashboardLink:        "Here's your one-time dashboard login link (valid for 10 minutes): %s",
		KeyDashboardUnavailable: "The dashboard isn't available yet. Please check back later.",

		KeyRecurringCreated:        "✅ Recurring %s of %s for \"%s\" set up — it'll auto-record on day %d of each month, starting %s.",
		KeyRecurringCreateFailed:   "Something went wrong setting up that recurring transaction.",
		KeyRecurringPosted:         "🔁 Recurring: %s of %s for \"%s\" was just recorded automatically.",
		KeyRecurringListEmpty:      "You don't have any recurring transactions or bill reminders set up yet. Just tell me about one, e.g. \"netflix 150000 every month on the 5th\", or \"remind me to pay rent 800000 on the 1st\".",
		KeyRecurringListItem:       "#%d: %s of %s for \"%s\" — day %d of each month, next: %s%s",
		KeyRecurringModeReminder:   " (reminder only, not recorded automatically)",
		KeyRecurringCancelUsage:    "Usage: /recurring to list, or /recurring cancel <id> to cancel one, e.g. /recurring cancel 3.",
		KeyRecurringCancelSuccess:  "Canceled recurring %s of %s for \"%s\".",
		KeyRecurringCancelNotFound: "I couldn't find an active recurring transaction with that id. Use /recurring to see your list.",
		KeyRecurringUpdated:        "✅ Recurring %s of %s for \"%s\" updated (previously %s for \"%s\") — it'll auto-record on day %d of each month, starting %s.",
		KeyBillReminderUpdated:     "🔔 Reminder updated for %s of %s for \"%s\" (previously %s for \"%s\") — I'll notify you on day %d of each month, starting %s. I won't record it automatically; let me know when you've paid it.",

		KeyBillReminderCreated: "🔔 Reminder set for %s of %s for \"%s\" — I'll notify you on day %d of each month, starting %s. I won't record it automatically; let me know when you've paid it.",
		KeyBillReminderDue:     "🔔 Reminder: your %s of %s for \"%s\" is due today.",

		KeyCategoryInsightHeader:  "📊 Your top spending categories last month (total: %s):",
		KeyCategoryInsightItem:    "%d. %s: %s (%.0f%% of total)",
		KeyCategoryInsightWarning: "⚠️ %s increased the most compared to the month before — %s last month vs %s the month before (up %.0f%%). Consider limiting this category to save more.",

		KeyGoalUsage:            "Usage: /goal to see your progress, or /goal cancel to cancel your current goal.",
		KeyGoalCreated:          "🎯 New goal: save %s for \"%s\"%s. Check your progress anytime with /goal.",
		KeyGoalReplaced:         "🎯 New goal: save %s for \"%s\"%s — replacing your previous goal of %s for \"%s\". Check your progress anytime with /goal.",
		KeyGoalCreateFailed:     "Something went wrong setting up that savings goal.",
		KeyGoalNone:             "You don't have an active savings goal yet. Just tell me about one, e.g. \"I want to save 10000000 for a laptop by December\".",
		KeyGoalProgress:         "🎯 \"%s\": %s of %s saved (%.0f%%).",
		KeyGoalProgressToGo:     "%s to go.",
		KeyGoalProgressDeadline: "Deadline: %s.",
		KeyGoalReached:          "🎉 You've reached your goal! Great job — set a new one anytime, or just keep going.",
		KeyGoalCancelSuccess:    "Canceled your goal: \"%s\".",

		KeySubscribeLink:        "Tap here to complete your Pro upgrade (Rp50.000/month): %s\nI'll confirm here once the payment goes through.",
		KeySubscribePending:     "You already have a pending payment link — check your last message, or wait a bit before trying again.",
		KeySubscribeUnavailable: "Pro upgrades aren't available yet. Please check back later.",
		KeySubscribeSuccess:     "🎉 You're now Pro! Unlimited daily use until %s.",
		KeySubscribeFailed:      "Your payment didn't go through. Send /subscribe (or just tell me you want to upgrade) to try again.",
		KeySubscriptionExpired:  "Your Pro subscription has expired. Send /subscribe to renew and keep unlimited daily use.",
		KeyMySubscriptionPro:    "You're on the Pro tier — unlimited daily use. Renews on %s.",
		KeyMySubscriptionFree:   "You're on the Free tier. Send /subscribe (or just tell me you want to upgrade) to go Pro for unlimited daily use.",
	},
	models.LanguageID: {
		KeyWelcome:                             "Selamat datang di Aura-Saku! Kirim pesan seperti \"makan siang 25000\" untuk mencatat pengeluaran, atau kirim voice note. Gunakan /language en atau /language id untuk mengganti bahasa balasanku kapan saja.",
		KeyUndoNone:                            "Kamu belum punya transaksi yang bisa dibatalkan.",
		KeyUndoFailed:                          "Terjadi kesalahan saat membatalkan transaksi terakhirmu.",
		KeyUndoSuccess:                         "Dibatalkan: %s sebesar %s.",
		KeySyncUsage:                           "Cara pakai: /sync <saldo sebenarnya>, contoh: /sync 1500000",
		KeySyncFailed:                          "Terjadi kesalahan saat menyinkronkan saldomu.",
		KeySyncMatched:                         "Saldo cocok — sesuai dengan catatan sebesar %.2f.",
		KeySyncDiscrepancy:                     "Ditemukan selisih %.2f (saldomu: %.2f, tercatat: %.2f). Aku sudah mencatat entri penyesuaian agar catatan tetap akurat — gunakan /undo jika itu salah.",
		KeyVoiceDownloadFailed:                 "Maaf, aku tidak bisa mengunduh voice note-mu. Silakan coba lagi.",
		KeyVoiceUnsupported:                    "Voice note tidak didukung oleh AI provider saat ini (%s). Silakan ketik transaksimu, atau minta admin mengganti AI_PROVIDER.",
		KeyReceiptDownloadFailed:               "Maaf, aku tidak bisa mengunduh foto struk-mu. Silakan coba lagi.",
		KeyAITrouble:                           "Maaf, aku kesulitan memproses pesan itu. Silakan coba lagi sebentar. Jika terus terjadi, laporkan kode error %s ke developer.",
		KeyTransactionSaveFailed:               "Terjadi kesalahan saat menyimpan transaksimu.",
		KeyTransactionRecorded:                 "Tercatat: %s sebesar %s untuk \"%s\".",
		KeyTransactionCorrected:                "Dikoreksi: %s sebesar %s untuk \"%s\" (entri sebelumnya dibatalkan).",
		KeyTransactionRecordedWithDescription:  "Tercatat: %s sebesar %s untuk \"%s\" — %s.",
		KeyTransactionCorrectedWithDescription: "Dikoreksi: %s sebesar %s untuk \"%s\" — %s (entri sebelumnya dibatalkan).",
		KeyBalance:                             "Saldo kamu saat ini adalah %s.",
		KeyQuerySpendingTotal:                  "Kamu sudah menghabiskan %s %s.",
		KeyQuerySpendingByCategory:             "Kamu sudah menghabiskan %s untuk %s %s.",
		KeyQueryCategoryNotFound:               "Aku tidak menemukan kategori yang cocok dengan \"%s\" — tidak ada pengeluaran tercatat dengan nama itu %s.",
		KeyQuerySpendingByBucket:               "Kamu sudah menghabiskan %s untuk %s %s:",
		KeyQuerySpendingBreakdownItem:          "%d. %s: %s (%.0f%% dari total)",
		KeyQuerySpendingScopeNone:              "Kamu belum ada pengeluaran untuk %s %s.",
		KeyUncategorizedLabel:                  "Tanpa kategori",
		KeyQuerySpendingListHeader:             "Berikut pengeluaranmu untuk %s %s:",
		KeyQuerySpendingListHeaderAll:          "Berikut pengeluaranmu %s:",
		KeyQuerySpendingListItem:               "%d. %s — %s: %s (%s)",
		KeyQuerySpendingListMore:               "...dan %d lainnya — ketik \"lanjutkan\" untuk lihat selanjutnya.",
		KeyQuerySpendingListEmptyAll:           "Kamu belum ada pengeluaran %s.",
		KeyPeriodLabelSince:                    "sejak %s",
		KeyQuerySpendingListHeaderAllTypes:     "Berikut riwayat transaksimu %s:",
		KeyQuerySpendingListItemWithType:       "%d. %s — [%s] %s: %s (%s)",
		KeyQuerySpendingListEmptyAllTypes:      "Kamu belum ada transaksi %s.",
		KeyQueryTypeTotal:                      "Total %s kamu %s: %s.",
		KeyQueryAllTypesHeader:                 "Berikut ringkasan transaksimu %s:",
		KeyQueryAllTypesItem:                   "%s: %s",
		KeyLanguageUsage:                       "Cara pakai: /language <en|id>, contoh: /language id",
		KeyLanguageSet:                         "Bahasa diatur ke Bahasa Indonesia.",
		KeyInputTooLong:                        "Pesan itu terlalu panjang (maks %d karakter). Coba persingkat dan kirim ulang ya — dipecah jadi beberapa pesan juga tidak akan membantu, karena masing-masing dibaca sendiri-sendiri.",

		KeyBudgetNoIncome:                "Belum ada pemasukan tercatat periode ini — catat pemasukanmu (misal \"gaji 5000000\") untuk mengaktifkan pelacakan batas anggaran.",
		KeyBudgetNeedsExceeded:           "Pengeluaran Needs sudah melebihi alokasi %.0f%%: %.2f dari %.2f.",
		KeyBudgetWantsExceeded:           "Pengeluaran Wants sudah melebihi alokasi %.0f%%: %.2f dari %.2f.",
		KeyBudgetWantsPace:               "Laju pengeluaran Wants-mu diperkirakan mencapai %.2f di akhir bulan, melebihi alokasi %.2f — pertimbangkan untuk mengurangi pengeluaran.",
		KeyBudgetRollover:                "Kamu punya surplus %s bulan lalu — pertimbangkan untuk memindahkannya ke tabungan atau investasi.",
		KeySurplusNone:                   "Kamu nggak ada surplus bulan lalu — pengeluaranmu sama besar atau lebih besar dari pemasukanmu.",
		KeyDailyTransactionReminder:      "👋 Pengingat santai — kamu belum mencatat transaksi apa pun hari ini. Jangan lupa catat pengeluaranmu sebelum hari berakhir, ya!",
		KeyDailyTransactionRecordedAck:   "🎉 Mantap, kamu udah mencatat %d transaksi hari ini! Terus pertahankan kebiasaan ini, ya!",
		KeyBudgetSetUsage:                "Cara pakai: /budget <needs%> <wants%> <savings%> — harus berjumlah 100, contoh: /budget 50 30 20. Kirim /budget tanpa argumen untuk melihat alokasi saat ini.",
		KeyBudgetSetInvalid:              "Persentase harus masing-masing nol atau lebih dan berjumlah tepat 100.",
		KeyBudgetSetFailed:               "Terjadi kesalahan saat memperbarui alokasi budgetmu.",
		KeyBudgetSetSuccess:              "✅ Budget diperbarui: %.0f%% Needs, %.0f%% Wants, %.0f%% Savings — berlaku mulai periode ini.",
		KeyBudgetShow:                    "Alokasi budgetmu saat ini: %.0f%% Needs, %.0f%% Wants, %.0f%% Savings. Gunakan /budget <needs> <wants> <savings> untuk mengubahnya.",
		KeyBudgetShowAgainstIncome:       "Berdasarkan pemasukan terakhirmu (%s): Needs %.0f%% = %s, Wants %.0f%% = %s, Savings %.0f%% = %s.",
		KeyBudgetShowAgainstHypothetical: "Kalau pemasukanmu %s: Needs %.0f%% = %s, Wants %.0f%% = %s, Savings %.0f%% = %s.",
		KeyBudgetShowNoIncome:            "Kamu belum punya pemasukan tercatat untuk jadi dasar perhitungan ini. Catat pemasukanmu dulu, atau tanya pembagian budgetmu dalam persen saja.",

		KeyQuotaExceededWithReward: "Kamu sudah pakai semua %d jatah gratis hari ini. 🚀 Kirim /subscribe untuk upgrade ke Pro dan dapat pemakaian tanpa batas, atau dapatkan %d tambahan hari ini: %s\nSetelah mengunjungi link itu, kirim /claim %s di sini untuk membukanya.",
		KeyQuotaExceededNoReward:   "Kamu sudah pakai semua %d jatah gratis hari ini. 🚀 Kirim /subscribe untuk upgrade ke Pro dan dapat pemakaian tanpa batas, atau coba lagi besok.",
		KeyClaimUsage:              "Cara pakai: /claim <kode> — kamu akan dapat kode saat mencapai batas harian.",
		KeyClaimSuccess:            "🎉 Kamu mendapat %d prompt bonus untuk hari ini!",
		KeyClaimInvalidCode:        "Kode itu tidak valid atau sudah kedaluwarsa. Kamu akan dapat kode baru saat mencapai batas harian lagi.",
		KeyClaimLimitReached:       "Kamu sudah klaim bonus untuk hari ini. Coba lagi besok!",

		KeyDashboardLink:        "Ini link login dashboard sekali pakai (berlaku 10 menit): %s",
		KeyDashboardUnavailable: "Dashboard belum tersedia. Silakan coba lagi nanti.",

		KeyRecurringCreated:        "✅ %s berulang sebesar %s untuk \"%s\" sudah diatur — akan otomatis tercatat tiap tanggal %d, mulai %s.",
		KeyRecurringCreateFailed:   "Terjadi kesalahan saat mengatur transaksi berulang itu.",
		KeyRecurringPosted:         "🔁 Berulang: %s sebesar %s untuk \"%s\" baru saja dicatat otomatis.",
		KeyRecurringListEmpty:      "Kamu belum punya transaksi berulang atau pengingat tagihan. Cukup ceritakan satu, misal \"netflix 150000 tiap tanggal 5\", atau \"ingetin bayar sewa 800000 tiap tanggal 1\".",
		KeyRecurringListItem:       "#%d: %s sebesar %s untuk \"%s\" — tanggal %d tiap bulan, berikutnya: %s%s",
		KeyRecurringModeReminder:   " (hanya pengingat, tidak dicatat otomatis)",
		KeyRecurringCancelUsage:    "Cara pakai: /recurring untuk melihat daftar, atau /recurring cancel <id> untuk membatalkan, contoh: /recurring cancel 3.",
		KeyRecurringCancelSuccess:  "Transaksi berulang %s sebesar %s untuk \"%s\" sudah dibatalkan.",
		KeyRecurringCancelNotFound: "Aku tidak menemukan transaksi berulang aktif dengan id itu. Gunakan /recurring untuk melihat daftarmu.",
		KeyRecurringUpdated:        "✅ %s berulang sebesar %s untuk \"%s\" telah diperbarui (sebelumnya %s untuk \"%s\") — akan otomatis tercatat tiap tanggal %d, mulai %s.",
		KeyBillReminderUpdated:     "🔔 Pengingat telah diperbarui untuk %s sebesar %s untuk \"%s\" (sebelumnya %s untuk \"%s\") — aku akan mengingatkanmu tiap tanggal %d, mulai %s. Aku tidak akan mencatatnya otomatis; kabari aku kalau sudah dibayar.",

		KeyBillReminderCreated: "🔔 Pengingat diatur untuk %s sebesar %s untuk \"%s\" — aku akan mengingatkanmu tiap tanggal %d, mulai %s. Aku tidak akan mencatatnya otomatis; kabari aku kalau sudah dibayar.",
		KeyBillReminderDue:     "🔔 Pengingat: %s sebesar %s untuk \"%s\" jatuh tempo hari ini.",

		KeyCategoryInsightHeader:  "📊 Kategori pengeluaran terbesarmu bulan lalu (total: %s):",
		KeyCategoryInsightItem:    "%d. %s: %s (%.0f%% dari total)",
		KeyCategoryInsightWarning: "⚠️ %s naik paling banyak dibanding bulan sebelumnya — %s bulan lalu vs %s bulan sebelumnya (naik %.0f%%). Pertimbangkan untuk membatasi kategori ini agar bisa menabung lebih banyak.",

		KeyGoalUsage:            "Cara pakai: /goal untuk melihat progressmu, atau /goal cancel untuk membatalkan goal saat ini.",
		KeyGoalCreated:          "🎯 Goal baru: menabung %s untuk \"%s\"%s. Cek progressmu kapan saja dengan /goal.",
		KeyGoalReplaced:         "🎯 Goal baru: menabung %s untuk \"%s\"%s — menggantikan goal sebelumnya sebesar %s untuk \"%s\". Cek progressmu kapan saja dengan /goal.",
		KeyGoalCreateFailed:     "Terjadi kesalahan saat mengatur goal tabunganmu.",
		KeyGoalNone:             "Kamu belum punya goal tabungan aktif. Cukup ceritakan satu, misal \"aku mau nabung 10000000 buat laptop sampai desember\".",
		KeyGoalProgress:         "🎯 \"%s\": %s dari %s terkumpul (%.0f%%).",
		KeyGoalProgressToGo:     "%s lagi.",
		KeyGoalProgressDeadline: "Target: %s.",
		KeyGoalReached:          "🎉 Kamu sudah mencapai goal-mu! Kerja bagus — atur goal baru kapan saja, atau terus lanjutkan.",
		KeyGoalCancelSuccess:    "Goal \"%s\" sudah dibatalkan.",

		KeySubscribeLink:        "Ketuk di sini untuk menyelesaikan upgrade ke Pro (Rp50.000/bulan): %s\nAku akan konfirmasi di sini begitu pembayarannya berhasil.",
		KeySubscribePending:     "Kamu masih punya link pembayaran yang belum selesai — cek pesan sebelumnya, atau tunggu sebentar sebelum coba lagi.",
		KeySubscribeUnavailable: "Upgrade ke Pro belum tersedia. Silakan coba lagi nanti.",
		KeySubscribeSuccess:     "🎉 Kamu sekarang Pro! Pemakaian harian tanpa batas sampai %s.",
		KeySubscribeFailed:      "Pembayaranmu tidak berhasil. Kirim /subscribe (atau bilang aja mau upgrade) untuk coba lagi.",
		KeySubscriptionExpired:  "Langganan Pro kamu sudah habis masa berlakunya. Kirim /subscribe untuk perpanjang dan tetap dapat pemakaian tanpa batas.",
		KeyMySubscriptionPro:    "Kamu di tier Pro — pemakaian harian tanpa batas. Perpanjangan pada %s.",
		KeyMySubscriptionFree:   "Kamu di tier Free. Kirim /subscribe (atau bilang aja mau upgrade) untuk jadi Pro dan dapat pemakaian harian tanpa batas.",
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

// FormatMoney renders a monetary amount for a reply, formatted per the
// user's language: Indonesian readers get the conventional "Rp"-prefixed,
// dot-thousands/comma-decimal style ("Rp2.950.000,00") instead of the
// plain %.2f-plus-currency-code format everyone else gets
// ("2950000.00 IDR"). Non-IDR currencies still get Indonesian-style digit
// grouping when lang is Indonesian, just with the currency code kept as a
// suffix rather than swapped for "Rp".
func FormatMoney(lang models.Language, amount float64, currency string) string {
	if lang != models.LanguageID {
		return fmt.Sprintf("%.2f %s", amount, currency)
	}

	sign := ""
	if amount < 0 {
		sign = "-"
		amount = -amount
	}
	grouped := groupIndonesian(amount)
	if strings.EqualFold(currency, "IDR") {
		return sign + "Rp" + grouped
	}
	return sign + grouped + " " + currency
}

// groupIndonesian formats a non-negative amount with "." as the thousands
// separator and "," before the 2 decimal digits (e.g. 2950000 ->
// "2.950.000,00") — the reverse of the English convention. Sign handling
// lives in FormatMoney (so "-Rp25.000,00" puts the sign before "Rp", not
// between it and the number) — built on integer cents rather than
// string-splitting a %.2f result, so rounding matches FormatMoney's English
// branch exactly.
func groupIndonesian(amount float64) string {
	cents := int64(math.Round(amount * 100))
	whole := strconv.FormatInt(cents/100, 10)
	frac := cents % 100

	var grouped strings.Builder
	for i, d := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			grouped.WriteByte('.')
		}
		grouped.WriteRune(d)
	}

	return fmt.Sprintf("%s,%02d", grouped.String(), frac)
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
