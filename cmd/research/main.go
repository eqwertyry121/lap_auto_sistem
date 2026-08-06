// research — сбор полного датасета рынка ноутбуков KP для анализа.
//
// В отличие от market-scan (только заголовок/цена), research выкачивает
// детали каждого объявления: описание, продавца, состояние, метки магазина.
// Данные складываются в отдельный SQLite (data/research.db) и выгружаются
// в CSV для анализа фильтров и «нормальности» цен.
//
// Два режима (KP болезненно реагирует на частые запросы деталей — антибот
// «areYouHuman» липкий, поэтому сбор разнесён на этапы):
//
//	go run ./cmd/research -search-only      # быстрый каркас рынка: все страницы,
//	                                        # только заголовок/цена/дата (без /eds/)
//	go run ./cmd/research                   # докачка деталей для собранных строк
//	                                        # (инкрементально, можно перезапускать)
//	go run ./cmd/research -dump-only        # только пересобрать CSV из БД
package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"kpbot/collector"
	"kpbot/filters"
	"kpbot/hb"
	"kpbot/hw"
	"kpbot/models"
	"kpbot/specs"
)

func main() {
	var (
		dbPath            = flag.String("db", "data/research.db", "SQLite-файл датасета")
		csvPath           = flag.String("csv", "data/research.csv", "куда выгрузить CSV (пусто — не выгружать)")
		hwPath            = flag.String("hw", "data/hw.db", "SQLite-файл эталонной базы железа")
		maxPages          = flag.Int("max-pages", 0, "максимум страниц поиска (0 — до конца выдачи)")
		pageDelayMS       = flag.Int("page-delay-ms", 2500, "база паузы между страницами поиска, мс (рандомизируется)")
		detailDelayMS     = flag.Int("detail-delay-ms", 6000, "база паузы между запросами деталей, мс (рандомизируется)")
		jitterPct         = flag.Int("jitter-pct", 40, "рандомизация задержек ±% (ломает роботий ритм для антибота KP)")
		challengePauseMin = flag.Int("challenge-pause-min", 30, "пауза (мин) при антибот-челлендже KP")
		searchOnly        = flag.Bool("search-only", false, "только каркас рынка (без запросов деталей /eds/)")
		doEnrich          = flag.Bool("enrich", false, "не собирать: распознать железо по собранному и заполнить research_specs")
		dumpOnly          = flag.Bool("dump-only", false, "не собирать, только пересобрать CSV из уже собранной БД")
		searchRefresh     = flag.Bool("search-refresh", false, "бэкфилл search-полей (user_id и др.) уже собранных строк без запросов /eds/")
		heartbeatPath     = flag.String("heartbeat", "data/research.heartbeat", "heartbeat-файл живости для watchdog")
	)
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	log := slog.Default()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := openResearch(*dbPath)
	if err != nil {
		log.Error("не удалось открыть базу датасета", "err", err)
		os.Exit(1)
	}
	defer store.close()

	// Heartbeat: критерий живости для watchdog. Горутина независима от
	// основного цикла — файл остаётся свежим даже в 30-минутных антибот-паузах.
	beat := hb.New(*heartbeatPath, "starting")
	go beat.Run(ctx, time.Minute)

	// Самозалечивание: OK-строки с пустым ответом /eds/ (мягкий антибот)
	// возвращаются в очередь. Идемпотентно, безопасно при каждом старте.
	if healed, herr := store.healEmptyOK(ctx); herr != nil {
		log.Warn("самозалечивание датасета не удалось", "err", herr)
	} else if healed > 0 {
		log.Info("самозалечивание: строки с пустыми деталями возвращены в очередь", "строк", healed)
	}

	if *dumpOnly {
		beat.SetState("dump")
		n, err := dumpCSV(ctx, store, *csvPath)
		if err != nil {
			log.Error("выгрузка CSV", "err", err)
			os.Exit(1)
		}
		log.Info("CSV пересобран", "path", *csvPath, "rows", n)
		return
	}

	if *doEnrich {
		beat.SetState("enrich")
		if err := enrich(ctx, store, *hwPath); err != nil {
			log.Error("обогащение датасета", "err", err)
			os.Exit(1)
		}
		return
	}

	client := collector.NewClient()
	log.Info("исследовательский сбор рынка ноутбуков KP",
		"db", *dbPath, "max_pages", *maxPages, "search_only", *searchOnly,
		"page_delay_ms", *pageDelayMS, "detail_delay_ms", *detailDelayMS, "jitter_pct", *jitterPct)

	started := time.Now()
	challengePause := time.Duration(*challengePauseMin) * time.Minute
	var (
		page       = 1
		seen       int
		newRows    int
		detailed   int
		totalPages int
		stopRun    bool // антибот не снялся — сворачиваемся, прогресс сохранён
	)

	for {
		if ctx.Err() != nil {
			log.Warn("остановлено пользователем")
			break
		}
		if stopRun {
			break
		}
		if *maxPages > 0 && page > *maxPages {
			log.Info("достигнут лимит страниц", "max_pages", *maxPages)
			break
		}

		beat.SetState("search")
		res, err := searchWithRetry(ctx, client, page, challengePause, *jitterPct, log, beat)
		if err != nil {
			if ctx.Err() == nil {
				log.Error("страница не далась даже после повторов — останавливаюсь", "page", page, "err", err)
			}
			break
		}

		ads := res.Ads
		if len(ads) == 0 {
			log.Info("выдача закончилась", "page", page)
			break
		}
		if totalPages == 0 && res.Pages > 0 {
			totalPages = res.Pages
			log.Info("KP заявил предел выдачи", "pages", totalPages, "total_ads", res.Total)
		}

		// Режим докачки деталей: сначала похожие на частников — это самые
		// ценные данные для медиан и калибровки; магазинный верх никуда не денется.
		if !*searchOnly && !*searchRefresh {
			prioritizeIndividuals(ads)
		}

		pageNew := 0
		for _, ad := range ads {
			if ctx.Err() != nil || stopRun {
				break
			}
			seen++

			st, err := store.status(ctx, ad.AdID)
			if err != nil {
				log.Error("чтение статуса", "ad_id", ad.AdID, "err", err)
				continue
			}

			// Бэкфилл search-полей: существующие строки обновляются ЧАСТИЧНО
			// (детали не затираются), новые вставляются как SEARCH.
			if *searchRefresh {
				row := searchLevelRow(ad)
				if st == "" {
					if err := store.upsert(ctx, row); err != nil {
						log.Error("запись в датасет", "ad_id", ad.AdID, "err", err)
						continue
					}
					newRows++
				} else if err := store.refreshSearch(ctx, row); err != nil {
					log.Error("бэкфилл search-полей", "ad_id", ad.AdID, "err", err)
					continue
				}
				if err := store.observeSeller(ctx, ad.UserID, false, ad.KPIzlog, 0, ""); err != nil {
					log.Warn("реестр продавцов", "user_id", ad.UserID, "err", err)
				}
				pageNew++
				continue
			}

			// Каркас: сохраняем только то, чего ещё нет вообще.
			if *searchOnly {
				if st != "" {
					continue
				}
				row := searchLevelRow(ad)
				if err := store.upsert(ctx, row); err != nil {
					log.Error("запись в датасет", "ad_id", ad.AdID, "err", err)
					continue
				}
				if err := store.observeSeller(ctx, ad.UserID, false, ad.KPIzlog, 0, ""); err != nil {
					log.Warn("реестр продавцов", "user_id", ad.UserID, "err", err)
				}
				pageNew++
				newRows++
				continue
			}

			// Детали: финальные строки не трогаем, SEARCH/ERROR докачиваем.
			if st == "OK" || st == "NOT_FOUND" {
				continue
			}

			beat.SetState("details")
			row := searchLevelRow(ad)
			detail, err := fetchDetailPolite(ctx, client, ad.AdID, time.Duration(*challengePauseMin)*time.Minute, *jitterPct, log, &stopRun, beat)
			switch {
			case err == nil && detailEmpty(detail):
				// Мягкий антибот KP: HTTP 200, но info без описания/продавца.
				// НЕ помечаем OK — иначе строка никогда не ретраится.
				log.Warn("пустой ответ /eds/ (мягкий антибот?) — повторим позже", "ad_id", ad.AdID)
				row.FetchStatus = "ERROR"
			case err == nil:
				row.Description = stripHTML(detail.Description)
				row.Seller = detail.Seller()
				if b, jerr := json.Marshal(detail.Attributes); jerr == nil {
					row.AttrsJSON = string(b)
				}
				row.Condition = detail.Condition
				row.IsTrader = detail.IsTrader()
				row.KPIzlog = detail.KPIzlog
				row.Kind = classify(detail.Condition, row.Title, row.Description)
				row.FetchStatus = "OK"
				detailed++
			case errors.Is(err, collector.ErrNotFound):
				row.FetchStatus = "NOT_FOUND"
			default:
				if ctx.Err() != nil || stopRun {
					break
				}
				log.Error("детализация", "ad_id", ad.AdID, "err", err)
				row.FetchStatus = "ERROR"
			}

			row.FetchedAt = time.Now()
			if err := store.upsert(ctx, row); err != nil {
				log.Error("запись в датасет", "ad_id", ad.AdID, "err", err)
				continue
			}
			// Реестр продавцов: детали дают метку торговца, отзывы и возраст
			// аккаунта; сбой детализации — хотя бы search-сигналы.
			if detail != nil {
				err = store.observeSeller(ctx, ad.UserID, detail.IsTrader(), detail.KPIzlog,
					int(detail.User.Reviews), detail.User.Created)
			} else {
				err = store.observeSeller(ctx, ad.UserID, false, ad.KPIzlog, 0, "")
			}
			if err != nil {
				log.Warn("реестр продавцов", "user_id", ad.UserID, "err", err)
			}
			pageNew++
			if st == "" {
				newRows++
			}

			if !sleepJitter(ctx, time.Duration(*detailDelayMS)*time.Millisecond, *jitterPct) {
				break
			}
		}

		log.Info("страница обработана",
			"page", page, "ads", len(ads), "touched", pageNew,
			"new_rows", newRows, "detailed", detailed)

		if res.HasReachedLimit || res.HasReachedMax {
			log.Info("KP: достигнут предел выдачи", "page", page)
			break
		}
		if totalPages > 0 && page >= totalPages {
			log.Info("достигнут заявленный KP лимит страниц", "page", page, "total_pages", totalPages)
			break
		}

		page++
		if !sleepJitter(ctx, time.Duration(*pageDelayMS)*time.Millisecond, *jitterPct) {
			break
		}
	}

	total, _ := store.count(ctx)
	byKind, _ := store.countByKind(ctx)
	log.Info("сбор завершён",
		"страниц", page, "просмотрено", seen, "новых_строк", newRows, "с_деталями", detailed,
		"всего_с_деталями", total, "по_типам", fmt.Sprint(byKind),
		"длительность", time.Since(started).Round(time.Second))

	if *csvPath != "" {
		n, err := dumpCSV(ctx, store, *csvPath)
		if err != nil {
			log.Error("выгрузка CSV", "err", err)
			os.Exit(1)
		}
		log.Info("CSV выгружен", "path", *csvPath, "rows", n)
	}
}

