package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"kpbot/collector"
	"kpbot/config"
	"kpbot/control"
	"kpbot/exporter"
	"kpbot/filters"
	"kpbot/funnel"
	"kpbot/hb"
	"kpbot/models"
	"kpbot/notifier"
	"kpbot/storage"
	"kpbot/vision"
)

type evalJob struct {
	listing models.Listing
	detail  *models.AdDetail
	hint    string
}

// botState — живое состояние бота между циклами опроса (PLAN_v4, Фаза 0):
// heartbeat для watchdog, пауза при антибот-челлендже, счётчик челленджей,
// ручной стоп/старт из пульта Telegram.
type botState struct {
	beat           *hb.Heartbeat
	pausedUntil    time.Time
	challengeCount atomic.Int64
	startedAt      time.Time
	manualPaused   atomic.Bool // пульт: ⏹ Стоп
}

// enterChallengePause — реакция на антибот-челлендж KP: длинная пауза вместо
// бессмысленного долбления (челлендж «липкий», снимается за 30–60 минут).
func (s *botState) enterChallengePause(cfg *config.Config) {
	s.challengeCount.Add(1)
	s.pausedUntil = time.Now().Add(cfg.ChallengePause)
	s.beat.SetState("challenge_pause")
}

func main() {
	cfg := config.Load()
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Warn("получен сигнал остановки — завершаемся gracefully")
		cancel()
	}()

	store, err := storage.Open(cfg.DBPath)
	if err != nil {
		log.Error("не удалось открыть БД", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	// Heartbeat: единственный критерий живости для watchdog. Горутина живёт
	// независимо от основного цикла — файл свежий даже в антибот-паузе.
	st := &botState{
		beat:      hb.New(cfg.HeartbeatPath, "starting"),
		startedAt: time.Now(),
	}
	go st.beat.Run(ctx, time.Minute)

	priceCache := storage.NewPriceCache(store, cfg.PriceCacheDays)
	priceCache.Refresh(ctx)

	gemini := vision.NewGeminiClient(cfg.GeminiAPIKey, cfg.GeminiModel).SetLimits(cfg.GeminiConcurrency, cfg.GeminiDailyLimit)
	evaluator := vision.NewEvaluator(gemini, cfg.GeminiConcurrency)
	tg := notifier.NewWithQueue(cfg.TelegramToken, cfg.TelegramChatID, cfg.AlertQueuePath)
	kp := collector.NewClient()

	var csvExp *exporter.CSVExporter
	if exp, err := exporter.NewCSV(cfg.CSVPath); err == nil {
		csvExp = exp
		log.Info("csv-экспортёр включён", "path", cfg.CSVPath)
	} else {
		log.Warn("csv-экспортёр отключён", "err", err)
	}
	if !tg.Enabled() {
		log.Warn("Telegram не настроен — алерты будут в консоль (dry-run)")
	}
	if cfg.GeminiAPIKey == "" {
		log.Warn("GEMINI_API_KEY не задан — оценка будет падать")
	}

	// Пул воркеров оценки.
	jobs := make(chan evalJob, 64)
	var wg sync.WaitGroup
	for i := 0; i < cfg.GeminiConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			evalWorker(ctx, jobs, store, evaluator, tg, log)
		}()
	}

	// Фон: обновление кэша цен.
	go func() {
		t := time.NewTicker(cfg.PriceCacheRefresh)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				priceCache.Refresh(ctx)
			}
		}
	}()

	// Фон: батчный экспорт рыночной базы в CSV.
	if csvExp != nil {
		go func() {
			// первый экспорт сразу, чтобы не ждать первый тик
			exportOnce(ctx, store, csvExp, log)
			t := time.NewTicker(cfg.ExportInterval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					exportOnce(ctx, store, csvExp, log)
				}
			}
		}()
	}

	// Фон: дневной дайджест (доказательство жизни для человека).
	go digestLoop(ctx, cfg, store, tg, st, log)

	// Воронка L0–L5: рыночная модель + эталон железа (PLAN_v4, Фаза 4).
	fnl := funnel.NewFunnel()
	fnl.RefreshMarket(ctx, cfg, log)
	go func() {
		t := time.NewTicker(cfg.MarketRefresh)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				fnl.RefreshMarket(ctx, cfg, log)
			}
		}
	}()

	// Пульт управления в Telegram: Старт / Стоп / Статус.
	if tg.Enabled() {
		rootDir, _ := os.Getwd()
		panel := control.New(tg, cfg.TelegramToken, cfg.TelegramChatID, rootDir,
			&st.manualPaused, st.startedAt, &st.challengeCount, fnl)
		if panel != nil {
			go panel.Run(ctx, log)
		}
	}

	log.Info("бот запущен",
		"poll_interval", cfg.PollInterval.String(),
		"gemini_concurrency", cfg.GeminiConcurrency,
		"gemini_daily_limit", cfg.GeminiDailyLimit,
		"gemini_model", cfg.GeminiModel,
		"gemini_text_model", cfg.GeminiTextModel,
		"gemini_vision_model", cfg.GeminiVisionModel,
		"gemini_search_model", cfg.GeminiSearchModel,
		"heartbeat", cfg.HeartbeatPath,
		"challenge_pause", cfg.ChallengePause.String(),
		"funnel_shadow", cfg.FunnelShadow,
		"diamond_dev_pct", cfg.DiamondDevPct)

	if tg.Enabled() {
		msg := fmt.Sprintf("🤖 <b>Бот запущен</b>\nПоллинг: %s · дайджест: ежедневно %s · heartbeat: %s",
			cfg.PollInterval, cfg.DigestAt, cfg.HeartbeatPath)
		if err := tg.SendRaw(ctx, msg); err != nil {
			log.Error("telegram: сообщение о старте", "err", err)
		}
	}

	// Первый цикл сразу, дальше по таймеру.
	pollOnce(ctx, kp, store, priceCache, jobs, cfg, log, st, fnl, gemini, tg)

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("остановка основного цикла, ждём воркеров")
			close(jobs)
			wg.Wait()
			if tg.Enabled() {
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
				_ = tg.SendRaw(stopCtx, "🛑 <b>Бот остановлен</b> (graceful shutdown)")
				stopCancel()
			}
			log.Info("бот остановлен")
			return
		case <-ticker.C:
			pollOnce(ctx, kp, store, priceCache, jobs, cfg, log, st, fnl, gemini, tg)
		}
	}
}

