package ai

import "aura-saku/internal/ai/prompt"

// ParsedTransactionFromToolCall maps a native function/tool call — a tool
// name plus its already-decoded argument map — onto the shared
// ParsedTransaction shape ValidateParsedTransaction and bot.Service
// already expect. Shared by all three native-function-calling providers
// (gemini/openai/anthropic) once each has reduced its own wire response
// (Gemini's args map, OpenAI's JSON-string arguments after one
// json.Unmarshal, Anthropic's input object) down to a plain
// map[string]interface{} — this is the one place that mapping logic lives
// instead of being duplicated three times.
func ParsedTransactionFromToolCall(name string, args map[string]interface{}) *ParsedTransaction {
	switch name {
	case prompt.ToolRecordTransaction:
		pt := transactionFromArgs(args)
		pt.Intent = IntentRecord
		return pt
	case prompt.ToolCorrectTransaction:
		pt := transactionFromArgs(args)
		pt.Intent = IntentCorrect
		pt.TargetTransactionID = argUint(args, "target_transaction_id")
		return pt
	case prompt.ToolQuerySpending:
		return &ParsedTransaction{
			Intent:               IntentQuery,
			QueryPeriod:          argString(args, "query_period"),
			QueryCategory:        argString(args, "query_category"),
			QueryBucket:          argString(args, "query_bucket"),
			QueryMode:            argString(args, "query_mode"),
			QueryListMore:        argBool(args, "query_list_more"),
			QueryStartDate:       argString(args, "query_start_date"),
			QueryTransactionType: argString(args, "query_transaction_type"),
			QuerySearchTerm:      argString(args, "query_search_term"),
		}
	case prompt.ToolGetBalance:
		return &ParsedTransaction{Intent: IntentBalance, AccountName: argString(args, "account_name")}
	case prompt.ToolDeleteTransaction:
		return &ParsedTransaction{
			Intent:              IntentDelete,
			TargetTransactionID: argUint(args, "target_transaction_id"),
		}
	case prompt.ToolCreateRecurringTransaction:
		pt := transactionFromArgs(args)
		pt.Intent = IntentRecurringCreate
		pt.DayOfMonth = argInt(args, "day_of_month")
		return pt
	case prompt.ToolCreateBillReminder:
		pt := transactionFromArgs(args)
		pt.Intent = IntentRecurringCreate
		pt.DayOfMonth = argInt(args, "day_of_month")
		pt.ReminderOnly = true
		return pt
	case prompt.ToolCreateSavingsGoal:
		return &ParsedTransaction{
			Intent:           IntentSavingsGoalCreate,
			GoalName:         argString(args, "name"),
			Amount:           argFloat(args, "target_amount"),
			Currency:         argString(args, "currency"),
			GoalDeadlineHint: argString(args, "deadline_hint"),
		}
	case prompt.ToolDeleteRecurringTransaction:
		return &ParsedTransaction{
			Intent:                       IntentRecurringDelete,
			TargetRecurringTransactionID: argUint(args, "target_recurring_transaction_id"),
		}
	case prompt.ToolSubscribeToPro:
		return &ParsedTransaction{Intent: IntentSubscribe}
	case prompt.ToolCheckSubscriptionStatus:
		return &ParsedTransaction{Intent: IntentSubscriptionStatus}
	case prompt.ToolCheckBudgetAllocation:
		return &ParsedTransaction{
			Intent:                     IntentBudgetAllocation,
			BudgetAgainstLatestIncome:  argBool(args, "against_latest_income"),
			BudgetHypotheticalAmount:   argFloat(args, "hypothetical_amount"),
			BudgetHypotheticalCurrency: argString(args, "hypothetical_currency"),
		}
	case prompt.ToolSetBudgetAllocation:
		return &ParsedTransaction{
			Intent:                  IntentBudgetSet,
			BudgetSetNeedsPercent:   argFloat(args, "needs_percent"),
			BudgetSetWantsPercent:   argFloat(args, "wants_percent"),
			BudgetSetSavingsPercent: argFloat(args, "savings_percent"),
			BudgetSetNeedsAmount:    argFloat(args, "needs_amount"),
			BudgetSetWantsAmount:    argFloat(args, "wants_amount"),
			BudgetSetSavingsAmount:  argFloat(args, "savings_amount"),
			BudgetSetCurrency:       argString(args, "budget_set_currency"),
		}
	case prompt.ToolCheckSurplus:
		return &ParsedTransaction{Intent: IntentSurplus}
	case prompt.ToolMoveMoney:
		return &ParsedTransaction{
			Intent:          IntentAccountTransfer,
			FromAccountName: argString(args, "from_account"),
			ToAccountName:   argString(args, "to_account"),
			Amount:          argFloat(args, "amount"),
			Currency:        argString(args, "currency"),
			Description:     argString(args, "description"),
			OccurredAtHint:  argString(args, "occurred_at_hint"),
		}
	case prompt.ToolRecordLoan:
		return &ParsedTransaction{
			Intent:           IntentDebtLend,
			DebtDirection:    argString(args, "direction"),
			DebtCounterparty: argString(args, "counterparty"),
			Amount:           argFloat(args, "amount"),
			Currency:         argString(args, "currency"),
			Description:      argString(args, "description"),
			OccurredAtHint:   argString(args, "occurred_at_hint"),
			AccountName:      argString(args, "account_name"),
		}
	case prompt.ToolRecordRepayment:
		return &ParsedTransaction{
			Intent:           IntentDebtRepay,
			DebtDirection:    argString(args, "direction"),
			DebtCounterparty: argString(args, "counterparty"),
			Amount:           argFloat(args, "amount"),
			Currency:         argString(args, "currency"),
			Description:      argString(args, "description"),
			OccurredAtHint:   argString(args, "occurred_at_hint"),
			AccountName:      argString(args, "account_name"),
		}
	case prompt.ToolCheckDebts:
		return &ParsedTransaction{
			Intent:           IntentDebtQuery,
			DebtCounterparty: argString(args, "counterparty"),
		}
	case prompt.ToolSetSpendingAlert:
		return &ParsedTransaction{
			Intent:       IntentSetSpendingAlert,
			AlertScope:   argString(args, "scope"),
			CategoryName: argString(args, "category_name"),
			Bucket:       argString(args, "bucket"),
			Amount:       argFloat(args, "threshold_amount"),
			QueryPeriod:  argString(args, "period"),
		}
	default:
		return nil
	}
}

