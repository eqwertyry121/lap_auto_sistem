package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
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
	"kpbot/runlock"
	"kpbot/storage"
	"kpbot/vision"
)

// botState — живое состояние бота между циклами опроса (PLAN_v4, Фаза 0):
// heartbeat для watchdog, пауза при антибот-челлендже, счётчик челленджей,
// ручной стоп/старт из пульта Telegram.
type botState struct {
	beat               *hb.Heartbeat
	pausedUntil        time.Time
	challengeCount     atomic.Int64
	startedAt          time.Time
	lastSearchOK       atomic.Int64
	lastDetailOK       atomic.Int64
	lastGeminiOK       atomic.Int64
	lastTelegramOK     atomic.Int64
	lastBackupOK       atomic.Int64
	geminiCalls        atomic.Int64
	geminiLimit        atomic.Int64
	geminiBudgetNanos  atomic.Int64
	geminiPromptTokens atomic.Int64
	geminiOutputTokens atomic.Int64
	geminiTotalTokens  atomic.Int64
	geminiCostNanos    atomic.Int64
	geminiCircuit      atomic.Int64
	schemaVersion      atomic.Int64
	buildVersion       string
	manualPaused       atomic.Bool // пульт: ⏹ Стоп
}

// enterChallengePause — реакция на антибот-челлендж KP: длинная пауза вместо
// бессмысленного долбления (челлендж «липкий», снимается за 30–60 минут).
func (s *botState) enterChallengePause(cfg *config.Config) {
	s.challengeCount.Add(1)
	s.pausedUntil = time.Now().Add(cfg.ChallengePause)
	s.beat.SetState("challenge_pause")
}

func (s *botState) markLastSearchOK(t time.Time)   { storeUnixTime(&s.lastSearchOK, t) }
func (s *botState) markLastDetailOK(t time.Time)   { storeUnixTime(&s.lastDetailOK, t) }
func (s *botState) markLastGeminiOK(t time.Time)   { storeUnixTime(&s.lastGeminiOK, t) }
func (s *botState) markLastTelegramOK(t time.Time) { storeUnixTime(&s.lastTelegramOK, t) }
func (s *botState) markLastBackupOK(t time.Time)   { storeUnixTime(&s.lastBackupOK, t) }

func (s *botState) LastSearchOK() time.Time        { return loadUnixTime(&s.lastSearchOK) }
func (s *botState) LastDetailOK() time.Time        { return loadUnixTime(&s.lastDetailOK) }
func (s *botState) LastGeminiOK() time.Time        { return loadUnixTime(&s.lastGeminiOK) }
func (s *botState) LastTelegramOK() time.Time      { return loadUnixTime(&s.lastTelegramOK) }
func (s *botState) LastBackupOK() time.Time        { return loadUnixTime(&s.lastBackupOK) }
func (s *botState) GeminiCallsToday() int          { return int(s.geminiCalls.Load()) }
func (s *botState) GeminiDailyLimit() int          { return int(s.geminiLimit.Load()) }
func (s *botState) GeminiPromptTokensToday() int64 { return s.geminiPromptTokens.Load() }
func (s *botState) GeminiOutputTokensToday() int64 { return s.geminiOutputTokens.Load() }
func (s *botState) GeminiTotalTokensToday() int64  { return s.geminiTotalTokens.Load() }
func (s *botState) GeminiEstimatedCostUSD() float64 {
	return float64(s.geminiCostNanos.Load()) / 1e9
}
func (s *botState) GeminiDailyBudgetUSD() float64 {
	return float64(s.geminiBudgetNanos.Load()) / 1e9
}
func (s *botState) GeminiCircuitUntil() time.Time {
	return loadUnixTime(&s.geminiCircuit)
}
func (s *botState) SchemaVersion() int { return int(s.schemaVersion.Load()) }
func (s *botState) BuildVersion() string {
	if s == nil || s.buildVersion == "" {
		return "dev"
	}
	return s.buildVersion
}

func (s *botState) syncGeminiStats(stats vision.GeminiStats) {
	if s == nil {
		return
	}
	if !stats.LastSuccess.IsZero() {
		s.markLastGeminiOK(stats.LastSuccess)
	}
	s.geminiCalls.Store(int64(stats.CallsToday))
	s.geminiLimit.Store(int64(stats.DailyLimit))
	s.geminiPromptTokens.Store(stats.PromptTokensToday)
	s.geminiOutputTokens.Store(stats.OutputTokensToday)
	s.geminiTotalTokens.Store(stats.TotalTokensToday)
	s.geminiCostNanos.Store(int64(stats.EstimatedCostUSD * 1e9))
	s.geminiBudgetNanos.Store(int64(stats.DailyBudgetUSD * 1e9))
	if stats.CircuitUntil.IsZero() {
		s.geminiCircuit.Store(0)
	} else {
		storeUnixTime(&s.geminiCircuit, stats.CircuitUntil)
	}
}

