package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
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

func acquireSingleton(path string) (func(), error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(f, "%d\n%s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return func() { _ = os.Remove(path) }, nil
}

func main() {
	cfg := config.Load()
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)
	if err := cfg.Validate(); err != nil {
		log.Error("invalid config", "err", err)
		os.Exit(2)
	}
	releaseLock, err := acquireSingleton(cfg.LockPath)
	if err != nil {
		log.Error("another kpbot instance is already running or lock is stale", "lock", cfg.LockPath, "err", err)
		os.Exit(1)
	}
	defer releaseLock()

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

	gemini := vision.NewGeminiClient(cfg.GeminiAPIKey, cfg.GeminiModel).SetLimits(cfg.GeminiConcurrency, cfg.GeminiDailyLimit)
	tg := notifier.New(cfg.TelegramToken, cfg.TelegramChatID)
	kp := collector.NewClient()
	go telegramOutboxLoop(ctx, store, tg, log)

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
		"diamond_dev_pct", cfg.DiamondDevPct)

	if tg.Enabled() {
		msg := fmt.Sprintf("🤖 <b>Бот запущен</b>\nПоллинг: %s · дайджест: ежедневно %s · heartbeat: %s",
			cfg.PollInterval, cfg.DigestAt, cfg.HeartbeatPath)
		if err := tg.SendRaw(ctx, msg); err != nil {
			log.Error("telegram: сообщение о старте", "err", err)
		}
	}

	// Первый цикл сразу, дальше по таймеру.
	pollOnce(ctx, kp, store, cfg, log, st, fnl, gemini, tg)

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("остановка основного цикла")
			if tg.Enabled() {
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
				_ = tg.SendRaw(stopCtx, "🛑 <b>Бот остановлен</b> (graceful shutdown)")
				stopCancel()
			}
			log.Info("бот остановлен")
			return
		case <-ticker.C:
			pollOnce(ctx, kp, store, cfg, log, st, fnl, gemini, tg)
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

func pollOnce(ctx context.Context, kp *collector.Client, store *storage.Store, cfg *config.Config, log *slog.Logger, st *botState, fnl *funnel.Funnel, gem *vision.GeminiClient, tg *notifier.Telegram) {
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
		{
			l := models.Listing{
				AdID:         ad.AdID,
				Title:        ad.Name,
				Price:        float64(ad.Price),
				Currency:     models.NormalizeCurrency(ad.Currency),
				URL:          ad.URL(),
				Status:       models.Status(models.ProcessDetailPending),
				ProcessState: models.ProcessDetailPending,
				CreatedAt:    time.Now(),
			}
			if err := store.UpsertDiscovered(ctx, l); err != nil {
				log.Error("durable queue: discover", "ad_id", ad.AdID, "err", err)
			}
		}
	}
	processDueListings(ctx, kp, store, cfg, log, st, fnl, gem, tg)
}

