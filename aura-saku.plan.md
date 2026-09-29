Act as an expert Full-Stack Developer, System Architect, and Monetization/SaaS Strategist. I want you to help me build a Telegram-based AI Expense Tracker SaaS platform (with a Web Dashboard) from scratch. I have zero background in finance or deep software engineering, so please design a clean, modular, scalable, and well-documented architecture with robust error handling.

### Tech Stack Specifications:
- Backend: Golang (using Fiber or Gin).
- Frontend Web Dashboard: Next.js with TypeScript & Tailwind CSS.
- Database: PostgreSQL (using GORM or sqlx) + Redis (for high-speed rate limiting & caching).
- AI API: OpenAI, Gemini, or Anthropic API (configurable via .env, with robust JSON structured output parser and Audio/Voice transcription support).
- Messaging Integration: 
  - Phase 1 (Core Focus): Telegram Bot (via webhook or long polling).
  - Architecture Preparation: Design the messaging layer abstractly/modularly so it can support WhatsApp Business API later.

### Core Architecture & Anti-Fragile Requirements (Crucial Edge Cases):

1. **Authentication & Multi-Currency User Profile:**
   - Users register/login via Telegram Chat ID.
   - Each user has a default profile currency (e.g., IDR, USD, EUR, SGD) stored in PostgreSQL.
   - Support multi-currency transactions: If a user spends in a foreign currency (detected via chat/voice), store original amount & currency, fetch/apply historical exchange rate at transaction time, and calculate converted default currency for monthly aggregation.

2. **Strict Cost/Security Guardrails (Prevent Boncos/Spam):**
   - **Input Length Check:** Reject messages > 200 characters at Golang level before hitting AI API.
   - **Rate Limiting:** Redis-based rate limiting to block rapid-fire bot requests/DDOS per Chat ID.
   - **Daily Quota Enforcement:** Free Tier (Max 3 successful AI prompts/day), Pro Tier (Unlimited).

3. **Advanced Multi-Modal & Resilient NLP Parser:**
   - **Text & Voice Support:** Handle text & Telegram Voice Notes (VN). Backend downloads voice files, transcribes via AI API, and parses them.
   - **Flexible Transaction Types:** Support `expense`, `income`, `investment`, `transfer`, `reward`.
   - **Ambiguity & Fallback Handling:** If the user's input is unclear or lacks details (e.g., missing amount/context), the AI must prompt a polite clarification question instead of saving invalid data.
   - **Undo Feature (`/undo`):** Implement a command allowing users to instantly delete/revert their last recorded transaction if AI misparsed it.

4. **Performance & Latency Optimization:**
   - Use Golang **Goroutines** to handle database logging and non-blocking tasks asynchronously, ensuring the Telegram bot replies swiftly or triggers typing indicators to mask network round-trips.
   - **Modular AI Client:** Use an interface-based design for the LLM client, allowing me to easily switch between providers (Gemini, OpenAI, Anthropic, or self-hosted models) via `.env`.

5. **Smart Financial Hacks & AI Guidance (Money Management Logic):**
   - **Customizable 50/30/20 Budget Rule:** Users set percentage allocations manually; AI acts as a guardrail against unstable ratios.
   - **Instant Feedback & Insights:** Real-time budget status after every entry.
   - **Anomaly/Impulse Detector & Smart Budget Rollover:** Triggers warnings for luxury/Wants spikes and suggests shifting end-of-month surpluses to savings.

6. **Saldo Reconciliation / Balance Sync Feature:**
   - Users can sync actual bank balances via command (e.g., `/sync 1500000`).
   - The system compares actual bank balance with app's calculated balance and lets the AI gracefully suggest logging an adjustment entry ("Blind spot / unrecorded expense") to keep records accurate.

7. **Comprehensive Reporting & Dashboard (Next.js Frontend + Backend API):**
   - Endpoints and UI views for users to view aggregated totals (Daily, Weekly, Monthly, Yearly).
   - Visual breakdown of top spending categories, budget progress bars, and a **CSV/Excel Export button**.

### What I Need You To Do First:
1. Design the PostgreSQL DB Schema & Redis Key structure (Users with default currency, Subscriptions, Transactions supporting multi-currency exchange rates, Budgets, Balances, Token Logs).
2. Provide the Golang project structure with strict security middleware, voice note async handler, dynamic currency parser prompt, fallback/ambiguity logic, `/undo` command, and reconciliation logic.
3. Provide core backend implementation and REST APIs for Next.js.
4. Provide the Next.js TypeScript basic layout for the user dashboard (including CSV export feature).
5. Provide `.env.example` and Docker Compose setup for VPS deployment.

---

### Status: Backend Scaffold Bootstrapped (2026-09-24)

