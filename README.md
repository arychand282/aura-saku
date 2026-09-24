# Aura-Saku Backend

Telegram-based AI expense tracker SaaS — Go backend (Fiber + GORM + PostgreSQL + Redis), with a pluggable AI client (Gemini by default) and a messaging layer abstracted for future WhatsApp support.

All three AI providers (Gemini, OpenAI, Anthropic) are fully implemented against their REST APIs, transaction persistence, budgeting, and reconciliation are real (not stubbed), and every external service endpoint (AI providers, the FX rate provider) is configurable via `.env` rather than hardcoded — see [Configurable external endpoints](#configurable-external-endpoints) below.

## Prerequisites

- Go 1.25+ (or let `go build` auto-download the toolchain via `GOTOOLCHAIN=auto`, the default)
- Docker + Docker Compose (for the local Postgres/Redis/backend stack)
- A Telegram bot token from [@BotFather](https://t.me/BotFather)
- A Gemini API key from [Google AI Studio](https://aistudio.google.com/) (or an OpenAI/Anthropic key if you switch `AI_PROVIDER`)

## Setup

1. Copy the env file (already done for you if `.env` exists) and fill in the blanks:
   ```
   cp .env.example .env
   ```
   Required for the bot to actually do anything: `TELEGRAM_BOT_TOKEN`, `GEMINI_API_KEY` (or the equivalent for your chosen `AI_PROVIDER`). `DB_PASSWORD` and `TELEGRAM_WEBHOOK_SECRET` are pre-filled with generated dev-safe random values.

2. Run the stack:

   **Option A — Docker (recommended, matches production shape):**
   ```
   make docker-up
   ```
   This builds the backend image and starts Postgres, Redis, and the backend together. Migrations run automatically on boot.

   **Option B — Native Go, against Dockerized Postgres/Redis:**
   ```
   docker compose up postgres redis -d
   make migrate
   make run
   ```

3. Verify it's alive:
   ```
   curl http://localhost:8080/healthz
   ```
   Expect `{"status":"ok","db":"ok","redis":"ok"}`.

## Registering the Telegram webhook

Telegram needs a public HTTPS URL to POST updates to. For local dev, use a tunnel:
```
ngrok http 8080
# or: cloudflared tunnel --url http://localhost:8080
```
Set `TELEGRAM_WEBHOOK_URL` in `.env` to the tunnel's HTTPS URL + `/webhook/telegram`, then register it with Telegram:
```
curl "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/setWebhook?url=${TELEGRAM_WEBHOOK_URL}&secret_token=${TELEGRAM_WEBHOOK_SECRET}"
```
Expect `{"ok":true,"result":true}`. Message your bot on Telegram and watch `make docker-logs` (or your terminal, if running natively) for the incoming update.

## Project layout

```
cmd/server        entrypoint: config, db, redis, migrations, AI client, messenger, services, Fiber app
cmd/migrate       standalone migration CLI
internal/config   env loading + validation
internal/db       Postgres/Redis connections, AutoMigrate registration
internal/models   GORM domain models (users, transactions, budgets, reconciliation, usage logs, ...)
internal/ai       AIClient interface + gemini/openai/anthropic implementations + factory
internal/messaging Messenger interface + telegram/whatsapp implementations
internal/middleware  input length, rate limit, daily quota, recover/logger
internal/service  bot (command routing), transaction, budget, reconciliation, fx
internal/api      REST router + handlers for the future Next.js dashboard
internal/logger   error-code registry + logging helpers
pkg/httpresponse  JSON response envelope helpers
```

## Debugging production errors

Every error/fatal log line is prefixed with a stable code, e.g.:
```
[AURA-TG-002] send reply to chat 777: telegram sendMessage error (status 404): ...
```
Look up what a code means (and every code that exists) in `internal/logger/logger.go` — it's a flat, single-file registry, grep it by the code shown in the log.

## Timezone convention

Backend and database are **UTC everywhere**, enforced at the connection level rather than by convention alone: `internal/db/postgres.go` sets `TimeZone=UTC` on the Postgres DSN and overrides GORM's `NowFunc` to `time.Now().UTC()` (GORM's default uses server-local time for every `CreatedAt`/`UpdatedAt`/`DeletedAt`, which would otherwise silently leak the host machine's timezone into storage). Docker containers also set `TZ=UTC` as defense-in-depth. When writing new code, always call `time.Now().UTC()` explicitly rather than relying on ambient config — never assume the process is running in a UTC-timezone environment. Convert to a user's local time only at the display layer (Telegram replies, dashboard UI, CSV exports), using `User.TimezoneOffset`.

## Configurable external endpoints

Every external HTTP dependency's base URL is overridable via `.env` rather than hardcoded, because the FX provider has already changed domains once during development:

| Env var | Default | Purpose |
|---|---|---|
| `GEMINI_BASE_URL` | Google's Gemini API | Override for a proxy/regional endpoint |
| `OPENAI_BASE_URL` | `api.openai.com/v1` | Override for Azure OpenAI, LiteLLM/vLLM/Ollama, or a corporate proxy |
| `ANTHROPIC_BASE_URL` | `api.anthropic.com/v1` | Override for a Bedrock/Vertex-hosted Claude endpoint or a proxy |
| `ANTHROPIC_API_VERSION` | `2023-06-01` | Anthropic's `anthropic-version` header. Only covers a version-header bump — a real response-schema change would still need a code update in `internal/ai/anthropic` |
| `FX_PROVIDER_BASE_URL` | Frankfurter (`api.frankfurter.dev/v1`) | The live exchange-rate provider. Falls back to a static rate table on any failure regardless of which URL is configured |

Leave any of these blank to use the built-in default.

## What's implemented

Config/DB/Redis wiring, all 10 GORM models (including `Account`) + migrations, the full security middleware chain (length check → rate limit → daily quota, in that order, before any AI call), the Telegram webhook (fast-ack + async goroutine dispatch), UTC-everywhere timestamps, error-code-tagged logging (`internal/logger`).

**AI (`internal/ai`):** Gemini, OpenAI, and Anthropic all fully implemented against their REST APIs (no SDKs) — structured JSON output with fence-stripping, schema validation, and one repair retry, shared across all three providers (`ai.StripJSONFences`, `ai.ValidateParsedTransaction`). Gemini and OpenAI both support voice-note transcription (Gemini natively, OpenAI via Whisper); Anthropic has no native audio support and returns `apperror.ErrProviderAudioUnsupported`, which the bot surfaces as a clear message rather than a generic failure. The system prompt tells the model today's date and asks it to resolve relative time language ("yesterday", "3 days ago") into a strict ISO date itself, which `internal/service/transaction.resolveOccurredAt` then parses — offloading that reasoning to the model instead of pattern-matching natural language in Go.

**Transactions (`internal/service/transaction`):** persistence with category resolution (seeded with a best-effort Needs/Wants/Savings bucket guess, self-healing categories created before the heuristic existed), historical currency conversion via `internal/service/fx`, and `/undo` (soft-delete + `UndoHistory` snapshot).

**Budget (`internal/service/budget`):** 50/30/20 evaluation against this month's income and spend-by-bucket, over-allocation warnings, a spending-pace anomaly check for Wants, and end-of-month rollover suggestions.

**Reconciliation (`internal/service/reconciliation`):** `/sync` computes the calculated balance and, on a discrepancy, auto-records a blind-spot adjustment transaction (same "commit immediately, `/undo` to revert" pattern used elsewhere).

**FX (`internal/service/fx`):** `LiveService` fetches real historical rates from Frankfurter, falling back to a static rate table on any failure so a provider outage never blocks recording a transaction.

**Accounts (`internal/models.Account`, `internal/service/account`):** schema groundwork for multi-account support — every user gets one auto-created "Default" account, and every transaction/reconciliation is attributed to it. No bot/dashboard UX to create or select additional accounts yet (intentionally deferred); see the code comments on `Transaction.AccountID` and `reconciliation.balanceSign` for how a second account and cross-account transfers are expected to be modeled when that UX is built.

**Language (`internal/i18n`):** every bot reply is localized to English or Bahasa Indonesia via `User.Language`, auto-detected from Telegram's own `language_code` on first contact (empty/unrecognized defaults to Indonesian, the primary market), overridable anytime with `/language en` or `/language id`. `category_name` is always extracted in English regardless of reply language, since it feeds the Needs/Wants/Savings bucket-classification keyword map in `internal/service/transaction` — only user-facing text (clarification questions, bot replies, budget warnings) is localized. The dashboard's `/api/v1/reports/budget-progress` also accepts an optional `?lang=` override independent of the user's stored bot-chat preference.

Dashboard endpoints: `/healthz`, `/api/v1/transactions`, `/api/v1/reports/summary`, `/api/v1/reports/top-categories`, `/api/v1/reports/budget-progress`, `/api/v1/export?format=csv|xlsx`.

## TODO (next session)

- Bot/dashboard UX to create and select additional accounts (schema is ready — see Accounts above)
- CSV/Excel export pagination or streaming if transaction history grows past the current 5000-row safety cap
- Dashboard auth (all endpoints currently trust a `user_id` query param — see `TODO(auth)` comments in `internal/api/handlers`)

## Commands

```
make run          # run the server natively
make build        # build a binary to bin/aura-saku
make migrate      # run migrations standalone
make test         # go test ./...
make docker-up    # build + start the full stack
make docker-down  # stop the stack
make docker-logs  # tail backend logs
```
