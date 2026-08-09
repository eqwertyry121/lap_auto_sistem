# ТЗ v2 (финальное): KP Laptop Arbitrage Bot — «The Bot God of Laptop»

> Консолидировано из `1.txt`–`4.txt`. Правки из `4.txt` (исключение авто-откликов, Postgres как SSOT, батчный экспорт, worker pool, правила CPU) учтены и являются обязательными.
>
> Update 2026-08-04: экспорт рынка переведён с Google Sheets API на локальный CSV-файл (`data/market_history.csv`) — без внешних зависимостей и ключей.

## 1. Цель

Go-бот 24/7 мониторит новые объявления ноутбуков на **KupujemProdajem** (Сербия), каждое новое объявление анализирует через **Gemini** (текст + фото), находит недооценённые лоты и мгновенно доставляет их в **Telegram**. Вся коммуникация с продавцами — вручную пользователем (MVP).

## 2. Стек

| Компонент | Технология | Примечание |
|---|---|---|
| Язык | Go 1.25 | |
| БД (SSOT) | SQLite (MVP) → PostgreSQL (продакшн) | замена только в слое `storage` |
| ИИ | Gemini API (REST), модель `gemini-2.5-flash` (настраивается) | Text + Vision |
| Уведомления | Telegram Bot API (REST) | ALERT / NEED CHECK |
| Экспорт рынка | CSV-файл (`data/market_history.csv`) | батч раз в 30–60 мин |
| Конфигурация | `.env` (godotenv) | шаблон: `.env.example` |

## 3. Пайплайн

### Этап 0 — Сканер рынка (разовый/периодический, `cmd/market-scan`)

Проходит **всю** категорию ноутбуков через Search API (`page=1..N`, сортировка `posted desc`), каждое объявление (id, заголовок, цена, валюта, ссылка) пишет в `market_listings` со статусом `SCANNED`. Заполняет базу для кэша цен и экспорта в CSV. Остановки: конец выдачи, лимит страниц, либо N страниц подряд без новых лотов (при повторных запусках — инкрементальный).

### Этап 1 — Живой цикл (каждые `POLL_INTERVAL_SEC`, по умолчанию 45 с)

1. **Search API** — страница 1, до 30 свежих объявлений.
2. **Дедупликация** — сверка `ad_id` в БД; известные лоты пропускаются.
3. **Спам-фильтр** — по заголовку сразу, по имени продавца после детализации (маркеры магазинов: `store, shop, doo, d.o.o, laptop centar…`; партии: `na stanju, komada, lager…`).
4. **Fetcher** — `GET /api/web/v1/eds/{ad_id}` (описание, HD-фото, атрибуты) **только для новых** ID + пауза `FETCH_DELAY_MS` между запросами.
5. **Оценка Gemini** (worker pool ≤ `GEMINI_CONCURRENCY=5`):
   - **Фаза 1 (текст):** заголовок + описание + атрибуты + рыночная сводка из кэша цен.
   - **Фаза 2 (vision):** если `model_found=false` — все фото объявления (наклейки, шильдики, гравировки, скриншоты характеристик в конце галереи).
6. **Вердикт:**
   - `is_deal=true` → статус `ALERTED` + **ALERT** в Telegram;
   - `need_check=true` → статус `NEED_CHECK` + **NEED CHECK** в Telegram;
   - иначе → `NO_DEAL`, тихо.

### Фоновые воркеры

- **Кэш цен:** каждые `PRICE_CACHE_REFRESH_MIN=30` мин перечитывает БД (последние `PRICE_CACHE_DAYS=90` дней, цены в EUR), строит сводку «похожие лоты: медиана/диапазон» для промпта Gemini.
- **CSV-экспортёр:** каждые `EXPORT_INTERVAL_MIN=30` мин забирает до 500 строк с `synced_to_sheets=0` и дописывает их в `data/market_history.csv` (Дата, Заголовок, Цена, Валюта, Ссылка), затем помечает синхронизированными. Записи во время парсинга **запрещены** — только батч.

## 4. Структура проекта

```
D:\the_bot_god_of_laptop\
├── PLAN.md                  — это ТЗ
├── README.md                — установка и запуск
├── go.mod
├── .env.example             — шаблон ключей
├── main.go                  — бот: воркеры, циклы, graceful shutdown
├── cmd/market-scan/         — сканер всего рынка (этап 0)
├── config/config.go         — конфигурация из env
├── models/models.go         — SearchAd, AdDetailResponse, Listing, Verdict, статусы
├── collector/
│   ├── client.go            — HTTP-клиент KP (заголовки, ротация UA, 429)
│   ├── poller.go            — Search API (страничный)
│   ├── fetcher.go           — /eds/{ad_id}
│   └── spam.go              — фильтр магазинов/партий
├── storage/
│   ├── db.go                — SQLite: market_listings, дедупликация, статусы
│   └── pricecache.go        — кэш рыночных цен + сводка для промпта
├── vision/
│   ├── gemini.go            — REST-клиент Gemini (generateContent)
│   └── evaluator.go         — 2-фазная оценка, промпт с правилами CPU, парсинг JSON
├── notifier/telegram.go     — ALERT / NEED CHECK + inline-кнопка
└── exporter/csv.go          — батчная дозапись рыночной истории в CSV
```