Confirmed stack decisions (resolving the open "or" choices above):
- **Backend framework: Fiber** (not Gin).
- **DB access layer: GORM** (not sqlx).
- **Default AI provider: Gemini** (native audio/voice support), with OpenAI and Anthropic implemented behind the same `AIClient` interface and selectable via `AI_PROVIDER` in `.env` — provider swap requires no code changes.
- **Telegram integration: Webhook** (not long polling), matching the VPS deployment target.
- **Go module path:** `aura-saku` (bare, no GitHub remote required).

Implemented in this pass: full project structure (`cmd/`, `internal/`, `pkg/`), all 9 GORM models + AutoMigrate, the Redis key scheme (rate limit, daily quota, tier cache, webhook idempotency, pending-clarification state, report cache), the full security middleware chain (input length → rate limit → daily quota, strictly before any AI call), the Telegram webhook (fast-ack + async goroutine dispatch), a working Gemini REST client (structured JSON parsing with fence-stripping, schema validation, and one repair retry) for both text and voice notes, bot command routing (`/start`, `/undo`, `/sync`, free-text/voice), stub-but-functional dashboard REST endpoints, `Dockerfile` + `docker-compose.yml` for local dev, and `.env.example`.

Deliberately left `TODO`-stubbed (fixed method signatures, ready for the next pass): persisting parsed transactions with real currency conversion + category resolution, `/undo` history persistence, 50/30/20 budget evaluation + anomaly detection, balance reconciliation math, OpenAI/Anthropic provider bodies, CSV/Excel export, and a real historical FX rate provider (currently a fixed dummy rate table).

Out of scope for this pass entirely: the Next.js dashboard frontend (item 4 above) — backend was intentionally built first per request.

See `README.md` for setup/run instructions.

---

### Status: Core Loop Hardened, AI Providers Complete (2026-09-29)

Everything the previous status entry left `TODO`-stubbed is now real: transaction persistence with currency conversion + category resolution, `/undo` history persistence, 50/30/20 budget evaluation + anomaly/impulse warnings, balance reconciliation math, full OpenAI and Anthropic provider bodies (including chained tool calling, matching Gemini's), CSV/XLSX export, and a live historical FX provider (`api.frankfurter.dev`, falling back to the static dummy table on failure). The Next.js dashboard (item 4) now exists as a sibling repo, `../aura-saku-web` (see `docker-compose.yml`'s `web` service) — out of scope for *this* repo's plan, tracked separately.

Also shipped since then, none of it in the original spec:
- **Persistent conversation memory** — one continuous `Conversation` per user, every message logged, a rolling AI-generated summary standing in for older history, threaded into every AI parse call so a follow-up like "totalnya 150rb" is understood in context instead of parsed in isolation.
- **Multi-transaction messages** — "bensin 300k, makan siang 300k, makan malam 100k" now records all three instead of silently keeping only the first.
- **Split-message transactions** — the model now checks recent history before treating a vague message as its own isolated case, so a purchase split across several consecutive messages ("beli baju", "sama sepatu", "totalnya 320rb") gets combined into one transaction instead of three failed/ambiguous parses.
- **Correction/deletion now target the RIGHT transaction** — both used to always act on the single most recent transaction regardless of what the message actually described; a `get_recent_transactions` lookup (all three providers) now lets the model match a correction or deletion against the right one by amount/category/description, scoped so one user can never touch another's transaction.
- **Natural-language transaction deletion** ("hapus transaksi makan siang tadi") — new capability, reuses the targeting mechanism above.

Not yet closed from the original spec: item 5's "customizable 50/30/20... users set percentage allocations manually" — the split is still fixed/auto-derived from income, no user-facing way to configure it (tracked in the backlog below).

### Status: Basic scheduler built, first job live (2026-09-29)

Followed through on the investigation below: `internal/scheduler` now exists and is wired into `cmd/server/main.go`. Design matches what was scoped — a `Runner` holding `Job{Name, Interval, Run}`, each on its own `time.Ticker` goroutine, no new dependency, started alongside the Fiber server and stopped via the same signal-driven shutdown context (`cancelScheduler()` before `app.ShutdownWithContext`). A job's error is logged (`AURA-SCHED-001`) and never stops that job's future ticks or the process.

First real job registered: `bot.Service.RunBudgetRolloverNudges`, ticking every 24h. It messages every active user with `budget.Service.SuggestRollover`'s end-of-month surplus suggestion, guarded by a Redis idempotency key scoped to `user + calendar month` (`idempotency.MarkSeen`, now takes an explicit `ttl` instead of a hardcoded 24h so this 32-day dedup window and the webhook-redelivery 24h window can coexist under the same helper) — safe to tick daily without ever double-sending the same month's nudge. No new `Notify`-style wrapper was needed: the job lives on `bot.Service` itself, so it just calls the existing private `reply()`, which already logs into conversation history the same as any replied-to message.

