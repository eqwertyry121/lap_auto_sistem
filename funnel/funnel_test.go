package funnel

import (
	"strings"
	"testing"

	"kpbot/filters"
	"kpbot/models"
	"kpbot/pricing"
)

func TestDecideL0(t *testing.T) {
	cases := []struct {
		name     string
		price    float64
		exchange bool
		title    string
		wantPass bool
	}{
		{"норма", 300, false, "Lenovo ThinkPad T480", true},
		{"нижняя граница", 0.5, false, "Лот за копейки", false},
		{"нулевая", 0, false, "Подарок", false},
		{"верхняя граница", 5001, false, "Монстр", false},
		{"ровно 5000", 5000, false, "Дорогой", true},
		{"обмен флагом", 300, true, "Ноутбук", false},
		{"обмен в заголовке", 300, false, "Продам или zamena на телефон", false},
	}
	for _, c := range cases {
		pass, _ := decideL0(c.price, c.exchange, c.title)
		if pass != c.wantPass {
			t.Errorf("%s: pass=%v, want %v", c.name, pass, c.wantPass)
		}
	}
}

func TestDecideBan(t *testing.T) {
	banned := []string{"macbook"}
	cases := []struct {
		title   string
		wantBan bool
	}{
		{"MacBook Air 13 M2 8GB 256GB SSD", true},
		{"Apple MacBook Pro 14 2021", true},
		{"Lenovo Legion 5 Ryzen 5 5600H RTX 3060", false},
		{"HP Victus 15 i5-12450H RTX 3050", false},
	}
	for _, c := range cases {
		got, _ := decideBan(c.title, banned)
		if got != c.wantBan {
			t.Errorf("decideBan(%q) = %v, want %v", c.title, got, c.wantBan)
		}
	}
}