// searchLevelRow — строка по данным поисковой страницы (без деталей).
func searchLevelRow(ad models.SearchAd) researchRow {
	return researchRow{
		AdID:        ad.AdID,
		Title:       ad.Name,
		Price:       float64(ad.Price),
		Currency:    models.NormalizeCurrency(ad.Currency),
		URL:         ad.URL(),
		Posted:      ad.Posted,
		Condition:   ad.Condition, // KP отдаёт condition уже на уровне поиска
		KPIzlog:     ad.KPIzlog,
		UserID:      ad.UserID,
		ViewCount:   ad.ViewCount,
		IsRenewed:   ad.IsRenewed,
		Snippet:     strings.TrimSpace(ad.DescriptionSnip),
		Kind:        "UNKNOWN",
		FetchStatus: "SEARCH",
	}
}

// searchWithRetry — устойчивый запрос страницы: 429 → бэкофф (до 3 раз),
// челлендж → длинная пауза (до 3 раз), сетевые сбои → до 5 попыток с
// растущим ожиданием. Ошибка возвращается, только когда всё исчерпано.
func searchWithRetry(ctx context.Context, c *collector.Client, page int,
	challengePause time.Duration, jitterPct int, log *slog.Logger, beat *hb.Heartbeat) (*models.SearchResults, error) {
	var lastErr error
	rateLimits, challenges := 0, 0
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		beat.SetState("search")
		res, err := c.SearchPageFull(ctx, page)
		if err == nil {
			return res, nil
		}
		lastErr = err
		switch {
		case errors.Is(err, collector.ErrRateLimited):
			rateLimits++
			if rateLimits > 3 {
				return nil, err
			}
			backoff := jittered(time.Duration(30*rateLimits)*time.Second, jitterPct)
			log.Warn("429 на поиске — бэкофф", "page", page, "backoff", backoff)
			if !sleepCtx(ctx, backoff) {
				return nil, ctx.Err()
			}
		case errors.Is(err, collector.ErrChallenge):
			challenges++
			if challenges > 3 {
				return nil, err
			}
			pause := jittered(challengePause, jitterPct)
			log.Warn("антибот-челлендж на поиске — длинная пауза", "page", page, "pause", pause)
			beat.SetState("challenge_pause")
			if !sleepCtx(ctx, pause) {
				return nil, ctx.Err()
			}
		default:
			// сетевые сбои, таймауты и пр.
			reclassify := false
			for i := 1; i <= 5; i++ {
				backoff := jittered(time.Duration(5*i)*time.Second, jitterPct)
				log.Warn("ошибка страницы — повтор", "page", page, "попытка", i, "backoff", backoff, "err", err)
				if !sleepCtx(ctx, backoff) {
					return nil, ctx.Err()
				}
				res2, err2 := c.SearchPageFull(ctx, page)
				if err2 == nil {
					return res2, nil
				}
				err, lastErr = err2, err2
				if errors.Is(err2, collector.ErrChallenge) || errors.Is(err2, collector.ErrRateLimited) {
					reclassify = true // обработается следующей итерацией внешнего цикла
					break
				}
			}
			if !reclassify {
				return nil, lastErr
			}
		}
	}
}

