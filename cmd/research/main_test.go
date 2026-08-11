package main

import (
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