func digestLoop(ctx context.Context, cfg *config.Config, store *storage.Store,
	tg *notifier.Telegram, st *botState, log *slog.Logger) {
	h, m, err := parseDigestTime(cfg.DigestAt)
	if err != nil {
		log.Error("дайджест отключён", "err", err)
		return
	}
	next := nextDigestTime(time.Now(), h, m)
	log.Info("дайджест запланирован", "next", next.Format("2006-01-02 15:04"))
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if now.Before(next) {
				continue
			}
			sendDigest(ctx, cfg, store, tg, st, log)
			next = nextDigestTime(time.Now(), h, m)
		}
	}
}

func sendDigest(ctx context.Context, cfg *config.Config, store *storage.Store,
	tg *notifier.Telegram, st *botState, log *slog.Logger) {
	if !tg.Enabled() {
		return
	}
	since := time.Now().Add(-24 * time.Hour)
	byStatus, err := store.CountByStatusSince(ctx, since)
	if err != nil {
		log.Error("дайджест: статусы", "err", err)
		return
	}
	alerts, err := store.RecentAlerts(ctx, since, 3)
	if err != nil {
		log.Error("дайджест: алерты", "err", err)
		alerts = nil
	}
	total, detailed := 0, 0
	if t, d, cerr := storage.ResearchCoverage(ctx, cfg.ResearchDBPath); cerr == nil {
		total, detailed = t, d
	} else {
		log.Warn("дайджест: покрытие research.db недоступно", "err", cerr)
	}
	text := digestText(time.Since(st.startedAt), byStatus, st.challengeCount.Load(),
		total, detailed, alerts)
	if err := tg.SendRaw(ctx, text); err != nil {
		log.Error("дайджест: отправка", "err", err)
	} else {
		log.Info("дайджест отправлен")
	}
}