## 5. БД: таблица `market_listings`

| Колонка | Тип | Назначение |
|---|---|---|
| `ad_id` | INTEGER PK | ID объявления KP (дедупликация) |
| `title`, `price`, `currency`, `url` | | данные лота |
| `description`, `seller` | TEXT | заполняются после `/eds/{id}` |
| `status` | TEXT | `NEW` → `SCANNED / SKIPPED_SPAM / ALERTED / NEED_CHECK / NO_DEAL / ERROR` |
| `is_deal`, `need_check`, `model_found` | 0/1 | вердикт Gemini |
| `estimated_profit`, `reason`, `specs` | | вердикт Gemini |
| `synced_to_sheets` | 0/1 | флаг для экспортёра |
| `created_at` | INTEGER (unix) | время обнаружения ботом |

SQLite — только на MVP: запросы написаны так, чтобы перенос на PostgreSQL свёлся к смене драйвера и плейсхолдеров.

## 6. Правила для Gemini (ОБЯЗАТЕЛЬНЫЕ, из 4.txt)

1. **Запрещено** `is_deal=true` без точной модели CPU (например `i5-1135G7`, `Ryzen 5 4600H`). «Просто i5/i7» — недостаточно.
2. На фото искать наклейки Intel Core / AMD Ryzen и шильдики:
   - серый/чёрный стикер Intel → 10–14 поколение;
   - синий стикер Intel → 4–9 поколение (старьё).
3. Поколение не определено → строго `{"is_deal": false, "need_check": true, "reason": "Неизвестно поколение CPU…"}` → бот шлёт `⚠️ NEED CHECK`.
4. Модель не определена даже по фото → `model_found=false`.

**Формат ответа Gemini (строго JSON):**
```json
{"is_deal": false, "need_check": false, "estimated_profit": 0, "reason": "аргументация", "specs": "модель и железо одной строкой", "model_found": true}
```

## 7. Telegram

- **ALERT:** `🚀 НАЙДЕН ПРОФИТ!` — ноутбук, цена продавца, рыночная сводка, ожидаемая прибыль, причина, ссылка + inline-кнопка «Открыть объявление на KP».
- **NEED CHECK:** `⚠️ ТРЕБУЕТСЯ ПРОВЕРКА` — заголовок, цена, причина, ссылка.
- Без настроенного Telegram — алерты печатаются в консоль (dry-run).

## 8. Антибан KP

- Обязательные заголовки: `accept`, `accept-language: sr-RS…`, мобильный `user-agent` (ротация из 4), `x-kp-channel: mobile_web_react`.
- `FETCH_DELAY_MS=700` между запросами деталей; пауза между страницами сканера.
- HTTP 429 → пауза/бэкофф, не более N повторов подряд.
- Тяжёлый `/eds/{id}` дёргается **только** для новых объявлений.

## 9. Scope MVP

**Входит:** сканер рынка, поллер, дедупликация, спам-фильтр, fetcher, Gemini Text+Vision с worker pool, кэш цен, Telegram-алерты, CSV-экспортёр.

**НЕ входит (правка 4.txt):** авто-сообщения продавцам (Reply Worker, `WAITING_SELLER_REPLY`, авторизованные запросы к KP) — коммуникация вручную по ссылке; Redis (SQLite достаточно).

## 10. Конфигурация (.env)

| Переменная | Дефолт | Назначение |
|---|---|---|
| `POLL_INTERVAL_SEC` | 45 | интервал опроса Search API |
| `DB_PATH` | `data/kp_bot.db` | файл SQLite |
| `FETCH_DELAY_MS` | 700 | пауза между `/eds/` |
| `GEMINI_API_KEY` | — | ключ Gemini (обязателен для анализа) |
| `GEMINI_MODEL` | `gemini-2.5-flash` | модель |
| `GEMINI_CONCURRENCY` | 5 | размер worker pool |
| `TELEGRAM_BOT_TOKEN` / `TELEGRAM_CHAT_ID` | — | уведомления |
| `CSV_PATH` | `data/market_history.csv` | CSV-файл экспорта рыночной базы |
| `EXPORT_INTERVAL_MIN` | 30 | период экспорта |
| `PRICE_CACHE_REFRESH_MIN` / `PRICE_CACHE_DAYS` | 30 / 90 | кэш цен |

## 11. Этапы реализации

0. ✅ Каркас проекта + это ТЗ.
1. ✅ **Сканер рынка** `cmd/market-scan` — собрать всю рыночную базу (выполняется в первую очередь).
2. Ядро бота: poller/fetcher/spam/storage.
3. Gemini-оценщик (Text + Vision, pool ≤5).
4. Telegram-нотификатор (ALERT / NEED CHECK).
5. CSV-экспортёр + `main.go` (связка и запуск).
6. Эксплуатация: полный скан рынка → заполнить `.env` → запуск бота.
7. Post-MVP: миграция SQLite → PostgreSQL; фазы 3–4 из `1.txt` (авто-сообщение продавцу на сербском + воркер ответов); прокси/дополнительные антибан-меры; веб-дашборд.