func processDueListings(ctx context.Context, kp *collector.Client, store *storage.Store, cfg *config.Config, log *slog.Logger, st *botState, fnl *funnel.Funnel, gem *vision.GeminiClient, tg *notifier.Telegram) {
	for {
		batch, err := store.ClaimDueListings(ctx, []models.ProcessState{
			models.ProcessDetailPending,
			models.ProcessEvaluating,
		}, 16, 15*time.Minute)
		if err != nil {
			log.Error("durable queue: claim", "err", err)
			return
		}
		if len(batch) == 0 {
			return
		}
		for _, l := range batch {
			if ctx.Err() != nil {
				return
			}
			if banned, hit := filters.IsBannedModel(l.Title, cfg.BannedModels); banned {
				log.Info("ban-list title", "ad_id", l.AdID, "marker", hit)
				_ = store.CompleteProcess(ctx, l.AdID, models.StatusSkippedBan)
				continue
			}
			if spam, reason := collector.IsSpam(l.Title, ""); spam {
				log.Info("spam title", "ad_id", l.AdID, "reason", reason)
				_ = store.CompleteProcess(ctx, l.AdID, models.StatusSkippedSpam)
				continue
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(cfg.FetchDelay):
			}

			detail, err := kp.FetchDetail(ctx, l.AdID)
			if err != nil {
				switch {
				case errors.Is(err, collector.ErrRateLimited):
					_ = store.RetryProcess(ctx, l.AdID, models.ProcessDetailPending, err.Error())
					log.Warn("429 on details: retry scheduled", "ad_id", l.AdID)
					return
				case errors.Is(err, collector.ErrChallenge):
					_ = store.RetryProcess(ctx, l.AdID, models.ProcessDetailPending, err.Error())
					st.enterChallengePause(cfg)
					log.Warn("KP challenge on details: retry scheduled and pause entered", "ad_id", l.AdID, "pause", cfg.ChallengePause.String())
					return
				case errors.Is(err, collector.ErrNotFound):
					_ = store.MarkDead(ctx, l.AdID, err.Error())
					continue
				default:
					if ctx.Err() == nil {
						log.Error("details", "ad_id", l.AdID, "err", err)
					}
					_ = store.RetryProcess(ctx, l.AdID, models.ProcessDetailPending, err.Error())
					continue
				}
			}
			if detail.Description == "" && detail.Seller() == "" {
				reason := "empty /eds response"
				log.Warn(reason, "ad_id", l.AdID)
				_ = store.RetryProcess(ctx, l.AdID, models.ProcessDetailPending, reason)
				continue
			}

			price := l.Price
			cur := models.NormalizeCurrency(l.Currency)
			if detail.Price > 0 {
				price = float64(detail.Price)
				cur = models.NormalizeCurrency(detail.Currency)
			}
			if err := store.UpdateDetails(ctx, l.AdID, detail.Description, detail.Seller(), price, cur); err != nil {
				log.Error("details update", "ad_id", l.AdID, "err", err)
				_ = store.RetryProcess(ctx, l.AdID, models.ProcessDetailPending, err.Error())
				continue
			}
			if spam, reason := collector.IsSpam(l.Title, detail.Seller()); spam {
				log.Info("spam seller", "ad_id", l.AdID, "reason", reason)
				_ = store.CompleteProcess(ctx, l.AdID, models.StatusSkippedSpam)
				continue
			}

			adURL := detail.AdURL
			if adURL == "" {
				adURL = l.URL
			}
			ad := models.SearchAd{
				AdID: l.AdID, Name: l.Title, Price: models.FlexFloat(price), Currency: cur,
				AdURL: adURL, Condition: detail.Condition, KPIzlog: detail.KPIzlog,
			}
			_ = store.MarkProcessState(ctx, l.AdID, models.ProcessEvaluating)
			out := funnel.Run(ctx, fnl, cfg, gem, log, ad, detail)
			if out.AlertText != "" {
				if err := store.SaveFunnelAlertPending(ctx, l.AdID, out.Audit, out.AlertText, out.AlertURL, out.Status); err != nil {
					log.Error("funnel alert outbox", "ad_id", l.AdID, "err", err)
					_ = store.RetryProcess(ctx, l.AdID, models.ProcessEvaluating, err.Error())
					continue
				}
				drainTelegramOutbox(ctx, store, tg, log, 8)
			} else if err := store.SaveFunnelVerdict(ctx, l.AdID, out.Audit, out.Status); err != nil {
				log.Error("funnel verdict", "ad_id", l.AdID, "err", err)
				_ = store.RetryProcess(ctx, l.AdID, models.ProcessEvaluating, err.Error())
			} else {
				log.Info("funnel: quiet outcome", "ad_id", l.AdID, "code", out.Code)
			}
		}
	}
}

func drainTelegramOutbox(ctx context.Context, store *storage.Store, tg *notifier.Telegram, log *slog.Logger, limit int) {
	items, err := store.ClaimTelegramOutbox(ctx, limit, 2*time.Minute)
	if err != nil {
		log.Error("telegram outbox: claim", "err", err)
		return
	}
	for _, it := range items {
		if ctx.Err() != nil {
			return
		}
		messageID, err := tg.SendRawDirect(ctx, it.Text, it.URL)
		if err != nil {
			log.Error("telegram outbox: send", "id", it.ID, "ad_id", it.AdID, "err", err)
			_ = store.RetryTelegramOutbox(ctx, it.ID, err.Error())
			continue
		}
		if err := store.MarkTelegramOutboxSent(ctx, it, messageID); err != nil {
			log.Error("telegram outbox: mark sent", "id", it.ID, "ad_id", it.AdID, "err", err)
			_ = store.RetryTelegramOutbox(ctx, it.ID, err.Error())
			continue
		}
		log.Info("telegram outbox: sent", "id", it.ID, "ad_id", it.AdID, "status", it.FinalStatus)
	}
}

func telegramOutboxLoop(ctx context.Context, store *storage.Store, tg *notifier.Telegram, log *slog.Logger) {
	drainTelegramOutbox(ctx, store, tg, log, 20)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			drainTelegramOutbox(ctx, store, tg, log, 20)
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