func (s *botState) healthSnapshot(now time.Time) runtimeHealth {
	return runtimeHealth{
		Now:                     now,
		LastSearchOK:            s.LastSearchOK(),
		LastDetailOK:            s.LastDetailOK(),
		LastGeminiOK:            s.LastGeminiOK(),
		LastTelegramOK:          s.LastTelegramOK(),
		LastBackupOK:            s.LastBackupOK(),
		GeminiCallsToday:        s.GeminiCallsToday(),
		GeminiDailyLimit:        s.GeminiDailyLimit(),
		GeminiPromptTokensToday: s.GeminiPromptTokensToday(),
		GeminiOutputTokensToday: s.GeminiOutputTokensToday(),
		GeminiTotalTokensToday:  s.GeminiTotalTokensToday(),
		GeminiEstimatedCostUSD:  s.GeminiEstimatedCostUSD(),
		GeminiDailyBudgetUSD:    s.GeminiDailyBudgetUSD(),
		GeminiCircuitUntil:      s.GeminiCircuitUntil(),
		SchemaVersion:           s.SchemaVersion(),
		BuildVersion:            s.BuildVersion(),
	}
}

func storeUnixTime(dst *atomic.Int64, t time.Time) {
	if dst != nil && !t.IsZero() {
		dst.Store(t.Unix())
	}
}

func loadUnixTime(src *atomic.Int64) time.Time {
	if src == nil {
		return time.Time{}
	}
	unix := src.Load()
	if unix <= 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}

func main() {
	cfg := config.Load()
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)
	if err := cfg.Validate(); err != nil {
		log.Error("invalid config", "err", err)
		os.Exit(2)
	}
	releaseLock, err := runlock.Acquire(cfg.LockPath, runlock.Options{HeartbeatPath: cfg.HeartbeatPath})
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
		beat:         hb.New(cfg.HeartbeatPath, "starting"),
		startedAt:    time.Now(),
		buildVersion: buildVersion(),
	}
	if schemaVersion, err := store.SchemaVersion(ctx); err == nil {
		st.schemaVersion.Store(int64(schemaVersion))
	} else {
		log.Warn("schema version unavailable", "err", err)
	}
	go st.beat.Run(ctx, time.Minute)

	gemini := vision.NewGeminiClient(cfg.GeminiAPIKey, cfg.GeminiTextModel).
		SetLimits(cfg.GeminiConcurrency, cfg.GeminiDailyLimit).
		SetDailyBudgetUSD(cfg.GeminiDailyBudgetUSD)
	st.syncGeminiStats(gemini.Stats())
	tg := notifier.New(cfg.TelegramToken, cfg.TelegramChatID)
	kp := collector.NewClient(collector.WithSharedCooldown(
		cfg.KPCooldownPath, cfg.KPRateCooldown, cfg.KPChallengeCooldown,
	))
	go telegramOutboxLoop(ctx, store, tg, log, st)
	go dbBackupLoop(ctx, cfg, store, log, st)

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
			&st.manualPaused, st.startedAt, &st.challengeCount, fnl, st)
		if panel != nil {
			go panel.Run(ctx, log)
		}
	}

	log.Info("бот запущен",
		"poll_interval", cfg.PollInterval.String(),
		"live_search_pages", cfg.LiveSearchPages,
		"kp_cooldown_path", cfg.KPCooldownPath,
		"kp_rate_cooldown", cfg.KPRateCooldown.String(),
		"kp_challenge_cooldown", cfg.KPChallengeCooldown.String(),
		"gemini_concurrency", cfg.GeminiConcurrency,
		"gemini_daily_limit", cfg.GeminiDailyLimit,
		"gemini_daily_budget_usd", cfg.GeminiDailyBudgetUSD,
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

func dbBackupLoop(ctx context.Context, cfg *config.Config, store *storage.Store, log *slog.Logger, st *botState) {
	runDBBackup(ctx, cfg, store, log, st)
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			runDBBackup(ctx, cfg, store, log, st)
		}
	}
}

