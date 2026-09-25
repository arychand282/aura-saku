// Package logger wraps the standard logger to prefix every error/fatal log
// line with a stable error code, so a code seen in production logs can be
// grepped straight back to the exact call site and failure meaning below —
// no need to fingerprint free-text messages across log versions.
package logger

import "log"

type Code string

// Error code registry. Keep this list flat and exhaustive: when triaging a
// production incident, grep this file for the code shown in the log line.
const (
	CfgLoadFailed Code = "AURA-CFG-001" // config.Load failed: missing/invalid required env var

	DBConnectFailed   Code = "AURA-DB-001" // failed to open/ping the Postgres connection
	DBMigrationFailed Code = "AURA-DB-002" // AutoMigrate failed
	DBQueryFailed     Code = "AURA-DB-003" // a runtime GORM query/write failed outside of a service-specific code

	RedisConnectFailed Code = "AURA-REDIS-001" // failed to open/ping the Redis connection
	RedisOpFailed      Code = "AURA-REDIS-002" // a runtime Redis command failed (middleware fails open on this)

	AIClientBuildFailed Code = "AURA-AI-001" // ai/factory.NewAIClient failed (unknown/misconfigured AI_PROVIDER)
	AIParseFailed       Code = "AURA-AI-002" // AIClient.ParseTransactionText/TranscribeAndParseAudio returned an error

	ServerListenFailed   Code = "AURA-SRV-001" // fiber.App.Listen failed to bind
	ServerShutdownFailed Code = "AURA-SRV-002" // graceful shutdown did not complete cleanly within the timeout

	TelegramParseFailed    Code = "AURA-TG-001" // failed to unmarshal/normalize an inbound Telegram webhook payload
	TelegramSendFailed     Code = "AURA-TG-002" // sendMessage/sendChatAction to Telegram failed
	TelegramDownloadFailed Code = "AURA-TG-003" // downloading a Telegram voice note file failed

	SvcUserResolveFailed Code = "AURA-SVC-001" // get-or-create User by TelegramChatID failed
	SvcUndoFailed        Code = "AURA-SVC-002" // transaction.Service.UndoLast failed
	SvcSyncFailed        Code = "AURA-SVC-003" // reconciliation.Service.Sync failed
	SvcCreateTxFailed    Code = "AURA-SVC-004" // transaction.Service.CreateFromParsed failed
	SvcBudgetEvalFailed  Code = "AURA-SVC-005" // budget.Service.EvaluateBudget failed
	SvcReportQueryFailed Code = "AURA-SVC-006" // report.Service.Summary/CategoryTotal failed for a spending-query reply
)

// Error logs a recoverable error tagged with its code, e.g.:
// "[AURA-DB-003] get or create user for chat 12345: connection reset by peer"
func Error(code Code, msg string, err error) {
	log.Printf("[%s] %s: %v", code, msg, err)
}

// Fatal logs an unrecoverable error tagged with its code, then calls os.Exit(1)
// (via the standard log package) — use only for startup failures.
func Fatal(code Code, msg string, err error) {
	log.Fatalf("[%s] %s: %v", code, msg, err)
}