func transactionFromArgs(args map[string]interface{}) *ParsedTransaction {
	return &ParsedTransaction{
		Type:           argString(args, "type"),
		Amount:         argFloat(args, "amount"),
		Currency:       argString(args, "currency"),
		CategoryName:   argString(args, "category_name"),
		Bucket:         argString(args, "bucket"),
		Description:    argString(args, "description"),
		OccurredAtHint: argString(args, "occurred_at_hint"),
		AccountName:    argString(args, "account_name"),
	}
}

func argString(args map[string]interface{}, key string) string {
	s, _ := args[key].(string)
	return s
}

func argFloat(args map[string]interface{}, key string) float64 {
	f, _ := args[key].(float64)
	return f
}

// argUint reads an id-like argument (a tool call's decoded JSON number,
// always float64 in Go) as a uint — negative or missing values (including
// the zero the model would send for "not applicable") collapse to 0, which
// TargetTransactionID's own doc comment defines as "unspecified".
func argUint(args map[string]interface{}, key string) uint {
	f, _ := args[key].(float64)
	if f <= 0 {
		return 0
	}
	return uint(f)
}

// argInt reads a plain integer-valued argument (again always float64 as
// decoded JSON) — unlike argUint, a missing or out-of-range value here is
// deliberately NOT collapsed to a "valid" sentinel: ValidateParsedTransaction
// is what rejects an out-of-range DayOfMonth, so this just truncates.
func argInt(args map[string]interface{}, key string) int {
	f, _ := args[key].(float64)
	return int(f)
}

// argBool reads a boolean-valued argument — a missing key (the common case,
// since a boolean tool property is only ever sent when true by most
// providers) collapses to false, the same "unspecified" default every other
// argN helper here uses for its own zero value.
func argBool(args map[string]interface{}, key string) bool {
	b, _ := args[key].(bool)
	return b
}