func evalWorker(ctx context.Context, jobs <-chan evalJob, store *storage.Store, ev *vision.Evaluator, tg *notifier.Telegram, log *slog.Logger) {
	for job := range jobs {
		if ctx.Err() != nil {
			return
		}
		l := job.listing
		v, err := ev.Evaluate(ctx, l, job.detail, job.hint)
		if err != nil {
			log.Error("оценка не удалась", "ad_id", l.AdID, "err", err)
			_ = store.SetStatus(ctx, l.AdID, models.StatusError)
			continue
		}
		switch {
		case v.IsDeal:
			_ = store.SaveVerdict(ctx, l.AdID, v, models.StatusAlerted)
			if err := tg.SendAlert(ctx, l, v, job.hint); err != nil {
				log.Error("telegram alert", "ad_id", l.AdID, "err", err)
			} else {
				log.Info("ALERT отправлен", "ad_id", l.AdID, "profit", v.EstimatedProfit)
			}
		case v.NeedCheck:
			_ = store.SaveVerdict(ctx, l.AdID, v, models.StatusNeedCheck)
			if err := tg.SendNeedCheck(ctx, l, v); err != nil {
				log.Error("telegram need_check", "ad_id", l.AdID, "err", err)
			} else {
				log.Info("NEED CHECK отправлен", "ad_id", l.AdID)
			}
		default:
			_ = store.SaveVerdict(ctx, l.AdID, v, models.StatusNoDeal)
		}
	}
}

