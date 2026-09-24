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
cmd/createadmin   bootstrap/update a dashboard admin account (email, name, password, role)
internal/config   env loading + validation
internal/db       Postgres/Redis connections, AutoMigrate registration
internal/models   GORM domain models (users, admins, transactions, budgets, reconciliation, usage logs, ...)
internal/ai       AIClient interface + gemini/openai/anthropic implementations + factory
internal/messaging Messenger interface + telegram/whatsapp implementations
internal/middleware  input length, rate limit, session auth, recover/logger
internal/auth     login-token + JWT session issuance/verification/revocation
internal/quota    daily AI-prompt quota Redis key + increment (checked/enforced in service/bot)
internal/reward   earn-bonus-quota affiliate-link + claim-code flow
internal/service  bot (command routing), transaction, budget, reconciliation, fx, usage
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

Config/DB/Redis wiring, all 10 GORM models (including `Account`) + migrations, the security middleware chain (length check → rate limit, before any AI call; daily quota is enforced downstream in `bot.Service`, see below), the Telegram webhook (fast-ack + async goroutine dispatch), UTC-everywhere timestamps, error-code-tagged logging (`internal/logger`).

**AI (`internal/ai`):** Gemini, OpenAI, and Anthropic all fully implemented against their REST APIs (no SDKs) — structured JSON output with fence-stripping, schema validation, and one repair retry, shared across all three providers (`ai.StripJSONFences`, `ai.ValidateParsedTransaction`). Gemini and OpenAI both support voice-note transcription (Gemini natively, OpenAI via Whisper); Anthropic has no native audio support and returns `apperror.ErrProviderAudioUnsupported`, which the bot surfaces as a clear message rather than a generic failure. The system prompt tells the model today's date and asks it to resolve relative time language ("yesterday", "3 days ago") into a strict ISO date itself, which `internal/service/transaction.resolveOccurredAt` then parses — offloading that reasoning to the model instead of pattern-matching natural language in Go.

**Transactions (`internal/service/transaction`):** persistence with category resolution (seeded with a best-effort Needs/Wants/Savings bucket guess, self-healing categories created before the heuristic existed), historical currency conversion via `internal/service/fx`, and `/undo` (soft-delete + `UndoHistory` snapshot).

**Budget (`internal/service/budget`):** 50/30/20 evaluation against this month's income and spend-by-bucket, over-allocation warnings, a spending-pace anomaly check for Wants, and end-of-month rollover suggestions.

**Reconciliation (`internal/service/reconciliation`):** `/sync` computes the calculated balance and, on a discrepancy, auto-records a blind-spot adjustment transaction (same "commit immediately, `/undo` to revert" pattern used elsewhere).

**FX (`internal/service/fx`):** `LiveService` fetches real historical rates from Frankfurter, falling back to a static rate table on any failure so a provider outage never blocks recording a transaction.

**Quota enforcement (`internal/quota`, `internal/service/usage`):** the free-tier daily AI-prompt cap is now actually enforced. Both the check and the increment live in `bot.Service` (moved out of HTTP middleware specifically so a blocked user gets an actual localized reply instead of a silent webhook-level 429 nobody sees) — a successful AI call increments the Redis counter (`quota.Increment`) and persists an `AIUsageLog` row (token/latency detail) plus a `QuotaUsage` daily mirror via `internal/service/usage`, fire-and-forget so neither write delays the reply. `FREE_TIER_DAILY_QUOTA`/`PRO_TIER_DAILY_QUOTA` in `.env` control the limits.

**Earn bonus quota (`internal/reward`):** when a free user hits their daily limit, if `REWARD_AFFILIATE_URL` is configured, the bot offers that link plus a one-time code; `/claim <code>` grants `REWARD_BONUS_AMOUNT` extra prompts for the day, up to `REWARD_MAX_CLAIMS_PER_DAY` times. This is a deliberately scoped-down v1: it's an honor-system flow (no landing page or ad-network callback verifying the user actually visited the link) since that would require web infrastructure that doesn't exist yet. Leave `REWARD_AFFILIATE_URL` blank to disable the feature entirely.

**Accounts (`internal/models.Account`, `internal/service/account`):** schema groundwork for multi-account support — every user gets one auto-created "Default" account, and every transaction/reconciliation is attributed to it. No bot/dashboard UX to create or select additional accounts yet (intentionally deferred); see the code comments on `Transaction.AccountID` and `reconciliation.balanceSign` for how a second account and cross-account transfers are expected to be modeled when that UX is built.

**Language (`internal/i18n`):** every bot reply is localized to English or Bahasa Indonesia via `User.Language`, auto-detected from Telegram's own `language_code` on first contact (empty/unrecognized defaults to Indonesian, the primary market), overridable anytime with `/language en` or `/language id`. `category_name` is always extracted in English regardless of reply language, since it feeds the Needs/Wants/Savings bucket-classification keyword map in `internal/service/transaction` — only user-facing text (clarification questions, bot replies, budget warnings) is localized. The dashboard's `/api/v1/reports/budget-progress` also accepts an optional `?lang=` override independent of the user's stored bot-chat preference.

