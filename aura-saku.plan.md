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