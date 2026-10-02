package prompt

import (
	"fmt"
	"strings"
	"time"
)

// This file is the provider-agnostic counterpart to
// transaction_prompt.go's giant prose contract, shared by all three
// native-function-calling providers (gemini, openai, anthropic — see each
// package's tools.go) so the tool descriptions only exist in one place
// instead of being copy-pasted three times with drift risk. Each provider
// adapts Tool/ToolProperty into its own wire schema shape (Gemini's
// uppercase "OBJECT"/"STRING"/"NUMBER" vs OpenAI/Anthropic's lowercase
// JSON Schema types) — see ToolProperty.Type's doc comment.
const (
	ToolRecordTransaction = "record_transaction"
	ToolQuerySpending     = "query_spending"
	ToolGetBalance        = "get_balance"
	// ToolCorrectTransaction's wire name dropped "last" (it isn't anymore —
	// see its Tool.Description below) now that it can target any of the
	// user's recent transactions, not just the newest one.
	ToolCorrectTransaction         = "correct_transaction"
	ToolDeleteTransaction          = "delete_transaction"
	ToolCreateRecurringTransaction = "create_recurring_transaction"
	ToolCreateBillReminder         = "create_bill_reminder"
	ToolCreateSavingsGoal          = "create_savings_goal"
	ToolDeleteRecurringTransaction = "delete_recurring_transaction"
	ToolSubscribeToPro             = "subscribe_to_pro"
	ToolCheckSubscriptionStatus    = "check_subscription_status"
	ToolCheckBudgetAllocation      = "check_budget_allocation"
	ToolSetBudgetAllocation        = "set_budget_allocation"
	ToolCheckSurplus               = "check_surplus"
	ToolMoveMoney                  = "move_money"
	ToolRecordLoan                 = "record_loan"
	ToolRecordRepayment            = "record_repayment"
	ToolCheckDebts                 = "check_debts"
)

// ToolProperty.Type uses lowercase JSON Schema primitives ("string",
// "number", "object") — the convention OpenAI and Anthropic both use
// as-is. Gemini's adapter uppercases these itself, since its Schema proto
// wants "STRING"/"NUMBER"/"OBJECT" instead.
type ToolProperty struct {
	Type        string
	Description string
	// Enum, if non-empty, restricts the value to these exact strings. Never
	// include "" as an enum member — Gemini's API rejects an empty enum
	// value outright; "not confident" is expressed by omitting the field,
	// not by adding an empty-string option.
	Enum []string
}

type Tool struct {
	Name        string
	Description string
	Properties  map[string]ToolProperty
	Required    []string
}

// targetTransactionIDProperty is shared by correct_transaction and
// delete_transaction — the one field either needs to identify EXACTLY which
// existing transaction it acts on, filled from a prior get_recent_transactions
// lookup rather than assumed to be the newest one. verb names the action
// ("corrected"/"deleted") in the description so each tool's own wording
// stays natural without duplicating this whole sentence per tool.
func targetTransactionIDProperty(verb string) ToolProperty {
	return ToolProperty{
		Type:        "number",
		Description: fmt.Sprintf("The id of the specific transaction being %s, taken from a prior get_recent_transactions lookup — pick whichever candidate's amount/category/description actually matches what this message describes. Required whenever that lookup was called and returned more than one transaction; omit only when there was exactly one transaction it could plausibly mean (e.g. the user's very first/only transaction ever, or the lookup wasn't needed because the message obviously means the single most recent transaction).", verb),
	}
}

// correctTransactionProperties is transactionProperties() plus one field
// only a correction needs: which existing transaction it replaces. Kept
// separate from transactionProperties() (rather than adding it there)
// because record_transaction has no "existing transaction" to reference at
// all — offering the field there would just confuse the model.
func correctTransactionProperties() map[string]ToolProperty {
	props := transactionProperties()
	props["target_transaction_id"] = targetTransactionIDProperty("corrected")
	return props
}

// recurringTransactionProperties is transactionProperties() plus one field
// only a recurring schedule needs: which day of the month it repeats on.
func recurringTransactionProperties() map[string]ToolProperty {
	props := transactionProperties()
	props["day_of_month"] = ToolProperty{
		Type:        "number",
		Description: "Day of the month (1-31) this transaction repeats on, as stated or clearly implied by the message (e.g. \"tanggal 5\" -> 5, \"akhir bulan\"/\"end of month\" -> 31). A month shorter than the given day uses that month's own last day instead, so 31 safely means \"end of every month\" including February — never ask a follow-up just to narrow this down to that month's exact length.",
	}
	return props
}