**Dashboard authentication (`internal/auth`, `internal/middleware/auth.go`):** every `/api/v1/*` endpoint (except `/auth/*` itself) now requires a verified session — the `user_id` query param it used to trust directly is gone as an attack surface. Two ways to get a session:

- **Bot users:** send `/dashboard` to the bot; it replies with a one-time login link (`POST /api/v1/auth/verify` with the token), valid for 10 minutes, single-use. The resulting session is locked to that user's own `user_id` — passing a different `?user_id=` is silently ignored.
- **Admins/superadmins:** `POST /api/v1/auth/admin-login` with `email`/`password` (accounts live in the `admins` table, created via `cmd/createadmin`, not `.env` — see below). An admin/superadmin session *is* allowed to pass `?user_id=` to view any user's data, since that's the point of the role.

Sessions are JWTs (role + identity claims, so a future frontend can read them client-side to decide what menu to render) referenced by an httpOnly `aura_session` cookie, but each token also carries a random session ID that must still exist in Redis — `POST /api/v1/auth/logout`, or deactivating an admin (`Admin.IsActive = false`), deletes that Redis entry and the token stops working immediately, even though its signature would otherwise still verify until natural expiry (`SESSION_TTL_HOURS`, default 7 days). Admin sessions are additionally re-checked against `Admin.IsActive` in Postgres on every request for exactly this reason. `POST /api/v1/auth/admin-login` has its own IP-keyed rate limit (`ADMIN_LOGIN_RATE_LIMIT_*`) since it's a real brute-forceable credential check.

Create the first admin:
```
go run ./cmd/createadmin -email=you@example.com -name="Your Name" -password=<password> -role=superadmin
```
Running it again for the same email updates their name/password/role — it doubles as a password-reset tool.

Dashboard endpoints (all session-gated): `/api/v1/transactions`, `/api/v1/reports/summary`, `/api/v1/reports/top-categories`, `/api/v1/reports/budget-progress`, `/api/v1/export?format=csv|xlsx`. `/healthz` remains open (no session needed).

**Webhook idempotency (`internal/idempotency`):** Telegram redelivers a webhook update if it doesn't get a timely 200 OK, and nothing previously deduplicated by `update_id` — a redelivered message got parsed and saved as a second, duplicate transaction. `NewWebhookHandler` now calls `idempotency.MarkSeen` (Redis `SET NX`, 24h TTL) before dispatching; a redelivery is ack'd 200 without reprocessing. Deliberately generic (namespace + id, not Telegram-specific) so a future WhatsApp webhook can reuse it under its own namespace.

## TODO (next session)

- Bot/dashboard UX to create and select additional accounts (schema is ready — see Accounts above)
- CSV/Excel export pagination or streaming if transaction history grows past the current 5000-row safety cap
- Route-level authorization distinguishing `admin` vs `superadmin` (both currently pass the same `RequireSession` check — no admin-only-vs-superadmin-only endpoints exist yet to actually differentiate)
- Set the session cookie's `Secure` flag once this is deployed behind real HTTPS (currently `false` so local HTTP dev works)

## Known backend hardening gaps (not yet addressed)

Surveyed but deliberately deferred — worth revisiting before real production traffic:

- **No rate limiting on the dashboard API itself.** `/webhook/telegram` and `/auth/admin-login` are rate-limited; `/api/v1/transactions`, `/reports/*`, `/export` are not. A leaked/stolen session token could hammer these with no cap (cost/DB-load risk, not a data-exposure one, since auth is still required).
- **Health check has no explicit timeout.** `sqlDB.Ping()`/`redis.Ping()` in `internal/api/handlers/health_handler.go` rely on driver defaults rather than a short deadline — a hung (not cleanly erroring) Postgres connection could make `/healthz` hang instead of failing fast, which matters for container orchestration liveness checks.
- **No automated tests for `bot.Service` itself.** Every lower-level service (`transaction`, `budget`, `reconciliation`, `quota`, `reward`, `auth`, `idempotency`) has real test coverage; the command-routing layer tying them together (including the quota/reward gating logic in `enforceQuota`) is only verified by manual `curl` sessions, not regression tests.
- **No CORS configuration yet.** Not exploitable today (no browser dashboard exists to call the API cross-origin), but needs deliberate setup (not a wildcard `*`, since sessions are cookie-based) once the Next.js frontend exists.
- **No audit trail for admin `?user_id=` access.** Given this is financial data and multiple admins are planned, logging "admin X viewed user Y's data at time Z" is worth having before that becomes a real multi-admin system.

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
