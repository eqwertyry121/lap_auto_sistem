# HANDOFF

Updated: 2026-08-11.

This repo is the KP laptop arbitrage bot. The current branch used for the audit
implementation is `codex/execute-pricing-tz`.

## Current Shape

- Runtime DB: `data/kp_bot.db`.
- Market and labeling DB: `data/research.db`.
- Live processing uses durable states in `market_listings`.
- Telegram delivery uses `telegram_outbox`; `ALERTED` is set only after delivery.
- Market alerts use comparable median, lower quartile, rational ceiling, and a
  normalized performance index. OLS/K3 is not used for production decisions.
- Gemini defaults to Flash-Lite and is used only for hardware extraction.
- All listing photos are sent to the Gemini photo stage when that stage is needed.
- Manual labels from `labels` are used by the runtime L1/L2 filters.

## Useful Commands

```powershell
go test -count=1 ./...
go vet ./...
go mod verify
go run .
go run ./cmd/research -db data/research.db
go run ./cmd/label -db data/research.db -show
```

## High-Risk Areas

- Pricing/grouping still depends on how complete `research.db` is.
- Seller precision depends on keeping manual labels fresh.
- The project still has two SQLite databases; `DB_PATH` is live state,
  `RESEARCH_DB_PATH` is market context.
- Local race tests need CGO/toolchain support; CI is the expected place for them.

## Canonical Docs

- [README.md](README.md) - current operational documentation.
- [PLAN.md](PLAN.md) - long-form project plan/spec.

Old prompt files and historical `PLAN_v*.md` drafts were removed from Git to
avoid agents following obsolete storage, export, and Gemini-pricing designs.