func TestDecideL5(t *testing.T) {
	base := l5Input{
		JunkClass: filters.JunkClean, CPUName: "i5-1135G7", CPUScore: 10000,
		DevOK: true, Dev: -0.20, N: 10,
		diamondDev: -0.15, suspectDev: -0.40, marketTol: 0.05, minN: 5,
	}
	cases := []struct {
		name string
		mut  func(*l5Input)
		want string
	}{
		{"алмаз", func(in *l5Input) {}, vcDiamond},
		{"некритичный дефект не блокирует (нюанс)", func(in *l5Input) { in.JunkClass = filters.JunkDefect }, vcDiamond},
		{"сомнительный хлам → проверка", func(in *l5Input) { in.JunkClass = filters.JunkUncertain }, vcCheck},
		{"глубже −40% → подозрение", func(in *l5Input) { in.Dev = -0.55 }, vcSuspect},
		{"молодой аккаунт на скидке → подозрение", func(in *l5Input) { in.YoungSeller = true }, vcSuspect},
		{"мелкая группа → проверка", func(in *l5Input) { in.N = 3 }, vcCheck},
		{"рыночная цена → тихо (не алерт)", func(in *l5Input) { in.Dev = 0.0 }, vcFair},
		{"чуть ниже рынка, но не алмаз → тихо", func(in *l5Input) { in.Dev = -0.08 }, vcFair},
		{"дороже рынка → тихо", func(in *l5Input) { in.Dev = 0.06 }, vcExpensive},
		{"сильно дороже → тихо", func(in *l5Input) { in.Dev = 0.25 }, vcExpensive},
		{"нет CPU → вручную", func(in *l5Input) { in.CPUName = ""; in.CPUScore = 0 }, vcManual},
		{"CPU назван, но без балла → проверка", func(in *l5Input) { in.CPUScore = 0 }, vcCheck},
		{"железо есть, сравнивать не с чем → RARE_NO_MARKET", func(in *l5Input) { in.DevOK = false }, vcMoose},
	}
	for _, c := range cases {
		in := base
		c.mut(&in)
		if got := decideL5(in); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestDecideL5DominatedDiamond(t *testing.T) {
	in := l5Input{
		JunkClass: filters.JunkClean, CPUName: "i5-1135G7", CPUScore: 10000,
		DevOK: true, Dev: -0.20, N: 10, Dominated: true,
		diamondDev: -0.15, suspectDev: -0.40, marketTol: 0.05, minN: 5,
	}
	if got := decideL5(in); got != vcOutclassed {
		t.Fatalf("dominated diamond got %s, want OUTCLASSED", got)
	}
}

func TestDecideL5StepUpOutclassedDiamond(t *testing.T) {
	in := l5Input{
		JunkClass: filters.JunkClean, CPUName: "i7-10510U", CPUScore: 4100,
		DevOK: true, Dev: -0.28, N: 1152, StepUpOutclassed: true,
		diamondDev: -0.15, suspectDev: -0.40, marketTol: 0.05, minN: 5,
	}
	if got := decideL5(in); got != vcOutclassed {
		t.Fatalf("step-up outclassed diamond got %s, want OUTCLASSED", got)
	}
}

func TestStepUpOutclassesLatitude3510Case(t *testing.T) {
	target := pricing.Lot{
		Price: 200, CPUScore: 4100, GPUScore: 4125,
	}
	step := pricing.Lot{
		AdID: 210, Title: "Lenovo Y520", URL: "https://example.test/lenovo-y520",
		Price: 210, CPUScore: 10000, GPUScore: 14947,
	}
	if !stepUpOutclasses(target, &step) {
		t.Fatal("Latitude 3510 alert must be suppressed: +€10 gives +203% composite score")
	}

	normalStep := pricing.Lot{
		AdID: 550, Title: "Normal step", URL: "https://example.test/normal",
		Price: 550, CPUScore: 30000, GPUScore: 30000,
	}
	normalTarget := pricing.Lot{Price: 500, CPUScore: 25000, GPUScore: 25000}
	if stepUpOutclasses(normalTarget, &normalStep) {
		t.Fatal("ordinary +10% price / +20% power step-up must stay informational")
	}
}

func TestWithoutProductionOLSKeepsAlternatives(t *testing.T) {
	dom := pricing.Lot{AdID: 2, URL: "https://example.test/dom", Price: 210, CPUScore: 10000}
	step := pricing.Lot{AdID: 3, URL: "https://example.test/step", Price: 220, CPUScore: 12000}
	eval := pricing.MarketEvaluation{
		Estimate:           pricing.PriceEstimate{Level: "K3", Median: 300, RawMedian: 300, N: 80},
		ComparableMedian:   300,
		ComparableP25:      250,
		OpportunityCeiling: 210,
		OpportunityBy:      &dom,
		DominatedBy:        &dom,
		StepUp:             &step,
		Confidence:         "LOW",
		Deviation:          -0.30,
		DevOK:              true,
	}

	got, dropped := withoutProductionOLS(eval)
	if !dropped {
		t.Fatal("K3 estimate must be disabled for production")
	}
	if got.DevOK || got.Estimate.Level != "" || got.ComparableMedian != 0 || got.OpportunityBy != nil {
		t.Fatalf("K3 market fields were not cleared: %+v", got)
	}
	if got.DominatedBy == nil || got.DominatedBy.AdID != dom.AdID || got.StepUp == nil || got.StepUp.AdID != step.AdID {
		t.Fatalf("alternatives must survive K3 clearing: %+v", got)
	}
}

func TestManualAlertWorthy(t *testing.T) {
	cases := []struct {
		price  float64
		minEUR int
		want   bool
	}{
		{399.99, 400, false}, // дешевле порога — время не тратим
		{400, 400, true},     // ровно порог — алерт
		{1200, 400, true},
		{50, 400, false},
	}
	for _, c := range cases {
		if got := manualAlertWorthy(c.price, c.minEUR); got != c.want {
			t.Errorf("manualAlertWorthy(%.2f, %d) = %v, want %v", c.price, c.minEUR, got, c.want)
		}
	}
}

func TestMooseAlertWorthy(t *testing.T) {
	cases := []struct {
		price  float64
		minEUR int
		want   bool
	}{
		{399.99, 400, false}, // дешевле порога — только аудит
		{400, 400, true},     // ровно порог — сводка в Telegram
		{1100, 400, true},
		{30, 400, false},
	}
	for _, c := range cases {
		if got := mooseAlertWorthy(c.price, c.minEUR); got != c.want {
			t.Errorf("mooseAlertWorthy(%.2f, %d) = %v, want %v", c.price, c.minEUR, got, c.want)
		}
	}
}

func TestMooseAlertText_NoOwnerClaims(t *testing.T) {
	// PLAN_v8: текст НЕ характеризует продавца. Формулировка v7 «владелец
	// лось, но я добыл инфу» клеветала на продавцов, у которых спеки указаны
	// в самом объявлении (кейсы дня 2026-08-06: Legion 5, Acer VX15).
	ad := models.SearchAd{Name: "Lenovo Legion 5 slim 16 Ryzen 7 7735hs/16gb/1TB/4060",
		AdURL: "/kompjuteri-laptop-i-tablet/laptopovi/oglas/194373542"}
	lot := pricing.Lot{Price: 1100, CPUScore: 13693, GPUScore: 51866}
	text := mooseAlertText(ad, "Ryzen 7 7735HS · 16GB · SSD 1024GB · RTX 4060", lot, "")
	for _, banned := range []string{"ВЛАДЕЛЕЦ", "владелец", "добыл"} {
		if strings.Contains(text, banned) {
			t.Errorf("в тексте есть %q (характеристика продавца): %s", banned, text)
		}
	}
	if !strings.Contains(text, "СРАВНИТЬ НЕ С ЧЕМ") {
		t.Errorf("нет честного диагноза «сравнить не с чем»: %s", text)
	}
}

func TestBuildSpecsLine(t *testing.T) {
	if got := buildSpecsLine("Lenovo ThinkPad T480", "i5-1135G7", 16, 512, "RTX 3060", false); got != "Lenovo ThinkPad T480 · i5-1135G7 · 16GB · SSD 512GB · RTX 3060" {
		t.Errorf("полная строка: %q", got)
	}
	if got := buildSpecsLine("", "i5-1135G7", 0, 0, "", false); got != "i5-1135G7" {
		t.Errorf("только CPU: %q", got)
	}
	if got := buildSpecsLine("HP EliteBook 840 G6", "i5-8250U", 8, 0, "", true); got != "HP EliteBook 840 G6 · i5-8250U · 8GB · встроенная графика" {
		t.Errorf("встроенная графика: %q", got)
	}
	if got := buildSpecsLine("", "", 0, 0, "", false); got != "железо не распознано" {
		t.Errorf("пусто: %q", got)
	}
}
