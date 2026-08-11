# Current Technical Spec: KP Laptop Arbitrage Bot

Updated: 2026-08-11.

This file is the canonical project spec. Historical drafts were removed from Git
because they described obsolete storage, export, and Gemini-pricing designs.

## Goal

Monitor KupujemProdajem laptop listings, enrich hardware specs, compare each
listing against the local KP market, and send Telegram alerts only when the
listing is actionable.

The bot should avoid:

- lost listings after temporary KP/Gemini/Telegram failures,
- diamond alerts that are dominated by better alternatives,
- alerts from shops/resellers,
- market text that presents a global stale median as real value,
- Gemini calls when deterministic parsing is enough.

## Runtime Blocks

```text
main.go
  -> collector: KP search/detail API and shared cooldown
  -> storage: SQLite state, observations, outbox, backups
  -> filters: L1 shop/reseller and L2 junk/defect
  -> specs/hw: deterministic hardware parsing and benchmark lookup
  -> vision: Gemini hardware extraction only
  -> pricing: comparable market, rational ceiling, alternatives
  -> funnel: final L0-L5 decision and alert text
  -> notifier/control: Telegram delivery and control panel
```

`DB_PATH` is the live processing database. `RESEARCH_DB_PATH` is the market and
labeling dataset. They are intentionally separate for now.

## P0 Requirements

Live listings must be durable.

- Store every discovered listing before processing.
- Use process states: `DISCOVERED`, `DETAIL_PENDING`, `ENRICH_PENDING`,
  `EVALUATING`, `ALERT_PENDING`, `DONE`, `DEAD`.
- Keep `attempt_count`, `next_attempt_at`, `lease_until`, and `last_error`.
- Retry temporary errors with backoff and recover unfinished work after restart.

Telegram delivery must be durable.

- Use DB-backed `telegram_outbox`, not a lossy file queue.
- Mark a listing as `ALERTED` only after confirmed Telegram delivery.
- Store attempts, message id, and last error.
- Drain pending outbox rows immediately after startup.

Bad diamonds must be suppressed.

- No diamond for K3/OLS production estimates.
- No diamond with unknown GPU score, unknown condition, or unknown seller type.
- No diamond when a clean private alternative dominates the candidate.
- Step-up alternatives shown in alerts must contain a working link.

Runtime must be controlled.

- Start/Stop buttons must match pause/resume semantics.
- A singleton lock prevents two live bots from running together.
- Bot and research share KP cooldown state after 429/challenge.
- Startup config validation must fail fast with a concrete error.

## P1 Market Evaluation

Alert text and decisions must separate:

- comparable KP median,
- lower quartile,
- rational price ceiling,
- confidence,
- normalized performance index.

The bot must not call a global model "average price for such laptops".

Comparable groups should be built from fresh USED private listings and should
prefer similarity by:

- model/generation,
- CPU,
- GPU Mobile/TGP when known,
- RAM,
- SSD,
- screen class,
- condition,
- defects,
- battery,
- warranty,
- age.

The rational ceiling is derived from stronger alternatives/Pareto front. A
candidate is alert-worthy only if it is below the decision reference with margin
and has no dominating available alternative.

Performance comparison is multidimensional. CPU and GPU PassMark values are not
summed directly; production value/step-up logic uses a normalized index.

OLS/K3 remains an analysis tool only until a real backtest exists with:

- holdout dataset,
- MAPE/MAE by price band,
- calibrated confidence,
- comparison against a simple robust baseline.

## P1 Seller And Ad Labels

The runtime must use manual labels from `research.db.labels`.

- `seller` labels: `SHOP`, `PRIVATE`.
- `ad` labels: `JUNK`, `CLEAN`, `DEFECT`, `PARTS_ONLY`.
- `SHOP` blocks seller alerts immediately.
- `PRIVATE` bypasses behavioral heuristics but does not override current KP
  `Trgovac` / `KP Izlog`.
- `JUNK` blocks an ad immediately.
- `CLEAN` overrides text junk markers but not official `condition=broken`.

Label workflow:

```powershell
go run ./cmd/label -db data/research.db -export-top 50 -out data/labels_review.csv
go run ./cmd/label -db data/research.db -export-random 50 -out data/labels_private.csv
go run ./cmd/label -db data/research.db -import data/labels_review.csv
go run ./cmd/label -db data/research.db -show
```

Seller precision target for alerting is at least 98% private-seller precision.
Unknown sellers should be reviewed separately instead of promoted to diamonds.

## P1 Gemini And Photos

Gemini is not a pricing engine. It can only extract or confirm hardware facts.

Extraction order:

1. deterministic parser and local model/benchmark catalogs,
2. title, description, and KP attributes,
3. all listing photos, preserving order,
4. web/model research only when an exact SKU/MTM/model code is available.

Defaults:

- Flash-Lite for text, vision, and exact-model research.
- More expensive models only by explicit config.
- Daily Gemini call limit, retry on 429/5xx, and circuit breaker.
- Cache merges partial results; later stages must not wipe earlier fields.

Field provenance must remain visible through source labels such as `regex`,
`model-catalog`, `gemini-text`, `gemini-photo-all`, and `gemini-search`.

## P2 Stability

CI must run:

- `go test`,
- race tests,
- `go vet`,
- `govulncheck`,
- migration/schema checks.

SQLite requirements:

- versioned transactional migrations,
- WAL,
- busy timeout,
- foreign keys,
- integrity check,
- daily `VACUUM INTO` backups.

Health output must include:

- last successful search/detail/Gemini/Telegram age,
- queue sizes,
- KP challenge/cooldown state,
- Gemini call and cost stats,
- build version,
- schema version.

Repository hygiene:

- no checked-in binaries,
- no downloaded JS/HTML probes,
- no obsolete prompt drafts,
- README and HANDOFF must describe current behavior.

## Acceptance

On a replay dataset:

- no lost listings after KP/Gemini/Telegram outages and process restart,
- no duplicate Telegram alerts,
- no diamond with a dominating alternative,
- every step-up link is present,
- shop/reseller alerts stay below 2%,
- unknown GPU/condition/seller diamonds are suppressed,
- Gemini cost stays within configured limits.