// savingsGoalProperties declares create_savings_goal's own fields — a goal
// has no Type/CategoryName/Bucket at all (it isn't a transaction), so this
// is deliberately NOT built on transactionProperties() like the other
// tools above.
func savingsGoalProperties() map[string]ToolProperty {
	return map[string]ToolProperty{
		"name": {
			Type:        "string",
			Description: "A short label for what this goal is for, kept in the user's OWN words/language (e.g. \"laptop baru\", \"dana darurat\", \"liburan ke Bali\") — shown back to the user verbatim, unlike category_name this is never translated to English.",
		},
		"target_amount": {
			Type:        "number",
			Description: "The total amount the user wants to save toward this goal. A positive number — never guess one that wasn't stated or clearly implied.",
		},
		"currency": {
			Type:        "string",
			Description: "ISO 4217 code (e.g. IDR, USD, EUR, SGD). If not mentioned, use the caller-provided default currency.",
		},
		"deadline_hint": {
			Type:        "string",
			Description: "ISO 8601 date (YYYY-MM-DD) the user wants to reach this goal by, computed from today's date and any relative/named time language (\"by December\", \"sampai akhir tahun\", \"in 6 months\", \"sebelum lebaran\"). Leave empty if no deadline is mentioned at all — a goal doesn't need one.",
		},
	}
}

// transactionProperties is shared by record_transaction, correct_transaction,
// and create_recurring_transaction — each describes a transaction using the
// exact same fields as a fresh record.
func transactionProperties() map[string]ToolProperty {
	return map[string]ToolProperty{
		"type": {
			Type:        "string",
			Description: "The kind of transaction.",
			Enum:        []string{"expense", "income", "investment", "transfer", "reward"},
		},
		"amount": {
			Type:        "number",
			Description: "A positive number. Never guess a number that wasn't stated or clearly implied.",
		},
		"currency": {
			Type:        "string",
			Description: "ISO 4217 code (e.g. IDR, USD, EUR, SGD). If not mentioned, use the caller-provided default currency.",
		},
		"category_name": {
			Type:        "string",
			Description: "A short free-text category guess (e.g. \"food\", \"transport\", \"salary\"), ALWAYS in English regardless of the message's language, since it's used for internal budget categorization, not shown to the user.",
		},
		"bucket": {
			Type:        "string",
			Description: "Which 50/30/20 budget bucket THIS SPECIFIC transaction belongs to, based on the necessity described in the message itself — not a fixed rule for the category in general. Example: \"abis ini beli sepatu baru soalnya yang lama udah jebol\" (buying new shoes because the old ones broke) is \"needs\" — an urgent replacement — even though shoes/shopping is normally a \"wants\" category. Omit this field entirely if not confident; that's fine and falls back to a sensible default.",
			Enum:        []string{"needs", "wants", "savings", "none"},
		},
		"description": {
			Type:        "string",
			Description: "A short free-text summary of what this transaction was for, in the user's OWN words/language (e.g. \"gaji\", \"isi bensin motor suzuki\", \"beli jajan pasar pagi\") — fill this in whenever the message gives ANY specific detail beyond the bare transaction type, including for income/salary (\"gaji bulan ini\" -> description \"gaji\"), even though category_name also conceptually covers it; category_name is an internal English label never shown to the user, while this field is what shows up in transaction lists and confirmations, so leaving it blank loses real information. Only leave it empty when the message truly states nothing beyond amount and type (rare).",
		},
		"occurred_at_hint": {
			Type:        "string",
			Description: "ISO 8601 date (YYYY-MM-DD) the transaction actually happened on, computed from today's date and any relative time language in the message (\"yesterday\", \"kemarin\", \"3 days ago\"). Leave empty if no time is mentioned (assume today).",
		},
		"account_name": {
			Type:        "string",
			Description: "Which of the user's own tracked accounts/wallets this applies to, ONLY when the message explicitly names one (e.g. \"beli kopi 20rb pake OVO\" -> \"OVO\", \"gaji masuk ke BCA\" -> \"BCA\"). Leave empty to use their default account — never guess an account that wasn't actually mentioned.",
		},
	}
}

