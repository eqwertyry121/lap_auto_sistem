# KP Laptop Arbitrage Bot

Бот на **Go** мониторит категорию «Ноутбуки» на **KupujemProdajem** (KP), находит
недооценённые лоты с помощью **Gemini (Text + Vision)** и мгновенно шлёт алерты в
**Telegram**. Рыночная база копится в **SQLite** и батчами выгружается в
**CSV-файл** (`data/market_history.csv`) — открывается в Excel/Google Sheets.

Полное ТЗ и архитектура — в [`PLAN.md`](PLAN.md).

> ⚠️ Авто-сообщения продавцам (Reply Worker) сознательно **вне MVP** — вся переписка
> ведётся вручную по ссылке из Telegram.

---

## Возможности

- **Сканер рынка** (`cmd/market-scan`) — обходит всю выдачу категории и собирает
  рыночную базу (заголовок, цена, валюта, ссылка) в SQLite.
- **Живой мониторинг** — каждые `POLL_INTERVAL_SEC` секунд забирает свежие объявления,
  дедуплицирует по `ad_id`, отсеивает магазины/партии, запрашивает детали (`/eds/{id}`).
- **Двухфазная оценка Gemini**:
  - Фаза 1 — текст (заголовок + описание + характеристики + рыночная сводка из кэша цен).
  - Фаза 2 — Vision (все фото объявления), если модель не определена по тексту.
  - Строгие правила квалификации CPU (без точной модели/поколения `is_deal` не ставится).
- **Кэш рыночных цен** в памяти (обновляется каждые 30 мин из SQLite) — даёт Gemini
  «медиана/диапазон похожих лотов».
- **Telegram**: `🚀 НАЙДЕН ПРОФИТ` и `⚠️ ТРЕБУЕТСЯ ПРОВЕРКА` с inline-кнопкой на объявление.
- **CSV-экспорт**: фоновый батч-экспортёр раз в `EXPORT_INTERVAL_MIN` дописывает
  до 500 новых строк в `data/market_history.csv` (Дата, Заголовок, Цена, Валюта,
  Ссылка). Без внешних API и ключей, файл открывается в Excel/Google Sheets.

---

## Быстрый старт

### 1. Зависимости

- Go ≥ 1.25
- (опционально) Docker — если позже захотите PostgreSQL вместо SQLite

### 2. Установка

```bash
cd the_bot_god_of_laptop
cp .env.example .env        # Windows: copy .env.example .env
```

Заполните `.env` (см. ниже). Минимально для запуска нужны `GEMINI_API_KEY` и
`TELEGRAM_BOT_TOKEN` + `TELEGRAM_CHAT_ID`.

### 3. Собрать рыночную базу (один раз)

```bash
go run ./cmd/market-scan                 # до конца выдачи (~800 страниц)
# или пробный прогон:
go run ./cmd/market-scan -max-pages 10
```

Сканер устойчив к 429 (бэкофф), идемпотентен (дедуп по `ad_id`) и пишет в `data/kp_bot.db`.

### 4. Запустить бота

```bash
go run .
```

Бот стартует, начнёт опрос KP и будет слать алерты в Telegram.
`Ctrl+C` — корректная остановка (graceful shutdown).

---

## Переменные окружения (.env)