Verified: `go build`/`vet`/`test ./...` clean, `gofmt` clean, including new tests (`scheduler_test.go`: repeated ticking, one job's error never stopping its own or another job's future ticks, two jobs on different intervals running independently; `bot/service_test.go`: a user with a real last-month surplus gets nudged exactly once, a second same-month call is a no-op). Smoke-tested the actual `cmd/server` binary end-to-end: builds, connects to the real dev Postgres/Redis, starts listening, and a `SIGTERM` triggers clean shutdown with no hang — confirmed via process signals, not just reading the code.

Backlog items 1 and 2 below are now "add a job to the existing registry," not "build a scheduler first."

### Investigated: basic cron/job runner (2026-09-29)

Asked to scope what a basic scheduled-job capability would take, since several backlog items below depend on it and none of them are buildable without it first. (Superseded by the Status entry above — kept here for the reasoning that led to the design.)

**Findings:**
- No scheduler existed at the time — the bot was purely reactive to inbound Telegram webhooks (confirmed by grepping the whole codebase for `cron`/`ticker`/`scheduler`: zero hits).
- Deployment is single-replica (`docker-compose.yml`'s `backend` service, no horizontal scaling config) — an in-process scheduler needs no distributed lock or leader election for now. That assumption breaks the day this ever runs as more than one replica; the fix then is a Postgres advisory lock or a Redis `SET NX` claim per job run, not a rewrite.
- Proactively messaging a user needed no new capability — `messaging.Messenger.SendMessage(ctx, chatID, text)` is already provider-agnostic and used internally by `bot.Service.reply`.

**Design (now implemented as described):**
- New `internal/scheduler` package: a `Runner` holding a list of `Job{Name string, Interval time.Duration, Run func(ctx context.Context) error}`, each on its own `time.Ticker` goroutine, errors logged (never crashing the process), started in `cmd/server/main.go` alongside the Fiber server and stopped via the same signal-driven shutdown context that already exists there.
- No new dependency for v1 — plain stdlib `time.Ticker` covers "every N minutes/hours," which is all the backlog below actually needs. Hold off on a real cron-expression library (e.g. `robfig/cron/v3`) until a feature needs true wall-clock scheduling ("daily at 08:00") that a ticker can't express — even that's a ~10-line "sleep until next 08:00, then tick every 24h" helper, not a new dependency, so this may never be needed at all.

### Backlog: features not yet in this project (2026-09-29)

**Needs a new scheduled job** (the scheduler itself now exists — see Status above — so these are just "add a `Job` to the registry in `cmd/server/main.go` plus its own model/logic," not blocked on new infrastructure):
1. **Recurring transactions** — auto-log rent/subscriptions on a schedule instead of requiring a message every time. Needs a new `RecurringTransaction` model (amount/currency/category/interval/next-run-at) and a job that creates a `Transaction` via the existing `transaction.Service.CreateFromParsed` when due.
2. **Bill/payment due-date reminders** — proactive "your Netflix payment is due tomorrow." Either a reminder-only flag on the model above or its own lightweight `Bill` model.
3. ~~Proactive budget-rollover nudges~~ — **done** (see Status above): `bot.Service.RunBudgetRolloverNudges`, registered as the `budget-rollover-nudge` job.

**Standalone** (no ordering implied):
4. **Subscription/Pro-tier payment flow** — `models.Subscription` exists in the schema and migrates cleanly, but nothing anywhere creates or upgrades one; `bot.Service`'s Pro-tier quota bypass exists but is currently unreachable without hand-editing the DB.
5. **WhatsApp channel** — `internal/messaging/whatsapp/client.go` satisfies `Messenger` with `ErrNotImplemented` on every method. The abstraction is proven out via Telegram; WhatsApp itself was never built.
6. **Custom/configurable budgets** — closes the gap noted above: let a user set their own Needs/Wants/Savings percentages (or per-category limits) instead of the fixed 50/30/20 derived from income.
7. **Savings goals** — a target amount + deadline + progress tracking. `BucketSavings` exists today only as a spending classification, not a goal.
8. **Multi-account/wallet UX** — `models.Account` already supports more than one account per user; only a single auto-created "Default" account is ever used. Building this out also unblocks the existing `// TODO: once multi-account support exists, "transfer" should net to zero` in `internal/service/reconciliation/service.go:126`.
9. **Debt/loan tracking** — money owed to or by a specific person, distinct from the generic `transfer` transaction type.
10. **Receipt/photo attachments** — voice input already exists; there's no image capture or receipt OCR.
11. **Custom category management** — categories currently only auto-create freeform from whatever the AI parses (`transaction.resolveCategory`); there's no way to list, rename, merge, or delete a user's own categories.
12. **Shared/family budgets** — everything today is single-user; no shared account or expense-splitting between people.

**Also still open**, flagged in-code rather than here (see each file's own `TODO` comment for context): `internal/messaging/telegram/webhook_handler.go` (bounded worker pool instead of unbounded `go` dispatch per webhook), `internal/api/handlers/auth_handler.go` (cookie `Secure: true` once deployed behind real HTTPS).