func runDBBackup(ctx context.Context, cfg *config.Config, store *storage.Store, log *slog.Logger, st *botState) {
	res, err := store.BackupDaily(ctx, cfg.DBBackupDir, time.Now())
	if err != nil {
		log.Error("sqlite backup failed", "dir", cfg.DBBackupDir, "err", err)
		return
	}
	if st != nil {
		st.markLastBackupOK(time.Now())
	}
	if res.Created {
		log.Info("sqlite backup created", "path", res.Path)
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
	processStates, err := store.CountByProcessState(ctx)
	if err != nil {
		log.Warn("дайджест: process states", "err", err)
		processStates = nil
	}
	pendingOutbox, err := store.PendingTelegramOutbox(ctx)
	if err != nil {
		log.Warn("дайджест: telegram outbox", "err", err)
	}
	now := time.Now()
	health := st.healthSnapshot(now)
	if lastTelegram, err := store.LastTelegramDelivery(ctx); err == nil && lastTelegram.After(health.LastTelegramOK) {
		health.LastTelegramOK = lastTelegram
	} else if err != nil {
		log.Warn("digest: last telegram delivery", "err", err)
	}
	text := digestText(time.Since(st.startedAt), byStatus, st.challengeCount.Load(),
		total, detailed, alerts, processStates, pendingOutbox, health)
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

	if !discoverFreshListings(ctx, kp, store, cfg, log, st) {
		return
	}
	processDueListings(ctx, kp, store, cfg, log, st, fnl, gem, tg)
}

func discoverFreshListings(ctx context.Context, kp *collector.Client, store *storage.Store, cfg *config.Config, log *slog.Logger, st *botState) bool {
	for page := 1; page <= cfg.LiveSearchPages; page++ {
		if ctx.Err() != nil {
			return false
		}
		res, err := kp.SearchPageFull(ctx, page)
		if err != nil {
			handleSearchError(err, cfg, log, st)
			return false
		}
		if res == nil {
			log.Error("поиск: пустой результат", "page", page)
			return false
		}
		st.markLastSearchOK(time.Now())
		for _, ad := range res.Ads {
			l := models.Listing{
				AdID:         ad.AdID,
				Title:        ad.Name,
				Price:        float64(ad.Price),
				Currency:     models.NormalizeCurrency(ad.Currency),
				URL:          ad.URL(),
				Status:       models.StatusNew,
				ProcessState: models.ProcessDetailPending,
				CreatedAt:    time.Now(),
			}
			if err := store.UpsertDiscovered(ctx, l); err != nil {
				log.Error("durable queue: discover", "ad_id", ad.AdID, "err", err)
			}
		}
		log.Info("search discovery page", "page", page, "ads", len(res.Ads), "total_pages", res.Pages, "limit", cfg.LiveSearchPages)
		if !shouldFetchNextLiveSearchPage(res, page, cfg.LiveSearchPages) {
			break
		}
	}
	return true
}

func handleSearchError(err error, cfg *config.Config, log *slog.Logger, st *botState) {
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
		log.Error("поиск", "err", err)
	}
}

func shouldFetchNextLiveSearchPage(res *models.SearchResults, page, maxPages int) bool {
	if res == nil || maxPages <= 0 || page >= maxPages {
		return false
	}
	if len(res.Ads) == 0 || res.HasReachedMax || res.HasReachedLimit {
		return false
	}
	if res.Pages > 0 && page >= res.Pages {
		return false
	}
	return true
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
			st.markLastDetailOK(time.Now())

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
			st.syncGeminiStats(gem.Stats())
			if out.AlertText != "" {
				if err := store.SaveFunnelAlertPending(ctx, l.AdID, out.Audit, out.AlertText, out.AlertURL, out.Status); err != nil {
					log.Error("funnel alert outbox", "ad_id", l.AdID, "err", err)
					_ = store.RetryProcess(ctx, l.AdID, models.ProcessEvaluating, err.Error())
					continue
				}
				drainTelegramOutbox(ctx, store, tg, log, st, 8)
			} else if err := store.SaveFunnelVerdict(ctx, l.AdID, out.Audit, out.Status); err != nil {
				log.Error("funnel verdict", "ad_id", l.AdID, "err", err)
				_ = store.RetryProcess(ctx, l.AdID, models.ProcessEvaluating, err.Error())
			} else {
				log.Info("funnel: quiet outcome", "ad_id", l.AdID, "code", out.Code)
			}
		}
	}
}

func drainTelegramOutbox(ctx context.Context, store *storage.Store, tg *notifier.Telegram, log *slog.Logger, st *botState, limit int) {
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
		if st != nil {
			st.markLastTelegramOK(time.Now())
		}
		log.Info("telegram outbox: sent", "id", it.ID, "ad_id", it.AdID, "status", it.FinalStatus)
	}
}

func telegramOutboxLoop(ctx context.Context, store *storage.Store, tg *notifier.Telegram, log *slog.Logger, st *botState) {
	drainTelegramOutbox(ctx, store, tg, log, st, 20)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			drainTelegramOutbox(ctx, store, tg, log, st, 20)
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
