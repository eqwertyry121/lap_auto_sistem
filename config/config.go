package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	PollInterval time.Duration
	DBPath       string
	MaxPhotos    int
	FetchDelay   time.Duration

	GeminiAPIKey      string
	GeminiModel       string
	GeminiConcurrency int

	TelegramToken  string
	TelegramChatID string

	CSVPath        string
	ExportInterval time.Duration

	PriceCacheRefresh time.Duration
	PriceCacheDays    int

	// Фаза 0 (PLAN_v4): надёжность и наблюдаемость.
	ChallengePause time.Duration // пауза при антибот-челлендже KP на поиске
	HeartbeatPath  string        // файл живости для watchdog
	DigestAt       string        // время дневного дайджеста, «HH:MM»
	ResearchDBPath string        // датасет рынка (read-only для дайджеста)

	// Фаза 6 (PLAN_v4): автономность.
	AlertQueuePath string // JSONL-очередь алертов на сбой Telegram

	// Фаза 4 (PLAN_v4): воронка L0–L5 в живом боте.
	FunnelShadow  bool          // FUNNEL_SHADOW=1: воронка пишет аудит, алерты — старый путь
	FunnelTrace   bool          // FUNNEL_TRACE=0: выключить подробный журнал воронки
	MarketRefresh time.Duration // MARKET_REFRESH_MIN: пересборка рыночной модели (по умолчанию 6ч)
	DiamondDevPct int           // DIAMOND_DEV_PCT: порог «алмаза» в % от медианы (−15)
	SuspectDevPct int           // SUSPECT_DEV_PCT: глубже — подозрение на приманку (−40)
	DiamondMinN   int           // DIAMOND_MIN_N: минимальный размер группы (5)

	// Интернет используется ТОЛЬКО чтобы узнать, ЧТО за ноутбук (L3.4
	// модель→железо), а НЕ для цен. Цены — только наши данные KP.
	WebResearch  bool // WEB_RESEARCH=0: выключить интернет-определение железа (по умолчанию включён)
	ManualMinEUR int  // MANUAL_MIN_EUR: алерт «нужно посмотреть» только от этой цены (400)

	// PLAN_v5: арбитраж ноутбуков с дискретной графикой.
	BannedModels []string // BANNED_MODELS: запрещённые линейки через запятую (macbook)
	RequireDGPU  bool     // REQUIRE_DGPU=0: снять обязательность дискретной видеокарты
	MarketTolPct int      // MARKET_TOL_PCT: «рыночная цена» = до +N% выше медианы (5)
}

// Load читает конфигурацию из переменных окружения (.env подхватывается автоматически).
func Load() *Config {
	_ = godotenv.Load() // .env не обязателен

	return &Config{
		PollInterval:      envDurationSec("POLL_INTERVAL_SEC", 45),
		DBPath:            envStr("DB_PATH", "data/kp_bot.db"),
		MaxPhotos:         envInt("MAX_PHOTOS_PER_AD", 5),
		FetchDelay:        time.Duration(envInt("FETCH_DELAY_MS", 700)) * time.Millisecond,
		GeminiAPIKey:      os.Getenv("GEMINI_API_KEY"),
		GeminiModel:       envStr("GEMINI_MODEL", "gemini-flash-latest"),
		GeminiConcurrency: envInt("GEMINI_CONCURRENCY", 5),
		TelegramToken:     os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:    os.Getenv("TELEGRAM_CHAT_ID"),
		CSVPath:           envStr("CSV_PATH", "data/market_history.csv"),
		ExportInterval:    envDurationMin("EXPORT_INTERVAL_MIN", 30),
		PriceCacheRefresh: envDurationMin("PRICE_CACHE_REFRESH_MIN", 30),
		PriceCacheDays:    envInt("PRICE_CACHE_DAYS", 90),

		ChallengePause: envDurationMin("CHALLENGE_PAUSE_MIN", 30),
		HeartbeatPath:  envStr("HEARTBEAT_PATH", "data/kpbot.heartbeat"),
		DigestAt:       envStr("DIGEST_AT", "09:00"),
		ResearchDBPath: envStr("RESEARCH_DB_PATH", "data/research.db"),

		AlertQueuePath: envStr("ALERT_QUEUE_PATH", "data/alert_queue.jsonl"),

		FunnelShadow:  envStr("FUNNEL_SHADOW", "") == "1",
		FunnelTrace:   envStr("FUNNEL_TRACE", "1") == "1",
		MarketRefresh: envDurationMin("MARKET_REFRESH_MIN", 360),
		DiamondDevPct: envInt("DIAMOND_DEV_PCT", -15),
		SuspectDevPct: envInt("SUSPECT_DEV_PCT", -40),
		DiamondMinN:   envInt("DIAMOND_MIN_N", 5),

		WebResearch:  envStr("WEB_RESEARCH", "1") == "1",
		ManualMinEUR: envInt("MANUAL_MIN_EUR", 400),

		BannedModels: envList("BANNED_MODELS", "macbook"),
		RequireDGPU:  envStr("REQUIRE_DGPU", "1") == "1",
		MarketTolPct: envInt("MARKET_TOL_PCT", 5),
	}
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envList — список значений через запятую (пустые элементы отбрасываются).
func envList(key, def string) []string {
	v := envStr(key, def)
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDurationSec(key string, defSec int) time.Duration {
	return time.Duration(envInt(key, defSec)) * time.Second
}

func envDurationMin(key string, defMin int) time.Duration {
	return time.Duration(envInt(key, defMin)) * time.Minute
}