func pollOnce(ctx context.Context, kp *collector.Client, store *storage.Store, cache *storage.PriceCache, jobs chan<- evalJob, cfg *config.Config, log *slog.Logger, st *botState, fnl *funnel.Funnel, gem *vision.GeminiClient, tg *notifier.Telegram) {
	// Ручной стоп из пульта: поллинг на паузе, процесс и пульт живы.
	if st.manualPaused.Load() {
		st.beat.SetState("paused")
		return
	}
	// Антибот-пауза: не долбим KP, пока челлендж не должен был сняться.
	if time.Now().Before(st.pausedUntil) {
		return
	}
	if st.beat.State() != "polling" {
		if st.beat.State() == "challenge_pause" {
			log.Info("антибот-пауза завершена — возобновляю опрос")
		}
		st.beat.SetState("polling")
	}

	ads, err := kp.Search(ctx)
	if err != nil {
		switch {
		case errors.Is(err, collector.ErrRateLimited):
			log.Warn("429 на поиске — пропускаем цикл")
		case errors.Is(err, collector.ErrChallenge):
			// Раньше бот ломился каждые 45с всю паузу челленджа — пустые
			// запросы, продлевающие бан. Теперь одна запись и длинная пауза.
			st.enterChallengePause(cfg)
			log.Warn("антибот-челлендж на поиске — длинная пауза",
				"pause", cfg.ChallengePause.String(),
				"всего_челленджей", st.challengeCount.Load())
		default:
			if ctx.Err() == nil {
				log.Error("поиск", "err", err)
			}
		}
		return
	}

	for _, ad := range ads {
		if ctx.Err() != nil {
			return
		}
		exists, err := store.Exists(ctx, ad.AdID)
		if err != nil {
			log.Error("проверка дубля", "ad_id", ad.AdID, "err", err)
			continue
		}
		if exists {
			continue
		}

		price := float64(ad.Price)
		cur := models.NormalizeCurrency(ad.Currency)
		l := models.Listing{
			AdID:      ad.AdID,
			Title:     ad.Name,
			Price:     price,
			Currency:  cur,
			URL:       ad.URL(),
			Status:    models.StatusNew,
			CreatedAt: time.Now(),
		}
		if err := store.InsertListing(ctx, l); err != nil {
			log.Error("запись лота", "ad_id", ad.AdID, "err", err)
			continue
		}

		// PLAN_v5, Фаза A: бан запрещённых линеек (MacBook) — мгновенно,
		// ещё до запроса /eds/ (экономия лимитов KP).
		if banned, hit := filters.IsBannedModel(ad.Name, cfg.BannedModels); banned {
			log.Info("бан-лист (заголовок)", "ad_id", ad.AdID, "маркер", hit)
			_ = store.SetStatus(ctx, ad.AdID, models.StatusSkippedBan)
			continue
		}

		if spam, reason := collector.IsSpam(ad.Name, ""); spam {
			log.Info("спам-фильтр (заголовок)", "ad_id", ad.AdID, "reason", reason)
			_ = store.SetStatus(ctx, ad.AdID, models.StatusSkippedSpam)
			continue
		}

		// Пауза, чтобы не долбить /eds/ слишком часто.
		select {
		case <-ctx.Done():
			return
		case <-time.After(cfg.FetchDelay):
		}

		detail, err := kp.FetchDetail(ctx, ad.AdID)
		if err != nil {
			if errors.Is(err, collector.ErrRateLimited) {
				log.Warn("429 на деталях — прерываем цикл")
				return
			}
			if errors.Is(err, collector.ErrChallenge) {
				// Челлендж липкий на уровне IP: следующий search тоже упадёт.
				// Возвращаем лот в очередь и уходим в длинную паузу.
				log.Warn("антибот-челлендж KP — возвращаем лот в очередь и ставим паузу",
					"pause", cfg.ChallengePause.String())
				_ = store.Delete(ctx, ad.AdID)
				st.enterChallengePause(cfg)
				return
			}
			if errors.Is(err, collector.ErrNotFound) {
				_ = store.SetStatus(ctx, ad.AdID, models.StatusError)
				continue
			}
			if ctx.Err() == nil {
				log.Error("детализация", "ad_id", ad.AdID, "err", err)
			}
			_ = store.SetStatus(ctx, ad.AdID, models.StatusError)
			continue
		}

		// Мягкий антибот KP: HTTP 200, но info без описания/продавца.
		// НЕ удаляем лот из БД: иначе он снова окажется «новым» в следующем
		// цикле и мы будем долбить /eds/ каждые 45 с (горячий цикл запросов).
		// Статус ERROR сохраняет дедупликацию; повтором займётся механизм
		// ретраев (Фаза 4).
		if detail.Description == "" && detail.Seller() == "" {
			log.Warn("пустой ответ /eds/ (мягкий антибот?) — лот помечен ERROR, повтора нет",
				"ad_id", ad.AdID)
			_ = store.SetStatus(ctx, ad.AdID, models.StatusError)
			continue
		}

		l.Description = detail.Description
		l.Seller = detail.Seller()
		_ = store.UpdateDetails(ctx, ad.AdID, detail.Description, detail.Seller(), price, cur)

		if spam, reason := collector.IsSpam(ad.Name, detail.Seller()); spam {
			log.Info("спам-фильтр (продавец)", "ad_id", ad.AdID, "reason", reason)
			_ = store.SetStatus(ctx, ad.AdID, models.StatusSkippedSpam)
			continue
		}

		if cfg.FunnelShadow {
			// ТЕНЬ: воронка решает параллельно и пишет только аудит;
			// алерты идут по старому Gemini-пути (сравнение вердиктов).
			go func(ad models.SearchAd, detail *models.AdDetail) {
				out := funnel.Run(ctx, fnl, cfg, gem, log, ad, detail)
				if err := store.SaveFunnelAudit(ctx, ad.AdID, out.Audit); err != nil {
					log.Warn("воронка (тень): аудит", "ad_id", ad.AdID, "err", err)
				}
			}(ad, detail)
		} else {
			// БОЙ: воронка L0–L5 решает, Gemini — только ступень L3.
			out := funnel.Run(ctx, fnl, cfg, gem, log, ad, detail)
			if err := store.SaveFunnelVerdict(ctx, ad.AdID, out.Audit, out.Status); err != nil {
				log.Error("воронка: аудит", "ad_id", ad.AdID, "err", err)
			}
			if out.AlertText != "" {
				if err := tg.SendRawQueued(ctx, out.AlertText, out.AlertURL); err != nil {
					log.Error("telegram: алерт воронки", "ad_id", ad.AdID, "err", err)
				} else {
					log.Info("воронка: алерт отправлен", "ad_id", ad.AdID, "code", out.Code)
				}
			} else {
				log.Info("воронка: тихий исход", "ad_id", ad.AdID, "code", out.Code)
			}
			continue
		}

		hint := cache.HintFor(ad.Name)
		select {
		case jobs <- evalJob{listing: l, detail: detail, hint: hint}:
		default:
			log.Warn("очередь оценки переполнена — лот пропущен", "ad_id", ad.AdID)
		}
	}
}

func exportOnce(ctx context.Context, store *storage.Store, exp *exporter.CSVExporter, log *slog.Logger) {
	rows, err := store.Unsynced(ctx, 500)
	if err != nil {
		log.Error("csv: выборка", "err", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	if err := exp.AppendRows(ctx, rows); err != nil {
		log.Error("csv: append", "err", err)
		return
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.AdID)
	}
	if err := store.MarkSynced(ctx, ids); err != nil {
		log.Error("csv: mark synced", "err", err)
		return
	}
	log.Info("csv: экспортировано строк", "rows", len(rows))
}