// fetchDetailPolite — детали с бэкоффом на 429 и «вежливой» реакцией на
// антибот-челлендж: длинные паузы по challengePause, до maxChallengePauses
// циклов (обычно снимается за 30–60 мин); если так и не снялся — ставим
// stopRun (прогресс уже сохранён, перезапустим позже).
func fetchDetailPolite(ctx context.Context, c *collector.Client, adID int64,
	challengePause time.Duration, jitterPct int, log *slog.Logger, stopRun *bool, beat *hb.Heartbeat) (*models.AdDetail, error) {
	const maxChallengePauses = 6
	var lastErr error
	challengePauses := 0
	for attempt := 0; attempt < 12; attempt++ {
		d, err := c.FetchDetail(ctx, adID)
		if err == nil {
			return d, nil
		}
		if errors.Is(err, collector.ErrNotFound) || ctx.Err() != nil {
			return nil, err
		}
		lastErr = err

		if errors.Is(err, collector.ErrChallenge) {
			challengePauses++
			if challengePauses > maxChallengePauses {
				log.Error("антибот-челлендж не снялся после всех пауз — останавливаю прогон; перезапустите позже")
				*stopRun = true
				return nil, err
			}
			log.Warn("антибот-челлендж KP — длинная пауза",
				"ad_id", adID, "pause", challengePause, "пауза", challengePauses, "из", maxChallengePauses)
			beat.SetState("challenge_pause")
			if !sleepCtx(ctx, jittered(challengePause, jitterPct)) {
				return nil, ctx.Err()
			}
			continue
		}
		if errors.Is(err, collector.ErrRateLimited) {
			backoff := jittered(time.Duration(30*(attempt%3+1))*time.Second, jitterPct)
			log.Warn("429 на деталях — пауза", "ad_id", adID, "backoff", backoff)
			if !sleepCtx(ctx, backoff) {
				return nil, ctx.Err()
			}
			continue
		}
		if !sleepCtx(ctx, jittered(3*time.Second, jitterPct)) {
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

// Маркеры хлама — единые словари в пакете filters (SSOT): их же использует
// живая воронка бота (PLAN_v4, Фаза 3).

// detailEmpty — ответ /eds/ без содержимого (мягкая форма антибота KP):
// ни описания, ни продавца. У реального лота владелец есть всегда.
func detailEmpty(d *models.AdDetail) bool {
	return d.Description == "" && d.Seller() == ""
}

// classify — NEW / USED / BROKEN / UNKNOWN. Сначала фильтр хлама L2
// (маркеры + поле KP condition), затем официальное состояние.
func classify(condition, title, desc string) string {
	v := filters.L2(filters.AdFacts{Title: title, Description: desc, Condition: condition})
	if v.Class == filters.JunkPartsOnly || v.Class == filters.JunkUncertain {
		// сомнительные маркеры тоже выводим из медиан (консервативно);
		// живая воронка покажет по ним ⚠️ CHECK
		return "BROKEN"
	}
	switch condition {
	case "new":
		return "NEW"
	case "used":
		return "USED"
	}
	return "UNKNOWN"
}

var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

// stripHTML убирает теги и нормализует пробелы (описание KP приходит в HTML).
func stripHTML(s string) string {
	s = htmlTagRe.ReplaceAllString(s, " ")
	for _, pair := range [][2]string{{"&nbsp;", " "}, {"&amp;", "&"}, {"&quot;", "\""}, {"&#39;", "'"}, {"&lt;", "<"}, {"&gt;", ">"}} {
		s = strings.ReplaceAll(s, pair[0], pair[1])
	}
	return strings.Join(strings.Fields(s), " ")
}

// dumpCSV выгружает датасет в CSV (с UTF-8 BOM для Excel).
func dumpCSV(ctx context.Context, store *researchStore, path string) (int, error) {
	if path == "" {
		return 0, nil
	}
	rows, err := store.all(ctx)
	if err != nil {
		return 0, err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return 0, err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	if _, err := f.WriteString("\ufeff"); err != nil {
		return 0, err
	}
	w := csv.NewWriter(f)
	if err := w.Write([]string{"ID", "Дата_публикации", "Заголовок", "Цена", "Валюта",
		"Состояние_KP", "Тип", "Магазин", "Продавец", "Описание", "Ссылка", "Полнота"}); err != nil {
		return 0, err
	}
	for _, r := range rows {
		shop := ""
		if r.IsTrader || r.KPIzlog {
			shop = "магазин"
		}
		rec := []string{
			strconv.FormatInt(r.AdID, 10),
			r.Posted,
			r.Title,
			strconv.FormatFloat(r.Price, 'f', -1, 64),
			r.Currency,
			r.Condition,
			r.Kind,
			shop,
			r.Seller,
			r.Description,
			r.URL,
			r.FetchStatus,
		}
		if err := w.Write(rec); err != nil {
			return 0, err
		}
	}
	w.Flush()
	return len(rows), w.Error()
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// rng — единственный источник случайности для задержек.
var rng = rand.New(rand.NewSource(time.Now().UnixNano()))

// jittered — база ±jitterPct% (равномерно). Рандомизация ломает ровный
// «роботий» ритм запросов, по которому KP распознаёт автоматизацию.
func jittered(base time.Duration, jitterPct int) time.Duration {
	if jitterPct <= 0 {
		return base
	}
	j := float64(jitterPct) / 100.0
	mult := 1.0 + (rng.Float64()*2-1)*j // [1-j, 1+j]
	return time.Duration(float64(base) * mult)
}

// sleepJitter — сон с рандомизированной длительностью, отменяемый по ctx.
func sleepJitter(ctx context.Context, base time.Duration, jitterPct int) bool {
	return sleepCtx(ctx, jittered(base, jitterPct))
}

// prioritizeIndividuals — сортирует лоты так, чтобы первыми шли похожие на
// частников (не витрина KP Izlog и без автообновления). Они самые ценные для
// медиан и калибровки фильтров; магазинный верх выдачи и так никуда не денется.
// Стабильная сортировка: при равном счёте сохраняется исходный порядок.
func prioritizeIndividuals(ads []models.SearchAd) {
	sort.SliceStable(ads, func(i, j int) bool {
		return individualScore(ads[i]) > individualScore(ads[j])
	})
}

// individualScore — выше = больше похож на частника.
func individualScore(ad models.SearchAd) int {
	s := 0
	if !ad.KPIzlog {
		s += 2 // витрина = почти наверняка магазин
	}
	if !ad.IsRenewed {
		s += 1 // автообновление лотов — поведение магазинов
	}
	return s
}

// enrich — распознаёт железо (пакет specs) по всем лотам датасета и
// сопоставляет с эталонной базой (hw.db); результат пишет в research_specs.
func enrich(ctx context.Context, store *researchStore, hwPath string) error {
	cpus, err := hw.LoadCPUs(ctx, hwPath)
	if err != nil {
		return fmt.Errorf("эталон CPU: %w", err)
	}
	gpus, err := hw.LoadGPUs(ctx, hwPath)
	if err != nil {
		return fmt.Errorf("эталон GPU: %w", err)
	}

	rows, err := store.db.QueryContext(ctx,
		`SELECT ad_id, title, description FROM research_ads WHERE fetch_status IN ('SEARCH','OK')`)
	if err != nil {
		return err
	}
	type srcRow struct {
		adID  int64
		title string
		desc  string
	}
	var src []srcRow
	for rows.Next() {
		var r srcRow
		if err := rows.Scan(&r.adID, &r.title, &r.desc); err != nil {
			rows.Close()
			return err
		}
		src = append(src, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	// Одна транзакция + подготовленный statement: без этого 24k вставок
	// с индивидуальным fsync длятся минуты.
	// Защита результата Gemini: regex-проход НЕ затирает строки, где уже
	// отработал Gemini (источник старше в иерархии L3).
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO research_specs (ad_id, cpu_model, cpu_score, ram_gb, ssd_gb, gpu_model, gpu_score, updated_at, source)
VALUES (?,?,?,?,?,?,?,?, 'regex')
ON CONFLICT(ad_id) DO UPDATE SET
	cpu_model=CASE WHEN research_specs.source LIKE 'gemini%' THEN research_specs.cpu_model ELSE excluded.cpu_model END,
	cpu_score=CASE WHEN research_specs.source LIKE 'gemini%' THEN research_specs.cpu_score ELSE excluded.cpu_score END,
	ram_gb=CASE WHEN research_specs.source LIKE 'gemini%' THEN research_specs.ram_gb ELSE excluded.ram_gb END,
	ssd_gb=CASE WHEN research_specs.source LIKE 'gemini%' THEN research_specs.ssd_gb ELSE excluded.ssd_gb END,
	gpu_model=CASE WHEN research_specs.source LIKE 'gemini%' THEN research_specs.gpu_model ELSE excluded.gpu_model END,
	gpu_score=CASE WHEN research_specs.source LIKE 'gemini%' THEN research_specs.gpu_score ELSE excluded.gpu_score END,
	source=CASE WHEN research_specs.source LIKE 'gemini%' THEN research_specs.source ELSE excluded.source END,
	updated_at=excluded.updated_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now().Unix()

	var total, cpuMatched, gpuMatched int
	for _, r := range src {
		sp := specs.Extract(r.title + " " + r.desc)
		row := specsRow{AdID: r.adID, RAMGB: sp.RAMGB, SSDGB: sp.SSDGB}

		if sp.CPU != "" {
			if c, ok := cpus[hw.Key(sp.CPU)]; ok {
				row.CPUModel, row.CPUScore = c.Name, c.Score
				cpuMatched++
			} else {
				row.CPUModel = sp.CPU // распознан, но в эталоне отсутствует
			}
		}
		if sp.GPU != "" {
			if g, ok := matchGPU(gpus, sp.GPU); ok {
				row.GPUModel, row.GPUScore = g.Name, g.Score
				gpuMatched++
			} else {
				row.GPUModel = sp.GPU
			}
		}

		if _, err := stmt.ExecContext(ctx, row.AdID, row.CPUModel, row.CPUScore,
			row.RAMGB, row.SSDGB, row.GPUModel, row.GPUScore, now); err != nil {
			return err
		}
		total++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	slog.Info("обогащение завершено",
		"лотов", total,
		"cpu_распознано_и_сверено", cpuMatched,
		"gpu_распознано_и_сверено", gpuMatched)
	return nil
}

// matchGPU ищет эталонную видеокарту по токену («RTX 3060»). Ноутбучные
// варианты (Mobile/Laptop) в приоритете; «RTX 3060» не должен сматчиться
// в «RTX 3060 Ti» — лишние слова-модификаторы запрещены.
func matchGPU(gpus map[string]hw.GPU, token string) (hw.GPU, bool) {
	tok := strings.ToLower(token)
	var best hw.GPU
	bestClass, bestExtra, found := 2, 99, false
	for k, g := range gpus {
		if !strings.Contains(k, tok) {
			continue
		}
		extra := strings.Fields(strings.TrimSpace(strings.Replace(k, tok, "", 1)))
		bad, isMobile := false, false
		for _, w := range extra {
			switch w {
			case "mobile", "laptop", "gpu":
				isMobile = true
			default:
				bad = true // модификатор (ti, super…) — не наш токен
			}
		}
		if bad {
			continue
		}
		class := 1 // desktop
		if isMobile {
			class = 0
		}
		if !found || class < bestClass || (class == bestClass && len(extra) < bestExtra) {
			best, bestClass, bestExtra, found = g, class, len(extra), true
		}
	}
	return best, found
}
