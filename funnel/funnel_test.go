package funnel

import (
	"context"
	"database/sql"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"kpbot/filters"
	"kpbot/hw"
	"kpbot/models"
	"kpbot/pricing"
	"kpbot/specs"
	"kpbot/vision"
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

func TestDecideL5ConfigConflictForcesCheck(t *testing.T) {
	in := l5Input{
		JunkClass: filters.JunkClean, CPUName: "i5-1135G7", CPUScore: 10000,
		DevOK: true, Dev: -0.20, N: 10, ConfigConflict: true,
		diamondDev: -0.15, suspectDev: -0.40, marketTol: 0.05, minN: 5,
	}
	if got := decideL5(in); got != vcCheck {
		t.Fatalf("config conflict got %s, want CHECK", got)
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

func TestStepUpOutclassesFujitsuH7510Case(t *testing.T) {
	target := pricing.Lot{
		AdID: 449, Title: "Fujitsu H7510 i7-10850H Quadro T1000",
		Price: 449, CPUScore: 7198, GPUScore: 15125,
	}
	step := pricing.Lot{
		AdID: 450, Title: "Acer Predator Helios i7 RTX",
		URL:   "https://example.test/acer-predator-helios",
		Price: 450, CPUScore: 20000, GPUScore: 26087,
	}
	if !stepUpOutclasses(target, &step) {
		t.Fatal("Fujitsu H7510 alert must be suppressed: +€1 gives +106% composite score")
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

func TestDiamondSuppressionAllowsKnownIntegratedGPU(t *testing.T) {
	got := diamondSuppressionReason(pricing.PriceEstimate{Level: "K0"}, 0, true, "used", true, filters.ClassPrivate)
	if got != "" {
		t.Fatalf("known integrated GPU must not be suppressed as unknown GPU: %q", got)
	}
	got = diamondSuppressionReason(pricing.PriceEstimate{Level: "K0"}, 0, false, "used", true, filters.ClassPrivate)
	if !strings.Contains(got, "unknown GPU score") {
		t.Fatalf("unknown GPU must still suppress diamond, got %q", got)
	}
}

func TestDiamondSuppressionRejectsUnknownSellerClass(t *testing.T) {
	got := diamondSuppressionReason(pricing.PriceEstimate{Level: "K0"}, 1000, false, "used", true, filters.ClassUnknown)
	if !strings.Contains(got, "unknown seller type") {
		t.Fatalf("unknown seller class must suppress diamond, got %q", got)
	}
	got = diamondSuppressionReason(pricing.PriceEstimate{Level: "K0"}, 1000, false, "used", false, filters.ClassPrivate)
	if !strings.Contains(got, "unknown seller type") {
		t.Fatalf("missing seller record must suppress diamond, got %q", got)
	}
}

func TestDiamondSuppressionRejectsUnknownConditionValues(t *testing.T) {
	for _, condition := range []string{"", "unknown", "UNKNOWN", "n/a", "nije navedeno"} {
		got := diamondSuppressionReason(pricing.PriceEstimate{Level: "K0"}, 1000, false, condition, true, filters.ClassPrivate)
		if !strings.Contains(got, "unknown condition") {
			t.Fatalf("condition %q must suppress diamond, got %q", condition, got)
		}
	}
	for _, condition := range []string{"used", "new", "polovno", "novo"} {
		got := diamondSuppressionReason(pricing.PriceEstimate{Level: "K0"}, 1000, false, condition, true, filters.ClassPrivate)
		if strings.Contains(got, "unknown condition") {
			t.Fatalf("condition %q must be known, got %q", condition, got)
		}
	}
}

func TestReviewAlertSuppressionForUnknownSeller(t *testing.T) {
	reason := reviewAlertSuppressionReason(filters.ClassUnknown, []string{"garancija"})
	if !strings.Contains(reason, "unknown seller type") || !strings.Contains(reason, "garancija") {
		t.Fatalf("review suppression reason = %q", reason)
	}
	for _, code := range []string{vcManual, vcCheck, vcMoose} {
		gotCode, gotReason := applyReviewAlertSuppression(code, reason)
		if gotCode != vcReviewSuppressed || gotReason == "" {
			t.Fatalf("code %s suppression = %s/%q, want %s/reason", code, gotCode, gotReason, vcReviewSuppressed)
		}
	}
	for _, code := range []string{vcFair, vcExpensive, vcOutclassed, vcSuppressed} {
		gotCode, gotReason := applyReviewAlertSuppression(code, reason)
		if gotCode != code || gotReason != "" {
			t.Fatalf("quiet code %s suppression = %s/%q, want unchanged", code, gotCode, gotReason)
		}
	}
	if got := reviewAlertSuppressionReason(filters.ClassPrivate, nil); got != "" {
		t.Fatalf("private seller suppression reason = %q, want empty", got)
	}
}

func TestConditionKindUsesDetailWithSearchFallback(t *testing.T) {
	if got := listingCondition("", "used"); got != "used" {
		t.Fatalf("listingCondition fallback = %q, want used", got)
	}
	if got := listingCondition("new", "used"); got != "new" {
		t.Fatalf("listingCondition detail priority = %q, want new", got)
	}

	cases := []struct {
		condition string
		want      string
	}{
		{"new", "NEW"},
		{"novo", "NEW"},
		{"used", "USED"},
		{"polovno", "USED"},
		{"broken", "BROKEN"},
		{"", "UNKNOWN"},
		{"nije navedeno", "UNKNOWN"},
	}
	for _, c := range cases {
		if got := conditionKind(c.condition); got != c.want {
			t.Fatalf("conditionKind(%q) = %q, want %q", c.condition, got, c.want)
		}
	}
}

func TestMatchCPUFallsBackFromRyzenProToBaseSKU(t *testing.T) {
	cpus := map[string]hw.CPU{
		hw.Key("AMD Ryzen 7 PRO 8840HS"): {Name: "AMD Ryzen 7 PRO 8840HS", Score: 16168},
		hw.Key("AMD Ryzen 7 8840U"):      {Name: "AMD Ryzen 7 8840U", Score: 14125},
	}
	name, score := matchCPU(cpus, "Ryzen 7 PRO 8840HS")
	if name != "AMD Ryzen 7 PRO 8840HS" || score != 16168 {
		t.Fatalf("exact PRO match = %q %.0f", name, score)
	}
	name, score = matchCPU(cpus, "Ryzen 7 PRO 8840U")
	if name != "AMD Ryzen 7 8840U" || score != 14125 {
		t.Fatalf("PRO fallback match = %q %.0f", name, score)
	}
	cpus[hw.Key("AMD Ryzen AI 7 350")] = hw.CPU{Name: "AMD Ryzen AI 7 350", Score: 15016}
	name, score = matchCPU(cpus, "Ryzen AI 7 PRO 350")
	if name != "AMD Ryzen AI 7 350" || score != 15016 {
		t.Fatalf("Ryzen AI PRO fallback match = %q %.0f", name, score)
	}
}

func TestHardwareConflictNormalization(t *testing.T) {
	if hardwareConflict("Intel Core i7-10510U", "i7-10510U") {
		t.Fatal("normalized aliases for the same CPU must not conflict")
	}
	if !hardwareConflict("i7-10510U", "i7-10850H") {
		t.Fatal("different CPUs must conflict")
	}
	if hardwareConflict("", "i7-10510U") {
		t.Fatal("missing previous evidence must not be treated as conflict")
	}
}

func TestSpecSourcesLine(t *testing.T) {
	got := specSourcesLine("regex", "model-catalog", "", "gemini-photo-all", "gemini-text+regex")
	want := "laptop:regex,cpu:model-catalog,ssd:gemini-photo-all,gpu:gemini-text+regex"
	if got != want {
		t.Fatalf("specSourcesLine = %q, want %q", got, want)
	}
	if got := specSourcesLine("", "", "", "", ""); got != "" {
		t.Fatalf("empty specSourcesLine = %q, want empty", got)
	}
}

func TestGeminiPhotoPromptKeepsKnownModelHint(t *testing.T) {
	prompt := geminiPhotoPrompt(
		"Lenovo ThinkPad E14 Gen 6 - Ryzen 7 / 16GB / 512GB",
		"Prodajem laptop, specifikacije su na poslednjoj slici.",
		"Lenovo ThinkPad E14 Gen 6",
		[]models.Attribute{{Name: "Procesor", Value: "Ryzen 7"}},
	)
	if !strings.Contains(prompt, "Laptop model hint from title/description: Lenovo ThinkPad E14 Gen 6") {
		t.Fatalf("prompt does not include known model hint:\n%s", prompt)
	}
	if strings.Contains(prompt, "No exact laptop model was found") {
		t.Fatalf("prompt must not claim missing model when model is known:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Photos are attached in the original listing order") {
		t.Fatalf("prompt must remind Gemini to inspect all photos in order:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Procesor: Ryzen 7") {
		t.Fatalf("prompt must include listing attributes before photos:\n%s", prompt)
	}
}

func TestGeminiSpecStageOrderPrefersAllPhotos(t *testing.T) {
	got := geminiSpecStageOrder(true, 7, false, false)
	if len(got) != 2 || got[0] != geminiStagePhoto || got[1] != geminiStageText {
		t.Fatalf("stage order = %+v, want photo before text", got)
	}
	got = geminiSpecStageOrder(true, 7, true, false)
	if len(got) != 1 || got[0] != geminiStageText {
		t.Fatalf("cached photo stage order = %+v, want text fallback only", got)
	}
	got = geminiSpecStageOrder(false, 7, false, false)
	if len(got) != 0 {
		t.Fatalf("complete deterministic specs should not call Gemini, got %+v", got)
	}
}

func TestDownloadGeminiPhotoPartsRequiresAllPhotos(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("ok"))
			return
		}
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer srv.Close()

	gem := vision.NewGeminiClient("key", "gemini-2.5-flash-lite")
	parts, failures := downloadGeminiPhotoParts(context.Background(), gem, []models.PhotoDoc{
		{Big: srv.URL + "/ok"},
		{Big: srv.URL + "/missing"},
	})
	if len(parts) != 0 {
		t.Fatalf("parts len = %d, want 0 on partial download", len(parts))
	}
	if len(failures) != 1 || !strings.Contains(failures[0], "фото 2/2") {
		t.Fatalf("failures = %+v, want photo 2/2 failure", failures)
	}
}

func TestDownloadGeminiPhotoPartsKeepsListingOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		switch r.URL.Path {
		case "/one":
			_, _ = w.Write([]byte("one"))
		case "/two":
			_, _ = w.Write([]byte("two"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	gem := vision.NewGeminiClient("key", "gemini-2.5-flash-lite")
	parts, failures := downloadGeminiPhotoParts(context.Background(), gem, []models.PhotoDoc{
		{Big: srv.URL + "/one"},
		{Big: srv.URL + "/two"},
	})
	if len(failures) != 0 {
		t.Fatalf("failures = %+v", failures)
	}
	if len(parts) != 2 {
		t.Fatalf("parts len = %d, want 2", len(parts))
	}
	got1, _ := base64.StdEncoding.DecodeString(parts[0].InlineData.Data)
	got2, _ := base64.StdEncoding.DecodeString(parts[1].InlineData.Data)
	if string(got1) != "one" || string(got2) != "two" {
		t.Fatalf("photo order = %q, %q; want one, two", got1, got2)
	}
}

func TestValueAlertTextSeparatesComparableMarketAndCeiling(t *testing.T) {
	stronger := pricing.Lot{
		AdID: 2, Title: "Stronger Lenovo", URL: "https://example.test/stronger",
		Price: 110, CPUScore: 14000, GPUScore: 8000,
	}
	lot := pricing.Lot{
		AdID: 1, Title: "Target Dell", URL: "https://example.test/target",
		Price: 100, CPUScore: 5000, GPUScore: 3000,
	}
	est := pricing.PriceEstimate{
		Median: 110, RawMedian: 250, P25: 180, N: 12, Level: "K0",
		CompetitiveCap: 110, CapLot: &stronger,
	}
	eval := pricing.MarketEvaluation{
		Estimate:           est,
		ComparableMedian:   250,
		ComparableP25:      180,
		OpportunityCeiling: 110,
		OpportunityBy:      &stronger,
		Confidence:         "HIGH",
	}
	ad := models.SearchAd{AdID: 1, Name: "Target Dell", AdURL: "/target"}

	text := valueAlertText(vcDiamond, ad, "i7 · 16GB · SSD 512GB", lot, est, -0.09, "", nil, eval)
	for _, want := range []string{
		"медиана сопоставимых €250",
		"нижний квартиль €180",
		"confidence=HIGH",
		"Рациональный потолок: €110",
		"https://example.test/stronger",
		"Ориентир для решения: €110",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("alert text missing %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"Opportunity ceiling", "Рыночный ориентир (наши данные KP)", "Средняя цена", "средняя цена"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("alert text contains old wording %q:\n%s", forbidden, text)
		}
	}
}

func TestCachedGeminiSpecsMergePartialStages(t *testing.T) {
	dbPath := newSpecsCacheDB(t)
	ctx := context.Background()

	if err := saveCachedGeminiSpecs(ctx, dbPath, 101, "gemini-text", specs.GeminiSpecs{
		LaptopModel: "Fujitsu Celsius H7510", CPU: "i7-10850H", RAMGB: 32, SSDGB: 512,
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !researchSpecsColumnExists(t, dbPath, "laptop_model") {
		t.Fatal("saveCachedGeminiSpecs must upgrade old research_specs schema with laptop_model")
	}
	if !researchSpecsColumnExists(t, dbPath, "source") {
		t.Fatal("saveCachedGeminiSpecs must upgrade old research_specs schema with source")
	}
	if err := saveCachedGeminiSpecs(ctx, dbPath, 101, "gemini-photo-all", specs.GeminiSpecs{
		GPU: "NVIDIA Quadro T1000",
	}, nil, nil); err != nil {
		t.Fatal(err)
	}

	got, source, ok := loadCachedGeminiSpecs(ctx, dbPath, 101)
	if !ok {
		t.Fatal("cached Gemini specs not found")
	}
	if got.LaptopModel != "Fujitsu Celsius H7510" || got.CPU != "i7-10850H" || got.RAMGB != 32 || got.SSDGB != 512 || got.GPU != "NVIDIA Quadro T1000" {
		t.Fatalf("merged specs = %+v", got)
	}
	if !hasSpecSource(source, "gemini-text") || !hasSpecSource(source, "gemini-photo-all") {
		t.Fatalf("source %q must keep both stages", source)
	}
}

func TestSpecSourceTokensAreExact(t *testing.T) {
	source := "gemini-photo-all+gemini-search"
	if hasSpecSource(source, "gemini-text") {
		t.Fatalf("%q must not imply gemini-text", source)
	}
	if !hasSpecSource(source, "gemini-photo-all") || !hasSpecSource(source, "gemini-search") {
		t.Fatalf("%q source tokens not detected", source)
	}
}

func TestCachedModelCatalogSpecsAreTrusted(t *testing.T) {
	dbPath := newSpecsCacheDB(t)
	ctx := context.Background()

	if err := saveCachedGeminiSpecs(ctx, dbPath, 202, "model-catalog", specs.GeminiSpecs{
		LaptopModel: "Lenovo ThinkPad E14 Gen 6 21M3003PCX", CPU: "Ryzen 7 7735HS", RAMGB: 16, SSDGB: 512, GPU: "integrated",
	}, nil, nil); err != nil {
		t.Fatal(err)
	}

	got, source, ok := loadCachedGeminiSpecs(ctx, dbPath, 202)
	if !ok {
		t.Fatal("model-catalog specs must be loaded as trusted cache")
	}
	if got.LaptopModel != "Lenovo ThinkPad E14 Gen 6 21M3003PCX" || got.CPU != "Ryzen 7 7735HS" || got.RAMGB != 16 || got.SSDGB != 512 || !got.GPUIntegrated() {
		t.Fatalf("cached catalog specs = %+v", got)
	}
	if source != "model-catalog" {
		t.Fatalf("source = %q, want model-catalog", source)
	}
}

func newSpecsCacheDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "research.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE research_specs (
	ad_id INTEGER PRIMARY KEY,
	cpu_model TEXT NOT NULL DEFAULT '',
	cpu_score REAL NOT NULL DEFAULT 0,
	ram_gb INTEGER NOT NULL DEFAULT 0,
	ssd_gb INTEGER NOT NULL DEFAULT 0,
	gpu_model TEXT NOT NULL DEFAULT '',
	gpu_score REAL NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL DEFAULT 0
);`); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func researchSpecsColumnExists(t *testing.T, dbPath, column string) bool {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`PRAGMA table_info(research_specs)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     string
			notNull int
			def     sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &def, &pk); err != nil {
			t.Fatal(err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return false
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