// TransactionTools declares the nine callable tools every native-function-
// calling provider offers the model on every request — the direct analog
// of transaction_prompt.go's Step 1 intent list, except each entry's
// Description here plays the exact role the "jika user menanyakan tentang
// A, panggil tools A" instruction pattern plays in a configurable
// multi-agent platform: the model reads these descriptions and picks a
// tool, the same classification job Step 1 used to spell out in one big
// prompt.
func TransactionTools() []Tool {
	return []Tool{
		{
			Name:        ToolRecordTransaction,
			Description: "Call this when the message reports a transaction that already happened — money spent, earned, invested, transferred, or received as a reward (e.g. \"lunch 25000\", \"gaji 5000000\", \"makan siang tadi 30rb\"). This is the default for anything describing something that happened. If the message lists SEVERAL separate transactions (e.g. an itemized list: \"bensin 300k, makan siang 300k, makan malam 100k\"), call this tool ONCE PER TRANSACTION, in the order mentioned — do not merge them into a single total, and do not drop any of them. Do NOT call this for a transaction the message says REPEATS on a schedule (\"tiap bulan\", \"setiap tanggal 5\", \"every month\") — use create_recurring_transaction (auto-records each time) or create_bill_reminder (just notifies) instead.",
			Properties:  transactionProperties(),
			Required:    []string{"type", "amount", "currency", "category_name"},
		},
		{
			Name:        ToolQuerySpending,
			Description: "Call this when the message is a QUESTION about past spending/income/investment over some time period, not a new transaction (e.g. \"how much have I spent this month?\", \"dalam satu bulan ini aku udah ngeluarin duit berapa?\"). Never call this and record_transaction for the same message.",
			Properties: map[string]ToolProperty{
				"query_period": {
					Type:        "string",
					Description: "Whichever period the message states or implies. Default to \"monthly\" if unstated or unclear — there is no way to ask a follow-up, so never leave this empty.",
					Enum:        []string{"daily", "weekly", "monthly", "yearly"},
				},
				"query_category": {
					Type:        "string",
					Description: "Optional category filter, in English (e.g. \"berapa aku keluar buat makan bulan ini?\" -> \"food\"). Leave empty for spending in general. ALSO set this (or query_bucket, whichever fits) when the current message is a short confirmation (\"iya\", \"iya tolong\", \"yes please\", \"boleh\", \"ya udah\") replying to the ASSISTANT'S OWN PREVIOUS MESSAGE offering to break spending down by a specific category or budget bucket — resolve it from that offer, a confirmation like this is never asking for a plain total. Set this to \"uncategorized\" (regardless of the message's own language, e.g. Indonesian \"tanpa kategori\") when the message is asking about a transaction/line item YOU previously showed with no category name at all (labeled \"Uncategorized\"/\"Tanpa kategori\" in that prior reply) — this is a real, answerable query the server recognizes, not an invalid category; never treat such a follow-up as too ambiguous to call this tool over. Do NOT use this for a SPECIFIC merchant/place/item/person named by name (e.g. \"starbucks\", \"indomaret\", \"Budi\") — that's query_search_term instead, never a category guess.",
				},
				"query_bucket": {
					Type:        "string",
					Description: "Set this instead of query_category when the message asks about spending in a 50/30/20 budget bucket AS A WHOLE, not one specific category — e.g. \"berapa pengeluaran kebutuhanku?\" (needs), \"how much did I spend on wants?\", or a confirmation replying to the assistant's own previous message that just flagged/offered a Needs/Wants/Savings breakdown (see query_category's note on resolving such confirmations). Leave empty otherwise.",
					Enum:        []string{"needs", "wants", "savings"},
				},
				"query_mode": {
					Type:        "string",
					Description: "Set to \"list\" when the message asks to SEE or LIST the actual individual transactions/purchases, not a sum — e.g. \"beli apa aja buat food bulan ini?\" (what did I buy for food this month?), \"listing semua pengeluaran untuk makanan\", \"rincian pengeluaran wants\", \"what did I buy this month\". Leave empty (defaults to a plain total/breakdown) for a \"how much\" style question — that's the far more common case.",
					Enum:        []string{"total", "list"},
				},
				"query_list_more": {
					Type:        "boolean",
					Description: "Only meaningful when query_mode is \"list\". Set true when this message is asking to continue/see more of a list YOU (the assistant) just showed in your immediately previous reply — e.g. \"lanjutkan\", \"lanjut dong\", \"ada lagi?\", \"show me more\", \"next\" — rather than starting a brand-new list request. When true, still set query_period/query_category/query_bucket to the SAME values you used for that previous list (the server tracks exactly which page comes next on its own — never try to guess or state an offset/page number yourself). Leave false/omitted for a fresh list request.",
				},
				"query_start_date": {
					Type:        "string",
					Description: "ISO 8601 date (YYYY-MM-DD) to use as the query's START boundary INSTEAD of query_period's own default start — set this whenever the message specifies or implies a starting point that query_period's four fixed values (daily/weekly/monthly/yearly, each anchored to the CURRENT day/week/month/year) can't express on their own. Examples: \"mulai dari bulan lalu\"/\"starting from last month\" -> last month's 1st (still set query_period, e.g. \"monthly\", as the closest fit — query_start_date is what actually controls the start); \"sejak tanggal 15\" -> the 15th of the current month; \"since January\" -> Jan 1st of the current year; \"dari awal tahun lalu\" -> Jan 1st of last year. The end of the range is always today regardless of this field. Leave empty for a plain period query with no explicit custom start stated — that's the common case.",
				},
				"query_transaction_type": {
					Type:        "string",
					Description: "Restricts the query to ONE transaction type, or \"all\" for no restriction at all. Leave empty for the default: \"expense\" (the overwhelmingly common case — a bare \"how much did I spend\"/\"list my transactions\" question with no type stated means expenses). Set to \"all\" when the message EXPLICITLY asks for every transaction regardless of type — e.g. \"list semua transaksi\" (list ALL transactions, not just spending), \"show me everything this month\", \"riwayat transaksi lengkap\". Set to a specific other type (\"income\", \"investment\", \"transfer\", \"reward\") when the message asks specifically about that type instead of expenses — e.g. \"berapa pemasukan aku bulan ini?\" (how much income this month) -> \"income\". Only meaningful for a plain total or a \"list\" query_mode — never combine a non-expense value with query_category or query_bucket (both are inherently about spending/budget categories, not income or other types).",
					Enum:        []string{"expense", "income", "investment", "transfer", "reward", "all"},
				},
				"query_search_term": {
					Type:        "string",
					Description: "Set this when the message asks about a SPECIFIC MERCHANT, PLACE, ITEM, or PERSON named in past transactions — not a general spending category (e.g. \"cari transaksi starbucks\", \"pernah beli di indomaret bulan ini?\", \"transaksi ke Budi apa aja\"). Searches the free-text description of what was actually bought/for, never the internal category. Leave empty for a general category question (\"berapa pengeluaran makan?\") — that's query_category instead, never this field. Setting this implies a list of the matching transactions, the same as query_mode=\"list\" — still set query_mode to \"list\" too when you set this.",
				},
			},
			Required: []string{"query_period"},
		},
		{
			Name:        ToolGetBalance,
			Description: "Call this when the message asks what the user's current balance / how much money they have right now is — not scoped to any time period or category (e.g. \"what's my balance?\", \"berapa saldo aku sekarang?\", \"sisa uang aku berapa?\", \"saldo di Gopay berapa?\").",
			Properties: map[string]ToolProperty{
				"account_name": {
					Type:        "string",
					Description: "Set this ONLY when the message asks about ONE SPECIFIC named account/wallet's balance (e.g. \"saldo di Gopay berapa?\" -> \"Gopay\", \"how much is left in my BCA account?\" -> \"BCA\"). Leave empty for the default, far more common question: total balance across ALL of the user's accounts combined.",
				},
			},
		},
		{
			Name:        ToolMoveMoney,
			Description: "Call this ONLY when money moves between TWO OF THE USER'S OWN tracked FINANCIAL accounts/wallets — e.g. moving savings from a bank to an e-wallet, topping up an e-wallet from a bank balance (\"pindahin 200rb dari BCA ke Gopay\", \"topup OVO 50rb dari BCA\", \"aku pindah 1 juta dari tabungan ke dompet digital\"). Both from_account and to_account must be actual financial institutions/wallets (a bank name, an e-wallet brand, \"cash\"/\"tunai\", \"tabungan\", etc.) — NEVER a person's name, even when a real account is ALSO named in the same message and even with phrasing like \"pindahin\"/\"transfer dari\"/\"kirim dari\" that superficially matches this tool's own examples. If EITHER side is a person rather than a financial account (e.g. \"pindahin 100rb dari BCA ke Dania\", \"transfer ke adek 500rb\", \"bayar ke toko\", \"kirim ke Pak Budi\") — even one named right after a real account like BCA/Gopay — that entire message is record_transaction with type=\"transfer\" instead, completely unchanged, with NO account fields set. When genuinely unsure whether a name is a wallet or a person, prefer record_transaction's type=\"transfer\" — it's the far more common case and the safer default. from_account and to_account must both be given and must name two DIFFERENT accounts.",
			Properties: map[string]ToolProperty{
				"from_account": {
					Type:        "string",
					Description: "The financial account/wallet the money is moving OUT of (e.g. \"BCA\") — never a person's name.",
				},
				"to_account": {
					Type:        "string",
					Description: "The financial account/wallet the money is moving INTO (e.g. \"Gopay\") — never a person's name, even if a real account was named as from_account in the same message.",
				},
				"amount": {
					Type:        "number",
					Description: "A positive number. Never guess a number that wasn't stated or clearly implied.",
				},
				"currency": {
					Type:        "string",
					Description: "ISO 4217 code (e.g. IDR, USD). If not mentioned, use the caller-provided default currency.",
				},
				"description": {
					Type:        "string",
					Description: "A short free-text note about this transfer, in the user's own words, if the message gives one (e.g. \"topup buat jajan\"). Leave empty if the message states nothing beyond the accounts and amount.",
				},
				"occurred_at_hint": {
					Type:        "string",
					Description: "ISO 8601 date (YYYY-MM-DD) this move actually happened on, computed from today's date and any relative time language in the message. Leave empty if no time is mentioned (assume today).",
				},
			},
			Required: []string{"from_account", "to_account", "amount"},
		},
		{
			Name:        ToolRecordLoan,
			Description: "Call this ONLY when the message explicitly frames money as a LOAN to or from a specific PERSON — something expected to be paid back (\"pinjemin\", \"minjem\", \"utangin\", \"lend\", \"borrow\", \"owe\"), e.g. \"aku pinjemin Budi 500rb\" (I lent Budi 500k), \"aku minjem ke Dania 300rb buat bayar listrik\" (I borrowed 300k from Dania). Do NOT call this for a one-off gift, payment, or transfer to someone with no loan/repayment framing at all (\"aku kasih duit ke Budi 100rb buat jajan\", \"transfer ke adek 500rb\", \"bayar ke toko\") — those are record_transaction with type=\"transfer\" instead, completely unchanged. counterparty must be a PERSON's name, never a bank/wallet (that's move_money/record_transaction's account_name instead).",
			Properties: map[string]ToolProperty{
				"direction": {
					Type:        "string",
					Description: "\"lent\" if the user gave money to counterparty (they now owe the user); \"borrowed\" if the user received money from counterparty (the user now owes them).",
					Enum:        []string{"lent", "borrowed"},
				},
				"counterparty": {
					Type:        "string",
					Description: "The person's name, in the user's own words (e.g. \"Budi\", \"Dania\", \"adek\"). Never a bank or e-wallet name.",
				},
				"amount": {
					Type:        "number",
					Description: "A positive number. Never guess a number that wasn't stated or clearly implied.",
				},
				"currency": {
					Type:        "string",
					Description: "ISO 4217 code (e.g. IDR, USD). If not mentioned, use the caller-provided default currency.",
				},
				"description": {
					Type:        "string",
					Description: "A short free-text note, in the user's own words, if the message gives one (e.g. \"buat bayar listrik\"). Leave empty if the message states nothing beyond the person and amount.",
				},
				"occurred_at_hint": {
					Type:        "string",
					Description: "ISO 8601 date (YYYY-MM-DD) this happened on, computed from today's date and any relative time language in the message. Leave empty if no time is mentioned (assume today).",
				},
				"account_name": {
					Type:        "string",
					Description: "Which of the user's own tracked accounts/wallets the cash moved from/into, ONLY when the message explicitly names one (e.g. \"dari BCA\" -> \"BCA\"). Leave empty to use their default account.",
				},
			},
			Required: []string{"direction", "counterparty", "amount"},
		},
		{
			Name:        ToolRecordRepayment,
			Description: "Call this ONLY when the message describes repaying, or being repaid for, an EXISTING loan already tracked via record_loan — e.g. \"Budi bayar utang 200rb\" (Budi repaid the user 200k), \"aku bayar utang ke Dania 100rb\" (the user repaid Dania 100k), \"Budi udah lunasin utangnya\" (Budi fully settled what they owed). A brand new one-off payment with no prior loan context is record_transaction instead, never this tool. counterparty must be a PERSON's name, never a bank/wallet.",
			Properties: map[string]ToolProperty{
				"direction": {
					Type:        "string",
					Description: "\"received\" if counterparty is paying the user back (the user's receivable from them shrinks); \"made\" if the user is paying counterparty back (what the user owes them shrinks).",
					Enum:        []string{"received", "made"},
				},
				"counterparty": {
					Type:        "string",
					Description: "The person's name, exactly as used when the loan was first recorded if possible (e.g. \"Budi\").",
				},
				"amount": {
					Type:        "number",
					Description: "A positive number. Never guess a number that wasn't stated or clearly implied.",
				},
				"currency": {
					Type:        "string",
					Description: "ISO 4217 code (e.g. IDR, USD). If not mentioned, use the caller-provided default currency.",
				},
				"description": {
					Type:        "string",
					Description: "A short free-text note, if the message gives one. Leave empty otherwise.",
				},
				"occurred_at_hint": {
					Type:        "string",
					Description: "ISO 8601 date (YYYY-MM-DD) this happened on. Leave empty if no time is mentioned (assume today).",
				},
				"account_name": {
					Type:        "string",
					Description: "Which of the user's own tracked accounts/wallets the cash moved from/into, ONLY when explicitly named. Leave empty to use their default account.",
				},
			},
			Required: []string{"direction", "counterparty", "amount"},
		},
		{
			Name:        ToolCheckDebts,
			Description: "Call this when the message asks about outstanding loans — who owes the user money, who the user owes, or how much (e.g. \"siapa yang masih utang ke aku?\", \"utangku ke Dania berapa?\", \"do I owe anyone money?\").",
			Properties: map[string]ToolProperty{
				"counterparty": {
					Type:        "string",
					Description: "Set this ONLY when the message asks about ONE SPECIFIC named person's debt (e.g. \"utangku ke Dania berapa?\" -> \"Dania\"). Leave empty for the default, more common question: list everyone who owes the user and everyone the user owes.",
				},
			},
		},
		{
			Name:        ToolCorrectTransaction,
			Description: "Call this when the message is fixing a mistake in one of the user's own previous messages — correction language like \"typo\", \"salah\", \"harusnya\", \"maksudnya\", \"koreksi\" (Indonesian) or \"sorry I meant\", \"should be\", \"correction\", \"my bad\", \"oops\" (English), e.g. \"eh maaf, harusnya 1jt deh untuk reksa dana kemarin, typo\". Describe the CORRECTED transaction in full here (same fields as record_transaction, not a diff) — the caller replaces the existing entry with this one instead of adding a second one. IMPORTANT: the mistake may be in ANY of the user's recent transactions, not necessarily the newest one — e.g. several transactions were just recorded from one message, or the correction refers to something further back. Unless it's obviously the single most recent transaction, call get_recent_transactions first and set target_transaction_id to whichever candidate actually matches what this message describes as wrong, instead of assuming it's the latest.",
			Properties:  correctTransactionProperties(),
			Required:    []string{"type", "amount", "currency", "category_name"},
		},
		{
			Name:        ToolDeleteTransaction,
			Description: "Call this when the message asks to DELETE / remove / cancel one of the user's existing transactions entirely — not fix it, just remove it (e.g. \"hapus transaksi makan siang tadi\", \"batalin yang bensin\", \"delete the fuel expense\", \"remove that transaction\", \"cancel it\"). Never call this together with record_transaction or correct_transaction for the same message — a message either adds, fixes, or removes a transaction, never more than one of those at once. IMPORTANT: identify WHICH existing transaction to delete the same way correct_transaction does — unless it's obviously the single most recent transaction, call get_recent_transactions first and set target_transaction_id to whichever candidate matches what the message describes wanting removed, instead of assuming it's the latest.",
			Properties: map[string]ToolProperty{
				"target_transaction_id": targetTransactionIDProperty("deleted"),
			},
		},
		{
			Name:        ToolCreateRecurringTransaction,
			Description: "Call this when the message describes a transaction that REPEATS on a regular monthly schedule AND wants it recorded automatically each time — rent, a subscription, a salary that arrives on the same day every month (e.g. \"netflix 150rb setiap tanggal 5\", \"gaji 5 juta tiap tanggal 25\", \"sewa kos 800rb per bulan tanggal 1\", \"internet bill 300k every month on the 10th\"). Only call this when the message ITSELF says the transaction repeats (\"tiap\"/\"setiap\"/\"per bulan\"/\"every month\"/\"monthly\") — a one-off mention of something that happens to sound recurring (\"bayar internet bulan ini 300rb\" with no repeating cadence stated) is a normal record_transaction instead. If the message instead asks to just be REMINDED/notified about the bill without recording it automatically (\"ingetin\", \"remind me\", \"kasih tau\" — they'll log the actual payment themselves), use create_bill_reminder instead. IMPORTANT: this does NOT record anything happening today — it only schedules future occurrences starting from the NEXT time that day of the month comes around, never the current one. Never call this together with record_transaction for the same message — if the user separately wants this month's occurrence recorded too, that's a different message. EXCEPTION: if the message describes an UPDATE to an EXISTING recurring transaction/bill reminder (e.g. \"ubah netflix jadi 200rb\", \"netflix ganti jadi 200rb per bulan\", \"pindahin tanggal sewa ke tanggal 10\") — there is no dedicated update tool, so call delete_recurring_transaction (for the old one) AND this tool (describing the new/changed schedule in full) TOGETHER in the same message; that combination replaces the old one instead of leaving two active.",
			Properties:  recurringTransactionProperties(),
			Required:    []string{"type", "amount", "currency", "category_name", "day_of_month"},
		},
		{
			Name:        ToolCreateBillReminder,
			Description: "Call this when the message asks to be REMINDED/notified about a recurring bill or payment on a monthly schedule, WITHOUT it being recorded automatically — the user will log the actual payment themselves when it happens (e.g. \"ingetin bayar listrik 500rb tiap tanggal 5\", \"remind me about rent due the 1st, don't record it for me\", \"kasih tau kalau tagihan internet 300rb jatuh tempo tiap tanggal 10\"). Look for reminder language (\"ingetin\"/\"kasih tau\"/\"remind me\"/\"notify me\") — if the message just describes a repeating transaction with no reminder language, that's create_recurring_transaction instead (it auto-records; this only notifies). Never call this together with record_transaction or create_recurring_transaction for the same message. EXCEPTION: if the message describes an UPDATE to an EXISTING recurring transaction/bill reminder — there is no dedicated update tool, so call delete_recurring_transaction (for the old one) AND this tool (describing the new/changed schedule) TOGETHER in the same message to replace it, exactly like create_recurring_transaction's own exception.",
			Properties:  recurringTransactionProperties(),
			Required:    []string{"type", "amount", "currency", "category_name", "day_of_month"},
		},
		{
			Name:        ToolCreateSavingsGoal,
			Description: "Call this when the message describes wanting to SAVE UP toward a specific target amount for a specific purpose — a savings goal (e.g. \"aku mau nabung 10 juta buat laptop sampai desember\", \"I want to save 5,000,000 for an emergency fund\", \"target nabung liburan 3 juta di bulan Juni\"). This creates a new goal to track progress against, REPLACING any existing active goal — only one goal is active at a time. Do NOT call this for a one-off savings/investment transaction that already happened with no target/goal mentioned (\"nabung 500rb hari ini\" alone) — that's a normal record_transaction with type=investment or bucket=savings instead; only use this when the message frames it as a target/goal to save TOWARD.",
			Properties:  savingsGoalProperties(),
			Required:    []string{"name", "target_amount", "currency"},
		},
		{
			Name:        ToolDeleteRecurringTransaction,
			Description: "Call this when the message asks to CANCEL / stop / delete one of the user's existing RECURRING transactions or bill reminders entirely — a repeating schedule, not a one-off transaction (e.g. \"batalin netflix\", \"cancel my rent reminder\", \"stop the gym subscription\", \"hapus tagihan internet yang berulang\"). Use delete_transaction instead for a one-off past transaction. IMPORTANT: identify WHICH recurring transaction to cancel — call get_recurring_transactions first and set target_recurring_transaction_id to whichever candidate matches what the message describes (by category/amount/description); do this even when there's only one active one, so the id is real rather than guessed. Never call this together with record_transaction or delete_transaction for the same message. EXCEPTION: if the message describes an UPDATE to an existing recurring transaction/bill reminder rather than canceling it outright (e.g. \"ubah netflix jadi 200rb\", \"ganti tanggal sewa ke tanggal 10\") — there is no dedicated update tool, so call this tool for the OLD schedule's id AND create_recurring_transaction or create_bill_reminder (describing the new/changed schedule in full) TOGETHER in the same message; that combination replaces the old one instead of just deleting it with nothing to replace it.",
			Properties: map[string]ToolProperty{
				"target_recurring_transaction_id": {
					Type:        "number",
					Description: "The id of the specific recurring transaction or bill reminder being canceled, taken from a prior get_recurring_transactions lookup. Required — never leave this at 0 or guess one.",
				},
			},
			Required: []string{"target_recurring_transaction_id"},
		},
		{
			Name:        ToolSubscribeToPro,
			Description: "Call this when the message expresses wanting to upgrade THIS BOT's own account to Pro tier, in any phrasing (\"aku mau upgrade ke pro\", \"gimana caranya jadi member pro?\", \"mau langganan pro dong\", \"I want to go premium\", \"how do I upgrade?\", \"cara jadi pro gimana?\"). This is about the bot's own Pro tier specifically — never confuse it with a recurring transaction the user tracks for themselves (a gym membership, Netflix, etc; that's create_recurring_transaction/delete_recurring_transaction instead). Takes no parameters.",
			Properties:  map[string]ToolProperty{},
		},
		{
			Name:        ToolCheckSubscriptionStatus,
			Description: "Call this when the message asks about the user's OWN current Pro-tier status or renewal date on this bot (\"aku pro ga sih?\", \"kapan langganan pro ku abis?\", \"am I still Pro?\", \"when does my subscription renew?\", \"status pro aku gimana?\"). Not for a recurring transaction the user tracks for themselves — that's a normal query_spending/get_recurring_transactions question instead. Takes no parameters.",
			Properties:  map[string]ToolProperty{},
		},
		{
			Name:        ToolCheckBudgetAllocation,
			Description: "Call this when the message asks about the user's configured 50/30/20 budget ALLOCATION/SPLIT itself — what percentage of their money is assigned to Needs/Wants/Savings (e.g. \"pembagian budgeting ku gimana?\", \"budget split aku berapa?\", \"what's my budget allocation?\", \"how is my budget divided?\", \"needs/wants/savings aku berapa persen?\"). This is DIFFERENT from query_spending's query_bucket — that tool answers \"how much have I SPENT in a bucket,\" this one answers \"what percentages are my buckets SET TO.\" A plain \"gimana budgetku\"/\"how's my budget\" with no mention of spending amounts means this tool, not query_spending. At most ONE of against_latest_income or hypothetical_amount should be set — never both.",
			Properties: map[string]ToolProperty{
				"against_latest_income": {
					Type:        "boolean",
					Description: "Set true when the message asks for the allocation applied to their most recently recorded salary/income transaction, rather than just the bare percentages — e.g. \"budget dari gaji kemarin gimana?\", \"berdasarkan gaji terakhir, budgetku berapa?\", \"based on my last salary what's my needs/wants/savings split\". This always uses the user's actual last-recorded income, never an amount typed in the message itself (that's hypothetical_amount instead). Leave false/omit for a plain \"what's my split\" question with no income involved — that's the far more common case.",
				},
				"hypothetical_amount": {
					Type:        "number",
					Description: "Set this when the message states a SPECIFIC what-if amount to apply the percentages to, regardless of what's actually recorded — e.g. \"kalau gajiku 5 juta, berapa masing-masing budgetnya?\", \"if I had 10 million, what would my needs/wants/savings be?\", \"misalnya pemasukan 3 juta, budgetnya gimana\". Leave empty/omit when no specific amount is stated in the message itself — that's against_latest_income (for their real last income) or neither (for the bare percentages) instead.",
				},
				"hypothetical_currency": {
					Type:        "string",
					Description: "ISO 4217 code for hypothetical_amount (e.g. IDR, USD). Only meaningful alongside hypothetical_amount. If not mentioned, use the caller-provided default currency.",
				},
			},
		},
		{
			Name:        ToolSetBudgetAllocation,
			Description: "Call this when the message asks to CHANGE/SET the user's 50/30/20 budget allocation — this is a SET action, never confuse with check_budget_allocation (which only reports the current split, never changes it). The new split may be stated as percentages (e.g. \"ubah budgetku jadi 60 needs, 20 wants, 20 savings\", \"set my split to 50/30/20\") OR as nominal money amounts (e.g. \"kalau mau ngubah savings jadi 5juta, needs 7juta, wants 4.5juta bisa enggak?\", \"set needs to 2 million, wants to 1 million, savings to 1 million\") — use whichever form the message actually states, never convert one to the other yourself. Fill EITHER the three *_percent fields OR the three *_amount fields (plus currency), never both.",
			Properties: map[string]ToolProperty{
				"needs_percent": {
					Type:        "number",
					Description: "The new Needs percentage, only when the message states the split as percentages directly. Must be used together with wants_percent and savings_percent (all three, summing to 100) — never just one or two.",
				},
				"wants_percent": {
					Type:        "number",
					Description: "The new Wants percentage — see needs_percent.",
				},
				"savings_percent": {
					Type:        "number",
					Description: "The new Savings percentage — see needs_percent.",
				},
				"needs_amount": {
					Type:        "number",
					Description: "The new Needs amount in nominal money terms, only when the message states the split as amounts rather than percentages (e.g. \"needs 7juta\"). Must be used together with wants_amount and savings_amount (all three) — the caller converts these to percentages of their own sum itself, so they do NOT need to sum to any particular total (your recorded income, 100, or anything else) — just state exactly what the message says.",
				},
				"wants_amount": {
					Type:        "number",
					Description: "The new Wants amount — see needs_amount.",
				},
				"savings_amount": {
					Type:        "number",
					Description: "The new Savings amount — see needs_amount.",
				},
				"budget_set_currency": {
					Type:        "string",
					Description: "ISO 4217 code for the *_amount fields (e.g. IDR, USD). Only meaningful alongside those. If not mentioned, use the caller-provided default currency.",
				},
			},
		},
		{
			Name:        ToolCheckSurplus,
			Description: "Call this when the message asks about last month's SURPLUS specifically — income minus expense for the PREVIOUS calendar month only, as a single net figure (e.g. \"surplusku bulan lalu berapa?\", \"berapa surplus bulan lalu?\", \"ada sisa uang ga bulan lalu?\", \"last month's surplus?\"). This is DIFFERENT from query_spending — that tool can total/list/break down spending over any stated period, but has no concept of \"income minus expense for exactly one specific prior month\" as a single answer; a plain query_spending call (even with query_start_date/query_transaction_type set to approximate this) would answer a different, broader question. Use this tool whenever the message's own word is specifically \"surplus\" (or a clear synonym like \"sisa uang\"/\"leftover\") about last month, not a generic spending/income total. Takes no parameters.",
			Properties:  map[string]ToolProperty{},
		},
	}
}

