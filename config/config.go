package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	PollInterval time.Duration
	DBPath       string
	FetchDelay   time.Duration

	GeminiAPIKey      string
	GeminiModel       string
	GeminiTextModel   string
	GeminiVisionModel string
	GeminiSearchModel string
	GeminiConcurrency int
	GeminiDailyLimit  int

	TelegramToken  string
	TelegramChatID string

	CSVPath        string
	ExportInterval time.Duration

	// Фаза 0 (PLAN_v4): надёжность и наблюдаемость.
	ChallengePause time.Duration // пауза при антибот-челлендже KP на поиске
	HeartbeatPath  string        // файл живости для watchdog
	LockPath       string
	DigestAt       string // время дневного дайджеста, «HH:MM»
	ResearchDBPath string // датасет рынка (read-only для дайджеста)

	// Фаза 4 (PLAN_v4): воронка L0–L5 в живом боте.
	FunnelTrace   bool          // FUNNEL_TRACE=0: выключить подробный журнал воронки
	MarketRefresh time.Duration // MARKET_REFRESH_MIN: пересборка рыночной модели (по умолчанию 6ч)
	DiamondDevPct int           // DIAMOND_DEV_PCT: порог «алмаза» в % от медианы (−15)
	SuspectDevPct int           // SUSPECT_DEV_PCT: глубже — подозрение на приманку (−40)
	DiamondMinN   int           // DIAMOND_MIN_N: минимальный размер группы (5)

	// Интернет используется ТОЛЬКО чтобы узнать, ЧТО за ноутбук (L3.4
	// модель→железо), а НЕ для цен. Цены — только наши данные KP.
	WebResearch  bool // WEB_RESEARCH=0: выключить интернет-определение железа (по умолчанию включён)
	ManualMinEUR int  // MANUAL_MIN_EUR: алерт «нужно посмотреть» только от этой цены (400)
	MooseMinEUR  int  // MOOSE_MIN_EUR: сводка редкого железа только от этой цены (400)

	// PLAN_v5: арбитраж ноутбуков с дискретной графикой.
	BannedModels []string // BANNED_MODELS: запрещённые линейки через запятую (macbook)
	RequireDGPU  bool     // REQUIRE_DGPU=0: снять обязательность дискретной видеокарты
	MarketTolPct int      // MARKET_TOL_PCT: «рыночная цена» = до +N% выше медианы (5)
}

// Load читает конфигурацию из переменных окружения (.env подхватывается автоматически).
func Load() *Config {
	_ = godotenv.Load() // .env не обязателен
	baseGeminiModel := envStr("GEMINI_MODEL", "gemini-2.5-flash-lite")
	liteGeminiModel := envStr("GEMINI_LITE_MODEL", "gemini-2.5-flash-lite")

	return &Config{
		PollInterval:      envDurationSec("POLL_INTERVAL_SEC", 45),
		DBPath:            envStr("DB_PATH", "data/kp_bot.db"),
		FetchDelay:        time.Duration(envInt("FETCH_DELAY_MS", 700)) * time.Millisecond,
		GeminiAPIKey:      os.Getenv("GEMINI_API_KEY"),
		GeminiModel:       baseGeminiModel,
		GeminiTextModel:   envStr("GEMINI_TEXT_MODEL", liteGeminiModel),
		GeminiVisionModel: envStr("GEMINI_VISION_MODEL", liteGeminiModel),
		GeminiSearchModel: envStr("GEMINI_SEARCH_MODEL", liteGeminiModel),
		GeminiConcurrency: envInt("GEMINI_CONCURRENCY", 5),
		GeminiDailyLimit:  envInt("GEMINI_DAILY_LIMIT", 80),
		TelegramToken:     os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:    os.Getenv("TELEGRAM_CHAT_ID"),
		CSVPath:           envStr("CSV_PATH", "data/market_history.csv"),
		ExportInterval:    envDurationMin("EXPORT_INTERVAL_MIN", 30),

		ChallengePause: envDurationMin("CHALLENGE_PAUSE_MIN", 30),
		HeartbeatPath:  envStr("HEARTBEAT_PATH", "data/kpbot.heartbeat"),
		LockPath:       envStr("LOCK_PATH", "data/kpbot.lock"),
		DigestAt:       envStr("DIGEST_AT", "09:00"),
		ResearchDBPath: envStr("RESEARCH_DB_PATH", "data/research.db"),

		FunnelTrace:   envStr("FUNNEL_TRACE", "1") == "1",
		MarketRefresh: envDurationMin("MARKET_REFRESH_MIN", 360),
		DiamondDevPct: envInt("DIAMOND_DEV_PCT", -15),
		SuspectDevPct: envInt("SUSPECT_DEV_PCT", -40),
		DiamondMinN:   envInt("DIAMOND_MIN_N", 5),

		WebResearch:  envStr("WEB_RESEARCH", "1") == "1",
		ManualMinEUR: envInt("MANUAL_MIN_EUR", 400),
		MooseMinEUR:  envInt("MOOSE_MIN_EUR", 400),

		BannedModels: envList("BANNED_MODELS", "macbook"),
		RequireDGPU:  envStr("REQUIRE_DGPU", "1") == "1",
		MarketTolPct: envInt("MARKET_TOL_PCT", 5),
	}
}

func (c *Config) Validate() error {
	checkDuration := func(name string, v time.Duration) error {
		if v <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
		return nil
	}
	for _, item := range []struct {
		name string
		v    time.Duration
	}{
		{"POLL_INTERVAL_SEC", c.PollInterval},
		{"FETCH_DELAY_MS", c.FetchDelay},
		{"EXPORT_INTERVAL_MIN", c.ExportInterval},
		{"CHALLENGE_PAUSE_MIN", c.ChallengePause},
		{"MARKET_REFRESH_MIN", c.MarketRefresh},
	} {
		if err := checkDuration(item.name, item.v); err != nil {
			return err
		}
	}
	if c.GeminiConcurrency <= 0 || c.GeminiConcurrency > 20 {
		return fmt.Errorf("GEMINI_CONCURRENCY must be in 1..20")
	}
	if c.GeminiDailyLimit < 0 {
		return fmt.Errorf("GEMINI_DAILY_LIMIT must be >= 0")
	}
	if c.DiamondMinN <= 0 {
		return fmt.Errorf("DIAMOND_MIN_N must be positive")
	}
	if c.SuspectDevPct >= c.DiamondDevPct {
		return fmt.Errorf("SUSPECT_DEV_PCT must be lower than DIAMOND_DEV_PCT")
	}
	if c.MarketTolPct < 0 || c.MarketTolPct > 100 {
		return fmt.Errorf("MARKET_TOL_PCT must be in 0..100")
	}
	if _, err := time.Parse("15:04", c.DigestAt); err != nil {
		return fmt.Errorf("DIGEST_AT must be HH:MM: %w", err)
	}
	if (c.TelegramToken == "") != (c.TelegramChatID == "") {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID must be set together")
	}
	if strings.TrimSpace(c.DBPath) == "" || strings.TrimSpace(c.ResearchDBPath) == "" {
		return fmt.Errorf("DB_PATH and RESEARCH_DB_PATH must be non-empty")
	}
	if strings.TrimSpace(c.LockPath) == "" {
		return fmt.Errorf("LOCK_PATH must be non-empty")
	}
	return nil
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
