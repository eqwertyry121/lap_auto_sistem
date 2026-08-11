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
	DBBackupDir  string
	FetchDelay   time.Duration

	GeminiAPIKey         string
	GeminiModel          string
	GeminiTextModel      string
	GeminiVisionModel    string
	GeminiSearchModel    string
	GeminiConcurrency    int
	GeminiDailyLimit     int
	GeminiDailyBudgetUSD float64

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

	parseErrors []string
}

// Load читает конфигурацию из переменных окружения (.env подхватывается автоматически).
func Load() *Config {
	_ = godotenv.Load() // .env не обязателен
	r := envReader{lookup: os.LookupEnv}
	return loadWithEnv(&r)
}

type envReader struct {
	lookup func(string) (string, bool)
	errors []string
}

func loadWithEnv(r *envReader) *Config {
	baseGeminiModel := r.str("GEMINI_MODEL", "gemini-2.5-flash-lite")
	liteGeminiModel := r.str("GEMINI_LITE_MODEL", "gemini-2.5-flash-lite")

	cfg := &Config{
		PollInterval:         r.durationSec("POLL_INTERVAL_SEC", 45),
		DBPath:               r.str("DB_PATH", "data/kp_bot.db"),
		DBBackupDir:          r.str("DB_BACKUP_DIR", "data/backups"),
		FetchDelay:           time.Duration(r.int("FETCH_DELAY_MS", 700)) * time.Millisecond,
		GeminiAPIKey:         r.raw("GEMINI_API_KEY"),
		GeminiModel:          baseGeminiModel,
		GeminiTextModel:      r.str("GEMINI_TEXT_MODEL", liteGeminiModel),
		GeminiVisionModel:    r.str("GEMINI_VISION_MODEL", liteGeminiModel),
		GeminiSearchModel:    r.str("GEMINI_SEARCH_MODEL", liteGeminiModel),
		GeminiConcurrency:    r.int("GEMINI_CONCURRENCY", 5),
		GeminiDailyLimit:     r.int("GEMINI_DAILY_LIMIT", 80),
		GeminiDailyBudgetUSD: r.float("GEMINI_DAILY_BUDGET_USD", 0),
		TelegramToken:        r.raw("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:       r.raw("TELEGRAM_CHAT_ID"),
		CSVPath:              r.str("CSV_PATH", "data/market_history.csv"),
		ExportInterval:       r.durationMin("EXPORT_INTERVAL_MIN", 30),

		ChallengePause: r.durationMin("CHALLENGE_PAUSE_MIN", 30),
		HeartbeatPath:  r.str("HEARTBEAT_PATH", "data/kpbot.heartbeat"),
		LockPath:       r.str("LOCK_PATH", "data/kpbot.lock"),
		DigestAt:       r.str("DIGEST_AT", "09:00"),
		ResearchDBPath: r.str("RESEARCH_DB_PATH", "data/research.db"),

		FunnelTrace:   r.bool("FUNNEL_TRACE", true),
		MarketRefresh: r.durationMin("MARKET_REFRESH_MIN", 360),
		DiamondDevPct: r.int("DIAMOND_DEV_PCT", -15),
		SuspectDevPct: r.int("SUSPECT_DEV_PCT", -40),
		DiamondMinN:   r.int("DIAMOND_MIN_N", 5),

		WebResearch:  r.bool("WEB_RESEARCH", true),
		ManualMinEUR: r.int("MANUAL_MIN_EUR", 400),
		MooseMinEUR:  r.int("MOOSE_MIN_EUR", 400),

		BannedModels: r.list("BANNED_MODELS", "macbook"),
		RequireDGPU:  r.bool("REQUIRE_DGPU", true),
		MarketTolPct: r.int("MARKET_TOL_PCT", 5),
	}
	cfg.parseErrors = append(cfg.parseErrors, r.errors...)
	return cfg
}

func (c *Config) Validate() error {
	if len(c.parseErrors) > 0 {
		return fmt.Errorf("invalid environment: %s", strings.Join(c.parseErrors, "; "))
	}
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
	if c.GeminiDailyBudgetUSD < 0 {
		return fmt.Errorf("GEMINI_DAILY_BUDGET_USD must be >= 0")
	}
	if c.DiamondMinN <= 0 {
		return fmt.Errorf("DIAMOND_MIN_N must be positive")
	}
	if c.DiamondDevPct >= 0 || c.DiamondDevPct < -100 {
		return fmt.Errorf("DIAMOND_DEV_PCT must be in -100..-1")
	}
	if c.SuspectDevPct < -100 {
		return fmt.Errorf("SUSPECT_DEV_PCT must be >= -100")
	}
	if c.SuspectDevPct >= c.DiamondDevPct {
		return fmt.Errorf("SUSPECT_DEV_PCT must be lower than DIAMOND_DEV_PCT")
	}
	if c.MarketTolPct < 0 || c.MarketTolPct > 100 {
		return fmt.Errorf("MARKET_TOL_PCT must be in 0..100")
	}
	if c.ManualMinEUR < 0 || c.MooseMinEUR < 0 {
		return fmt.Errorf("MANUAL_MIN_EUR and MOOSE_MIN_EUR must be >= 0")
	}
	if _, err := time.Parse("15:04", c.DigestAt); err != nil {
		return fmt.Errorf("DIGEST_AT must be HH:MM: %w", err)
	}
	if (c.TelegramToken == "") != (c.TelegramChatID == "") {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID must be set together")
	}
	if strings.TrimSpace(c.DBPath) == "" || strings.TrimSpace(c.DBBackupDir) == "" || strings.TrimSpace(c.ResearchDBPath) == "" {
		return fmt.Errorf("DB_PATH, DB_BACKUP_DIR and RESEARCH_DB_PATH must be non-empty")
	}
	if strings.TrimSpace(c.CSVPath) == "" || strings.TrimSpace(c.HeartbeatPath) == "" || strings.TrimSpace(c.LockPath) == "" {
		return fmt.Errorf("CSV_PATH, HEARTBEAT_PATH and LOCK_PATH must be non-empty")
	}
	for _, item := range []struct {
		name  string
		value string
	}{
		{"GEMINI_MODEL", c.GeminiModel},
		{"GEMINI_TEXT_MODEL", c.GeminiTextModel},
		{"GEMINI_VISION_MODEL", c.GeminiVisionModel},
		{"GEMINI_SEARCH_MODEL", c.GeminiSearchModel},
	} {
		if strings.TrimSpace(item.value) == "" {
			return fmt.Errorf("%s must be non-empty", item.name)
		}
	}
	return nil
}

func (r *envReader) raw(key string) string {
	if r == nil || r.lookup == nil {
		return ""
	}
	v, _ := r.lookup(key)
	return strings.TrimSpace(v)
}

func (r *envReader) str(key, def string) string {
	if v := r.raw(key); v != "" {
		return v
	}
	return def
}

// envList — список значений через запятую (пустые элементы отбрасываются).
func (r *envReader) list(key, def string) []string {
	v := r.str(key, def)
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func (r *envReader) int(key string, def int) int {
	if v := r.raw(key); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			r.errors = append(r.errors, fmt.Sprintf("%s must be integer, got %q", key, v))
			return def
		}
		return n
	}
	return def
}

func (r *envReader) float(key string, def float64) float64 {
	v := r.raw(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		r.errors = append(r.errors, fmt.Sprintf("%s=%q is not a float", key, v))
		return def
	}
	return n
}

func (r *envReader) bool(key string, def bool) bool {
	v := strings.ToLower(r.raw(key))
	if v == "" {
		return def
	}
	switch v {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		r.errors = append(r.errors, fmt.Sprintf("%s must be boolean (1/0/true/false), got %q", key, v))
		return def
	}
}

func (r *envReader) durationSec(key string, defSec int) time.Duration {
	return time.Duration(r.int(key, defSec)) * time.Second
}

func (r *envReader) durationMin(key string, defMin int) time.Duration {
	return time.Duration(r.int(key, defMin)) * time.Minute
}
