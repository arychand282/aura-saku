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

### Status: Recurring transactions shipped (2026-09-30)

Backlog item 1 below is done. New `RecurringTransaction` model + `internal/service/recurring.Service` (Create/ListActive/Cancel/Due/Advance — schedule math only, no messaging or category/FX logic of its own). Creation is natural language, matching how every other transaction intent already works: a new `create_recurring_transaction` tool (`ai.IntentRecurringCreate`) alongside record/correct/delete/query/balance — "netflix 150rb setiap tanggal 5" schedules a monthly recurrence instead of recording anything right away. `day_of_month` accepts 1-31 (31 means "end of every month," clamped to each month's own actual last day at schedule-computation time — no special February handling needed anywhere, `time.Date`'s own month-overflow normalization handles the December→January boundary too). Listing and canceling are plain commands (`/recurring`, `/recurring cancel <id>`) rather than AI tools — an id is exact, so there's no ambiguity for a lookup tool to resolve, unlike a correction/deletion against dozens of past transactions.

New job `RunRecurringTransactions` (registered as `recurring-transactions`, hourly — finer-grained than the rollover nudge's daily tick, so a due bill posts within the hour instead of up to a day late) reconstructs an `ai.ParsedTransaction` from a due `RecurringTransaction` row and posts it via the existing `transaction.Service.CreateFromParsed` unchanged, so category resolution and FX conversion behave identically to a message-triggered record. `recurring.Service.Advance` moves the schedule forward by exactly one month from its OWN stored value (never recomputed from "now"), so a fixed day-of-month never drifts even if the scheduler was down for a while.

Mid-build design question, resolved before finishing: should editing/canceling a recurring transaction also work via natural language, mirroring the `get_recent_transactions`/`target_transaction_id` pattern already built for corrections and deletions? Decided **not** to build 3 new tools for this — only 2 would ever be needed (a `get_recurring_transactions` lookup + a `delete_recurring_transaction` tool), since an "update" is better modeled as cancel-and-recreate (both already exist) than a third tool with its own partial-update semantics. Deferred to a follow-up rather than folded into this pass, so the MVP (creation + command-based list/cancel) could ship and be verified end to end first — tracked as a new backlog item below.

Verified: `go build`/`vet`/`test ./...` clean, `gofmt` clean, including new tests (`recurring/schedule_test.go`: same-day creation never fires immediately, December→January rollover, day-31 clamping including leap-year February, all pure unit tests with no DB; `recurring/service_test.go`: DB-backed Create/ListActive/Cancel/Due/Advance against real Postgres, including cross-user ownership enforcement; `bot/service_test.go`: natural-language creation end to end, `/recurring` list+cancel with a malformed/nonexistent/real id, and the scheduler job posting a due transaction + notifying the user + advancing the schedule + never double-posting on a second run). Live-verified against the real Gemini API: Indonesian ("setiap tanggal 5"), English ("every month on the 10th"), and "end of month" phrasing all correctly resolve to `create_recurring_transaction` with the right `day_of_month`, while a one-off mention with no stated cadence still resolves to a normal `record_transaction`. Smoke-tested the actual `cmd/server` binary: the new `recurring_transactions` table migrates cleanly with its indexes/constraints, and the new job starts and stops cleanly alongside the existing one.

### Status: Bill reminders shipped (2026-09-30)

Backlog item 2 below is done, exactly the way it was scoped above: no new model. `models.RecurringTransaction` gained one field, `ReminderOnly bool` (default false, so every row created before this field existed keeps auto-recording unchanged) — the exact same schedule (day-of-month, month-rollover/clamping, `NextRunAt` advancement) now serves both an auto-posting recurring transaction AND a notify-only bill reminder, distinguished only by that flag.

Creation is a second AI tool, `create_bill_reminder` (same fields/schema as `create_recurring_transaction`, reusing `recurringTransactionProperties()` as-is), rather than a boolean parameter on one tool — a distinct tool name gives the model a clearer signal than a flag buried in shared fields would, matching how record/correct/delete are already separate tools despite sharing most fields. `ParsedTransactionFromToolCall` sets `ReminderOnly: true` only for that tool's calls, so the two tools funnel into the same `IntentRecurringCreate` handling everywhere downstream (`recurring.Service.Create`, `handleRecurringCreate`, `RunRecurringTransactions`) with only a couple of small branches for the different confirmation/notification text and the auto-post-or-not decision.

`RunRecurringTransactions` (unchanged job registration, still hourly) now branches on `rt.ReminderOnly`: a reminder advances its schedule and messages the user ("🔔 Reminder: ... is due today") without ever touching `transaction.Service.CreateFromParsed` — nothing is recorded, since the user still wants to log the actual payment themselves. `/recurring`'s list output tags each reminder-only row with a translated suffix so a mixed list of auto-record and reminder-only rows stays unambiguous.

Verified: `go build`/`vet`/`test ./...` clean, `gofmt` clean, including new tests (`bot/service_test.go`: natural-language bill-reminder creation confirms the reminder-specific reply text and `ReminderOnly=true` on the stored row, `/recurring` list tags it correctly, and the scheduler job notifies + advances the schedule while creating zero `Transaction` rows on a due reminder). Live-verified against the real Gemini API: Indonesian ("ingetin"), English ("remind me... don't record it for me"), and "kasih tau" phrasing all correctly resolve to `create_bill_reminder` with `ReminderOnly:true`, while the existing `create_recurring_transaction`/`record_transaction` cases are unaffected. Smoke-tested the actual `cmd/server` binary: the new `reminder_only` column migrates cleanly onto the existing table.

### Status: End-of-month category spending insights shipped (2026-09-30)

Backlog item 4 below is done. Planned via plan mode first (the user asked to understand the feature before building it) — see the discussion for the two real design decisions made along the way: (1) flag whichever category grew the MOST since the month before, not just list the biggest categories, since that's what's actually actionable ("what changed"), and (2) show the flagged category's real totals from BOTH months side by side ("Rp600.000 last month vs Rp200.000 the month before"), not just the delta/percent alone, for direct comparability.

New `report.Service.MonthlyCategoryInsight(ctx, userID)` (in `report.go`, alongside the existing `TopCategories`/`Summary`/`CategoryTotal`): sums expenses by category for last calendar month and the month before via a new `CategoryBreakdown(ctx, userID, start, end)` (same GORM-builder shape as `TopCategories`, but an explicit half-open `[start, end)` range instead of `PeriodRange`'s trailing-window keywords, so two adjacent calendar months can be diffed without double-counting a transaction landing exactly on the boundary). Returns the top 3 categories plus whichever one (if any) grew at least 15% since the month before — a plain constant, not configurable yet — with both months' real totals kept as separate fields, not just the delta. Returns `(nil, nil)` when there were no expenses at all last month, mirroring `budget.Service.SuggestRollover`'s "empty means nothing to report" convention.

New required `ai.AIClient` method `SuggestSavings(ctx, spendingSummary, preferredLanguage) (string, *UsageMetadata, error)` — a plain text completion, no tools, implemented identically across Gemini/OpenAI/Anthropic by copying `Summarize`'s exact request-building shape in each provider file (new `internal/ai/prompt/savings.go` mirrors `summary.go`). Given a plain-English description of the month's numbers, it writes one short (2-3 sentence), specific, actionable suggestion in the user's language — this is the part the user specifically asked to add mid-build: "ask AI to give suggestion what best to do... so user know what best to keep saving more."

New job `RunCategoryInsights` (registered as `category-insights`, daily, same per-user-per-calendar-month idempotency pattern as the other two jobs) composes the final message: deterministic header/list/warning lines via i18n (no AI involved, always works), then calls `SuggestSavings` and appends a "💡 " suggestion line — best-effort: if that call fails, the factual recap still sends without the tip rather than being dropped entirely. The AI call doesn't touch the interactive daily quota (system-triggered, not user-initiated) but is still logged via `usage.Service` for cost visibility.

Verified: `go build`/`vet`/`test ./...` clean, `gofmt` clean, including new tests (`report/report_test.go`: top-3 ranking, the 15%-threshold cutoff both sides, no-prior-month-data omits the flag, no-expenses-at-all returns nil; `bot/service_test.go`: full recap end to end with both months' real amounts and the AI suggestion present, a second same-month call is a no-op, and a simulated `SuggestSavings` failure still sends the deterministic part with no suggestion line). Live-verified `SuggestSavings` against the real Gemini API in Indonesian and English, with and without a flagged category — every response was short, specific, and referenced the actual numbers, not generic advice. Smoke-tested the actual `cmd/server` binary with all three jobs registered.

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
1. ~~Recurring transactions~~ — **done** (see Status above): `RecurringTransaction` model, `internal/service/recurring.Service`, `create_recurring_transaction` tool, and the `recurring-transactions` job.
2. ~~Bill/payment due-date reminders~~ — **done** (see Status above): `RecurringTransaction.ReminderOnly`, `create_bill_reminder` tool, reusing the same `recurring-transactions` job.
3. ~~Proactive budget-rollover nudges~~ — **done** (see Status above): `bot.Service.RunBudgetRolloverNudges`, registered as the `budget-rollover-nudge` job.
4. ~~End-of-month category spending insights~~ — **done** (see Status above): `report.Service.MonthlyCategoryInsight`, `ai.AIClient.SuggestSavings`, and the `category-insights` job.

**Standalone** (no ordering implied):
5. **Natural-language edit/cancel for recurring transactions and bill reminders** — deferred out of the recurring-transactions build above. `/recurring cancel <id>` (command-based) covers cancellation today; saying "batalin netflix" or "netflix now 200rb" instead would need a `get_recurring_transactions` lookup tool + a `delete_recurring_transaction` tool (an "update" is better modeled as cancel-and-recreate with the two tools that already exist than a third tool with its own partial-update semantics — see that Status entry for the reasoning).
6. **Subscription/Pro-tier payment flow** — `models.Subscription` exists in the schema and migrates cleanly, but nothing anywhere creates or upgrades one; `bot.Service`'s Pro-tier quota bypass exists but is currently unreachable without hand-editing the DB.
7. **WhatsApp channel** — `internal/messaging/whatsapp/client.go` satisfies `Messenger` with `ErrNotImplemented` on every method. The abstraction is proven out via Telegram; WhatsApp itself was never built.
8. **Custom/configurable budgets** — closes the gap noted above: let a user set their own Needs/Wants/Savings percentages (or per-category limits) instead of the fixed 50/30/20 derived from income.
9. **Savings goals** — a target amount + deadline + progress tracking. `BucketSavings` exists today only as a spending classification, not a goal.
10. **Multi-account/wallet UX** — `models.Account` already supports more than one account per user; only a single auto-created "Default" account is ever used. Building this out also unblocks the existing `// TODO: once multi-account support exists, "transfer" should net to zero` in `internal/service/reconciliation/service.go:126`.
11. **Debt/loan tracking** — money owed to or by a specific person, distinct from the generic `transfer` transaction type.
12. **Receipt/photo attachments** — voice input already exists; there's no image capture or receipt OCR.
13. **Custom category management** — categories currently only auto-create freeform from whatever the AI parses (`transaction.resolveCategory`); there's no way to list, rename, merge, or delete a user's own categories.
14. **Shared/family budgets** — everything today is single-user; no shared account or expense-splitting between people.

**Also still open**, flagged in-code rather than here (see each file's own `TODO` comment for context): `internal/messaging/telegram/webhook_handler.go` (bounded worker pool instead of unbounded `go` dispatch per webhook), `internal/api/handlers/auth_handler.go` (cookie `Secure: true` once deployed behind real HTTPS).