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
	if cfg.DBBackupDir != "data/backups" {
		t.Fatalf("DBBackupDir = %q", cfg.DBBackupDir)
	}
}

func TestConfigRejectsInvalidEnvValues(t *testing.T) {
	cfg := loadTestConfig(map[string]string{
		"POLL_INTERVAL_SEC":  "abc",
		"GEMINI_CONCURRENCY": "fast",
		"REQUIRE_DGPU":       "maybe",
	})
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate must reject invalid env values")
	}
	msg := err.Error()
	for _, want := range []string{"POLL_INTERVAL_SEC", "GEMINI_CONCURRENCY", "REQUIRE_DGPU"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not mention %s", msg, want)
		}
	}
}

func TestConfigBoolParsing(t *testing.T) {
	cfg := loadTestConfig(map[string]string{
		"FUNNEL_TRACE": "off",
		"WEB_RESEARCH": "false",
		"REQUIRE_DGPU": "0",
	})
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid bool aliases rejected: %v", err)
	}
	if cfg.FunnelTrace || cfg.WebResearch || cfg.RequireDGPU {
		t.Fatalf("bool aliases not applied: trace=%v web=%v dgpu=%v", cfg.FunnelTrace, cfg.WebResearch, cfg.RequireDGPU)
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
		{"bad tol", func(c *Config) { c.MarketTolPct = 101 }, "MARKET_TOL_PCT"},
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
