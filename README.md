# Aura-Saku Backend

Telegram-based AI expense tracker SaaS — Go backend (Fiber + GORM + PostgreSQL + Redis), with a pluggable AI client (Gemini by default) and a messaging layer abstracted for future WhatsApp support.

This scaffold is a **runnable skeleton**: config, database, Redis, migrations, security middleware, the Telegram webhook, and the dashboard REST API are fully wired. The core NLP parsing (Gemini) is implemented; persistence of parsed transactions, budget math, and balance reconciliation are intentionally `TODO`-stubbed behind fixed interfaces for the next development pass — see `// TODO` comments in `internal/service/*`.

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

## What's implemented vs. TODO

**Implemented:** config/DB/Redis wiring, all 9 GORM models + migrations, the full security middleware chain (length check → rate limit → daily quota, in that order, before any AI call), the Telegram webhook (fast-ack + async goroutine dispatch), Gemini text & voice-note parsing (structured JSON output with fence-stripping, validation, and one repair retry), bot command routing (`/start`, `/undo`, `/sync`, free-text/voice), and stub-but-functional dashboard endpoints (`/healthz`, `/api/v1/transactions`, `/api/v1/reports/summary`, `/api/v1/reports/top-categories`).

**TODO (next session):** persisting parsed transactions with real currency conversion + category resolution (`internal/service/transaction`), `/undo` history (soft-delete + snapshot), 50/30/20 budget evaluation and anomaly detection (`internal/service/budget`), balance reconciliation math (`internal/service/reconciliation`), OpenAI/Anthropic provider implementations, CSV/Excel export, and a real historical FX rate provider (currently a fixed dummy rate table in `internal/service/fx`).

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
