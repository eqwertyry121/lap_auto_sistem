# KP Laptop Arbitrage Bot

Go bot for monitoring laptop listings on KupujemProdajem, enriching hardware specs,
comparing each listing against the local KP market dataset, and sending only
actionable Telegram alerts.

The project is intentionally local-first:

- SQLite is the runtime storage.
- `data/research.db` is the market dataset used for comparable prices.
- Gemini is used only to extract/confirm hardware specs, not to decide whether a
  listing is profitable.
- Telegram delivery is handled through a DB outbox so alerts survive restarts.

Canonical implementation plan: [PLAN.md](PLAN.md).

## Architecture

```text
KP Search API
  -> storage durable queue
  -> KP detail fetch
  -> L0/L1/L2 deterministic filters
  -> specs extraction: regex -> exact model catalog -> text -> all photos -> exact model research
  -> market evaluation
  -> Telegram outbox
```

Main blocks:

- `main.go` - live process, workers, startup checks, singleton lock, graceful shutdown.
- `collector/` - KP API client, search/detail requests, shared KP cooldown.
- `storage/` - SQLite schema, migrations, durable process states, listing observations,
  Telegram outbox, backups.
- `filters/` - deterministic L1 shop/reseller filter and L2 junk/defect filter.
- `specs/` and `hw/` - deterministic parsing, exact model catalog, and hardware benchmark lookup.
- `vision/` - Gemini client with Flash-Lite defaults, retry/cooldown, token/cost stats.
- `pricing/` - market loading, comparable groups, Pareto/step-up suppression, value logic.
- `funnel/` - full L0-L5 decision pipeline and Telegram alert text.
- `control/` - Telegram control panel.
- `cmd/research` - market dataset collector.
- `cmd/label` - manual seller/ad labeling workflow.
- `tools/` - diagnostics and one-off maintenance utilities.

## Decision Rules

The bot no longer treats a global median as "average price for this laptop".
Alerts separate:

- comparable KP median,
- lower quartile,
- rational price ceiling from stronger alternatives,
- confidence level,
- normalized performance index.

Diamond alerts are suppressed when a clean private alternative dominates the
candidate or a much better step-up exists for nearly the same money. Step-up
alerts must include the alternative link.

Production decisions do not use OLS/K3 estimates. Those remain available only
for analysis until a real backtest exists.

## Manual Labels

Use manual labels to tighten the seller/shop filter without changing code:

```powershell
go run ./cmd/label -db data/research.db -export-top 50 -out data/labels_review.csv
go run ./cmd/label -db data/research.db -export-random 50 -out data/labels_private.csv
go run ./cmd/label -db data/research.db -import data/labels_review.csv
go run ./cmd/label -db data/research.db -show
```

Supported labels:

- `seller` target: `SHOP` or `PRIVATE`.
- `ad` target: `JUNK`, `CLEAN`, `DEFECT`, `PARTS_ONLY`.

Runtime uses these labels in the funnel. `SHOP` cuts a seller immediately.
`PRIVATE` bypasses behavioral heuristics, but does not override current KP
`Trgovac` / `KP Izlog` evidence. `JUNK` cuts an ad immediately. `CLEAN`
overrides text junk markers, but not official `condition=broken`.

## Quick Start

```powershell
copy .env.example .env
go run .
```

Minimum production configuration needs:

- `GEMINI_API_KEY`
- `TELEGRAM_BOT_TOKEN`
- `TELEGRAM_CHAT_ID`

If Telegram variables are absent, alerts are printed to the console.

## Important Configuration

| Variable | Default | Purpose |
|---|---:|---|
| `POLL_INTERVAL_SEC` | `45` | KP search polling interval |
| `DB_PATH` | `data/kp_bot.db` | live bot SQLite DB |
| `RESEARCH_DB_PATH` | `data/research.db` | market dataset SQLite DB |
| `DB_BACKUP_DIR` | `data/backups` | daily SQLite backups |
| `FETCH_DELAY_MS` | `700` | delay between KP detail requests |
| `GEMINI_MODEL` | `gemini-2.5-flash-lite` | base Gemini model |
| `GEMINI_TEXT_MODEL` | `gemini-2.5-flash-lite` | text extraction model |
| `GEMINI_VISION_MODEL` | `gemini-2.5-flash-lite` | photo extraction model |
| `GEMINI_SEARCH_MODEL` | `gemini-2.5-flash-lite` | exact model research model |
| `GEMINI_CONCURRENCY` | `5` | Gemini parallel request limit |
| `GEMINI_DAILY_LIMIT` | `80` | daily Gemini call limit, `0` disables it |
| `GEMINI_DAILY_BUDGET_USD` | `0` | estimated daily Gemini spend cap, `0` disables it |
| `MARKET_REFRESH_MIN` | `360` | market model refresh period |
| `DIAMOND_DEV_PCT` | `-15` | diamond threshold vs decision reference |
| `SUSPECT_DEV_PCT` | `-40` | bait-risk threshold |
| `DIAMOND_MIN_N` | `5` | minimum comparable group size |
| `REQUIRE_DGPU` | `1` | ignore laptops without discrete GPU |
| `WEB_RESEARCH` | `1` | exact model/SKU hardware lookup |
| `MANUAL_MIN_EUR` | `400` | manual-review alert minimum |
| `MOOSE_MIN_EUR` | `400` | rare-hardware summary minimum |
| `LOCK_PATH` | `data/kpbot.lock` | singleton process lock |
| `KP_COOLDOWN_PATH` | `data/kp_cooldown` | shared KP cooldown file |
| `KP_RATE_COOLDOWN_SEC` | `90` | shared cooldown after KP 429 |
| `KP_CHALLENGE_COOLDOWN_MIN` | `30` | shared cooldown after KP anti-bot challenge |
| `KP_WATCHDOG_RESEARCH` | `0` | watchdog does not start research by default |

Config is validated on startup. Invalid intervals, invalid booleans, empty model
names, bad thresholds, and half-configured Telegram credentials fail fast.

## Market Dataset

Refresh the research dataset separately from the live bot:

```powershell
go run ./cmd/research -db data/research.db
```

The live process uses `data/research.db` as read-mostly market context and
records lightweight seller/ad observations through the normal runtime path.
The current design still has two SQLite databases; `DB_PATH` is the source of
truth for live processing, while `RESEARCH_DB_PATH` is the market and labeling
dataset.

## Tests

```powershell
go test -count=1 ./...
go vet ./...
go mod verify
```

CI also runs race tests and `govulncheck` on GitHub.

Live KP checks are opt-in:

```powershell
go test -tags live ./collector -run TestLive -v
```

## Operational Notes

- Alerts are sent through `telegram_outbox`; a listing becomes `ALERTED` only
  after successful Telegram delivery.
- Incomplete work is stored as durable process states and retried with backoff.
- SQLite is opened with WAL, busy timeout, foreign keys, integrity checks, and
  daily `VACUUM INTO` backups.
- Gemini cost/call stats are visible in digest/control health output.
- Funnel trace can be enabled with `FUNNEL_TRACE=1` for listing-level debugging.
