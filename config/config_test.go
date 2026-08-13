package config

import (
	"strings"
	"testing"
	"time"
)

func loadTestConfig(values map[string]string) *Config {
	r := &envReader{
		lookup: func(key string) (string, bool) {
			v, ok := values[key]
			return v, ok
		},
	}
	return loadWithEnv(r)
}

func TestDefaultConfigValidates(t *testing.T) {
	cfg := loadTestConfig(nil)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config must validate: %v", err)
	}
	if cfg.GeminiModel != "gemini-2.5-flash-lite" {
		t.Fatalf("GeminiModel = %q", cfg.GeminiModel)
	}
	if cfg.PollInterval != 45*time.Second {
		t.Fatalf("PollInterval = %s", cfg.PollInterval)
	}
	if cfg.LiveSearchPages != 2 {
		t.Fatalf("LiveSearchPages = %d", cfg.LiveSearchPages)
	}
	if cfg.DBBackupDir != "data/backups" {
		t.Fatalf("DBBackupDir = %q", cfg.DBBackupDir)
	}
	if cfg.KPCooldownPath != "data/kp_cooldown" {
		t.Fatalf("KPCooldownPath = %q", cfg.KPCooldownPath)
	}
	if cfg.KPRateCooldown != 90*time.Second {
		t.Fatalf("KPRateCooldown = %s", cfg.KPRateCooldown)
	}
	if cfg.KPChallengeCooldown != 30*time.Minute {
		t.Fatalf("KPChallengeCooldown = %s", cfg.KPChallengeCooldown)
	}
	if cfg.KPWatchdogResearch {
		t.Fatal("KPWatchdogResearch default must be false")
	}
	if cfg.GeminiDailyBudgetUSD != 0 {
		t.Fatalf("GeminiDailyBudgetUSD = %.6f", cfg.GeminiDailyBudgetUSD)
	}
}

func TestConfigRejectsInvalidEnvValues(t *testing.T) {
	cfg := loadTestConfig(map[string]string{
		"POLL_INTERVAL_SEC":       "abc",
		"LIVE_SEARCH_PAGES":       "many",
		"GEMINI_CONCURRENCY":      "fast",
		"GEMINI_DAILY_BUDGET_USD": "money",
		"REQUIRE_DGPU":            "maybe",
		"KP_WATCHDOG_RESEARCH":    "sometimes",
	})
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate must reject invalid env values")
	}
	msg := err.Error()
	for _, want := range []string{"POLL_INTERVAL_SEC", "LIVE_SEARCH_PAGES", "GEMINI_CONCURRENCY", "GEMINI_DAILY_BUDGET_USD", "REQUIRE_DGPU", "KP_WATCHDOG_RESEARCH"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not mention %s", msg, want)
		}
	}
}

func TestConfigBoolParsing(t *testing.T) {
	cfg := loadTestConfig(map[string]string{
		"FUNNEL_TRACE":         "off",
		"WEB_RESEARCH":         "false",
		"REQUIRE_DGPU":         "0",
		"KP_WATCHDOG_RESEARCH": "yes",
	})
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid bool aliases rejected: %v", err)
	}
	if cfg.FunnelTrace || cfg.WebResearch || cfg.RequireDGPU {
		t.Fatalf("bool aliases not applied: trace=%v web=%v dgpu=%v", cfg.FunnelTrace, cfg.WebResearch, cfg.RequireDGPU)
	}
	if !cfg.KPWatchdogResearch {
		t.Fatal("KP_WATCHDOG_RESEARCH=yes must enable research watchdog flag")
	}
}

func TestConfigRejectsBadThresholds(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"diamond positive", func(c *Config) { c.DiamondDevPct = 1 }, "DIAMOND_DEV_PCT"},
		{"suspect above diamond", func(c *Config) { c.SuspectDevPct = c.DiamondDevPct }, "SUSPECT_DEV_PCT"},
		{"negative manual", func(c *Config) { c.ManualMinEUR = -1 }, "MANUAL_MIN_EUR"},
		{"negative gemini budget", func(c *Config) { c.GeminiDailyBudgetUSD = -0.01 }, "GEMINI_DAILY_BUDGET_USD"},
		{"bad tol", func(c *Config) { c.MarketTolPct = 101 }, "MARKET_TOL_PCT"},
		{"zero live search pages", func(c *Config) { c.LiveSearchPages = 0 }, "LIVE_SEARCH_PAGES"},
		{"too many live search pages", func(c *Config) { c.LiveSearchPages = 11 }, "LIVE_SEARCH_PAGES"},
		{"empty kp cooldown path", func(c *Config) { c.KPCooldownPath = "" }, "KP_COOLDOWN_PATH"},
		{"zero rate cooldown", func(c *Config) { c.KPRateCooldown = 0 }, "KP_RATE_COOLDOWN_SEC"},
		{"zero challenge cooldown", func(c *Config) { c.KPChallengeCooldown = 0 }, "KP_CHALLENGE_COOLDOWN_MIN"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := loadTestConfig(nil)
			c.mut(cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Validate error = %v, want mention %s", err, c.want)
			}
		})
	}
}