| Переменная | По умолчанию | Описание |
|---|---|---|
| `POLL_INTERVAL_SEC` | `45` | период опроса Search API |
| `DB_PATH` | `data/kp_bot.db` | файл SQLite (SSOT) |
| `DB_BACKUP_DIR` | `data/backups` | daily SQLite backups via `VACUUM INTO` |
| `FETCH_DELAY_MS` | `700` | пауза между запросами `/eds/{id}` |
| `GEMINI_API_KEY` | — | **обязателен** для оценки |
| `GEMINI_MODEL` | `gemini-2.5-flash-lite` | базовая модель Gemini |
| `GEMINI_LITE_MODEL` | `gemini-2.5-flash-lite` | лёгкая модель для text/vision/search-ступеней |
| `GEMINI_CONCURRENCY` | `5` | лимит параллельных вызовов Gemini |
| `GEMINI_DAILY_LIMIT` | `80` | дневной лимит вызовов Gemini; `0` явно снимает лимит |
| `TELEGRAM_BOT_TOKEN` | — | токен бота (`@BotFather`) |
| `TELEGRAM_CHAT_ID` | — | id чата/канала для алертов |
| `CSV_PATH` | `data/market_history.csv` | файл CSV-экспорта рыночной базы |
| `EXPORT_INTERVAL_MIN` | `30` | период батч-экспорта |
| `LOCK_PATH` | `data/kpbot.lock` | singleton-lock, чтобы не запустить два экземпляра бота |
| `KP_COOLDOWN_PATH` | `data/kp_cooldown` | общий cooldown bot/research после KP 429/challenge |
| `KP_RATE_COOLDOWN_SEC` | `90` | длительность общего cooldown после KP 429 |
| `KP_CHALLENGE_COOLDOWN_MIN` | `30` | длительность общего cooldown после KP anti-bot challenge |
| `KP_WATCHDOG_RESEARCH` | `0` | `1` включает watchdog для `research.exe`; по умолчанию watchdog следит только за ботом |

Если `TELEGRAM_*` не заданы — алерты печатаются в консоль (dry-run).
CSV-экспорт работает всегда и не требует настройки (путь — `CSV_PATH`).

---

## Структура проекта

```
├── main.go                  # связка: воркеры, циклы, graceful shutdown
├── cmd/market-scan/         # разовый/периодический сбор всей рыночной базы
├── config/config.go         # конфигурация из .env
├── models/models.go         # SearchAd, SearchResults, AdDetail, Listing, Verdict
├── collector/
│   ├── client.go            # HTTP-клиент KP + подпись x-kp-signature (SHA1)
│   ├── poller.go            # Search API (по страницам)
│   ├── fetcher.go           # /eds/{ad_id}
│   ├── spam.go              # фильтр магазинов/партий
│   ├── signature_test.go    # регрессия подписи (эталоны из живого API)
│   └── live_test.go         # live-тест реального KP API (go test -tags live)
├── storage/
│   ├── db.go                # SQLite: market_listings, дедуп, статусы
│   └── pricecache.go        # кэш рыночных цен + сводка для промпта
├── vision/
│   ├── gemini.go            # REST-клиент Gemini (generateContent, inline images)
│   └── evaluator.go         # двухфазная оценка, промпт с правилами CPU
├── notifier/telegram.go     # ALERT / NEED CHECK + inline-кнопка
└── exporter/csv.go          # батчная дозапись рыночной истории в CSV
```

---

## Важные технические детали

### Авторизация KP API
Эндпоинты `api/web/v1/*` требуют заголовок **`x-kp-signature`** — SHA1 от
`полный путь + query + тело + соль` (соль восстановлена из JS-бандла KP). Куки
**не нужны**. Реализация — в `collector/client.go`, регрессионные тесты — в
`collector/signature_test.go`. Без подписи API отвечает `401 not_authorized`.

### Рейт-лимиты KP
- Сканер и поллер делают паузы между запросами и откатываются при `429`
  (бэкофф 30/60/90 c, до 3 повторов подряд).
- Тяжёлый `/eds/{id}` вызывается **только для новых** `ad_id`.

### Хранение
SQLite выбран для MVP, чтобы бот работал «из коробки». Запросы написаны так, чтобы
переезд на PostgreSQL свёлся к смене драйвера и плейсхолдеров в `storage/db.go`.
Живые объявления обрабатываются через durable state machine в `market_listings`
(`DETAIL_PENDING` → `EVALUATING` → `ALERT_PENDING` → `DONE/DEAD`), а Telegram
алерты доставляются через транзакционный `telegram_outbox`. Статус `ALERTED`
ставится только после успешной отправки.

---

## Тесты

```bash
go test ./...                       # unit-тесты (подпись и пр.)
go test -tags live ./collector -run TestLive -v   # live-проверка реального KP API
```

---

## Ограничения MVP / что дальше

- Авто-сообщения продавцам и воркер ответов (фазы 3–4 из ТЗ) — не реализованы.
- Redis не используется (хватает SQLite + in-memory кэша).
- Дальше: миграция на PostgreSQL, прокси/антибан, веб-дашборд.
