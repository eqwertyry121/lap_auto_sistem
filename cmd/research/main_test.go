package main

import (
	"strings"
	"testing"
	"time"

	"kpbot/hw"
	"kpbot/models"
)

func TestJitteredWithinBounds(t *testing.T) {
	base := 6000 * time.Millisecond
	jitter := 40
	lo := float64(base) * 0.6 // 1 - 0.40
	hi := float64(base) * 1.4 // 1 + 0.40
	for i := 0; i < 1000; i++ {
		got := float64(jittered(base, jitter))
		if got < lo || got > hi {
			t.Fatalf("jittered вылез за границы: %.0f вне [%.0f, %.0f]", got, lo, hi)
		}
	}
}

func TestJitteredVaries(t *testing.T) {
	base := 6000 * time.Millisecond
	seen := map[time.Duration]bool{}
	for i := 0; i < 50; i++ {
		seen[jittered(base, 40)] = true
	}
	if len(seen) < 10 {
		t.Errorf("джиттер почти не варьирует: %d уникальных значений из 50", len(seen))
	}
}

func TestJitteredZero(t *testing.T) {
	base := 1234 * time.Millisecond
	if got := jittered(base, 0); got != base {
		t.Errorf("jitter=0 должен вернуть базу: got %v", got)
	}
}

func TestValidateResearchRuntimeConfig(t *testing.T) {
	valid := researchRuntimeConfig{
		PageDelayMS:            2500,
		DetailDelayMS:          6000,
		JitterPct:              40,
		ChallengePauseMin:      30,
		KPCooldownPath:         "data/kp_cooldown",
		KPRateCooldownSec:      90,
		KPChallengeCooldownMin: 30,
	}
	if err := validateResearchRuntimeConfig(valid); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	cases := []struct {
		name string
		mut  func(*researchRuntimeConfig)
		want string
	}{
		{"negative max pages", func(c *researchRuntimeConfig) { c.MaxPages = -1 }, "max-pages"},
		{"zero page delay", func(c *researchRuntimeConfig) { c.PageDelayMS = 0 }, "page-delay-ms"},
		{"zero detail delay", func(c *researchRuntimeConfig) { c.DetailDelayMS = 0 }, "detail-delay-ms"},
		{"bad jitter", func(c *researchRuntimeConfig) { c.JitterPct = 101 }, "jitter-pct"},
		{"zero challenge pause", func(c *researchRuntimeConfig) { c.ChallengePauseMin = 0 }, "challenge-pause-min"},
		{"empty cooldown path", func(c *researchRuntimeConfig) { c.KPCooldownPath = "" }, "kp-cooldown"},
		{"zero rate cooldown", func(c *researchRuntimeConfig) { c.KPRateCooldownSec = 0 }, "kp-rate-cooldown-sec"},
		{"zero challenge cooldown", func(c *researchRuntimeConfig) { c.KPChallengeCooldownMin = 0 }, "kp-challenge-cooldown-min"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid
			tc.mut(&cfg)
			err := validateResearchRuntimeConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want mention %q", err, tc.want)
			}
		})
	}
}

func TestEnvPositiveInt(t *testing.T) {
	t.Setenv("RESEARCH_TEST_INT", "42")
	var errs []string
	if got := envPositiveInt("RESEARCH_TEST_INT", 7, &errs); got != 42 || len(errs) != 0 {
		t.Fatalf("envPositiveInt valid = %d errors=%v, want 42/no errors", got, errs)
	}
	t.Setenv("RESEARCH_TEST_INT", "0")
	if got := envPositiveInt("RESEARCH_TEST_INT", 7, &errs); got != 7 {
		t.Fatalf("envPositiveInt invalid fallback = %d, want 7", got)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "RESEARCH_TEST_INT") {
		t.Fatalf("errors = %v, want one env error", errs)
	}
}

func TestIndividualScore(t *testing.T) {
	ind := models.SearchAd{KPIzlog: false, IsRenewed: false} // частник
	shop := models.SearchAd{KPIzlog: true, IsRenewed: true}  // магазин
	renewed := models.SearchAd{KPIzlog: false, IsRenewed: true}
	if individualScore(ind) <= individualScore(renewed) || individualScore(renewed) <= individualScore(shop) {
		t.Errorf("порядок счёта нарушен: частник=%d renewed=%d магазин=%d",
			individualScore(ind), individualScore(renewed), individualScore(shop))
	}
}

func TestPrioritizeIndividuals(t *testing.T) {
	ads := []models.SearchAd{
		{AdID: 1, KPIzlog: true, IsRenewed: true},   // магазин
		{AdID: 2, KPIzlog: false, IsRenewed: false}, // частник
		{AdID: 3, KPIzlog: true, IsRenewed: false},
		{AdID: 4, KPIzlog: false, IsRenewed: true},
	}
	prioritizeIndividuals(ads)
	if ads[0].AdID != 2 {
		t.Errorf("первым должен идти частник (id=2), got id=%d", ads[0].AdID)
	}
	if ads[len(ads)-1].AdID != 1 {
		t.Errorf("последним должен идти магазин (id=1), got id=%d", ads[len(ads)-1].AdID)
	}
}

func TestMatchCPUFallsBackFromRyzenProToBaseSKU(t *testing.T) {
	cpus := map[string]hw.CPU{
		hw.Key("AMD Ryzen 7 PRO 8840HS"): {Name: "AMD Ryzen 7 PRO 8840HS", Score: 16168},
		hw.Key("AMD Ryzen 7 8840U"):      {Name: "AMD Ryzen 7 8840U", Score: 14125},
	}
	cpu, ok := matchCPU(cpus, "Ryzen 7 PRO 8840HS")
	if !ok || cpu.Name != "AMD Ryzen 7 PRO 8840HS" || cpu.Score != 16168 {
		t.Fatalf("exact PRO match = %+v ok=%v", cpu, ok)
	}
	cpu, ok = matchCPU(cpus, "Ryzen 7 PRO 8840U")
	if !ok || cpu.Name != "AMD Ryzen 7 8840U" || cpu.Score != 14125 {
		t.Fatalf("PRO fallback match = %+v ok=%v", cpu, ok)
	}
}