// ToolCallingSystemInstruction is the short counterpart to
// BuildTransactionSystemPrompt: notice everything about *output format*
// (the exact JSON envelope, which fields belong to which intent, the
// "return ONLY a JSON object" instruction) is gone — the tool declarations
// above and each provider's own function-calling contract carry that
// weight now. All that's left here is domain knowledge with no natural
// home in a JSON schema: today's date, the default currency, and what to
// do when nothing fits (reply in plain text with a clarifying question
// instead of calling a tool — no tool call at all is itself the
// "ambiguous" signal, rather than a dedicated is_ambiguous field).
func ToolCallingSystemInstruction(defaultCurrency string, today time.Time, preferredLanguage string) string {
	return fmt.Sprintf(
		`You are a financial transaction parser and spending-report assistant for an expense-tracking bot.
Today's date is %s (YYYY-MM-DD). The user's default currency is %s.

Decide which tool (if any) matches the user's message. Call record_transaction once per DISTINCT transaction the message describes — most messages describe exactly one, but call it multiple times for a message listing several (see its own description for an example). Every OTHER tool (query_spending, get_balance, correct_transaction, delete_transaction, create_recurring_transaction, create_bill_reminder, create_savings_goal, delete_recurring_transaction, subscribe_to_pro, check_subscription_status) may be called AT MOST ONCE per message — never call more than one of those, and never mix them with record_transaction in the same message — with exactly ONE exception: delete_recurring_transaction MAY be paired with create_recurring_transaction or create_bill_reminder in the same message when the message describes an UPDATE to an existing recurring transaction/bill reminder rather than a plain cancellation (see each of those three tools' own description for exactly when). If the message is missing the amount, or is too vague to safely call any tool (e.g. no discernible transaction and no discernible question), do NOT call a tool — instead reply with a short, polite question asking for the missing detail, written in %s regardless of what language the user's message was in. Do NOT invent values to force a call just to avoid asking.

Before treating a vague or incomplete message as its own isolated case, check the recent conversation history above: a user will often split one transaction across several consecutive messages instead of sending it all at once (e.g. "beli baju", then "sama sepatu", then "totalnya 320rb" — three messages describing one combined purchase). If the current message plausibly completes or adds to an unfinished transaction from those recent messages, combine them into a single record_transaction call instead of parsing the current message alone or asking a clarifying question that ignores what was already said. Only fall back to asking for clarification if the message still can't be resolved even combined with that recent context.

The same history check applies to a short confirmation with no content of its own ("iya", "iya tolong", "yes please", "boleh") — it almost always answers a question YOU (the assistant) just asked in the previous turn, not a fresh request in isolation. If your previous message offered to break down spending by a specific category or budget bucket, treat this confirmation as a query_spending call for that exact category/bucket (see query_spending's query_category and query_bucket fields), never as a generic total. Likewise, a short continuation request ("lanjutkan", "lanjut", "ada lagi?", "show me more", "next") right after you showed a numbered spending list means another query_spending call with query_mode="list", query_list_more=true, and the SAME period/category/bucket as that list — never a fresh, unfiltered request.

Any plain-text reply you write yourself (a clarifying question, or a free-text answer built from a lookup tool's results) is sent to Telegram as-is — it supports only single-asterisk *bold* for emphasis, nothing else. NEVER use double-asterisk **bold** (GitHub/CommonMark style) — Telegram displays the literal asterisks instead of bold text. Never use markdown headers (#), bullet dashes, or nested formatting either. Keep it as plain conversational prose with at most a simple numbered list (e.g. "1. ...") when actually listing multiple items; reserve *single-word* emphasis for when it's truly needed, not as decoration.`,
		today.UTC().Format("2006-01-02"),
		defaultCurrency,
		toolCallingLanguageName(preferredLanguage),
	)
}

func toolCallingLanguageName(code string) string {
	if strings.EqualFold(strings.TrimSpace(code), "id") {
		return "Bahasa Indonesia"
	}
	return "English"
}
