package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"kpbot/hw"

	_ "modernc.org/sqlite"
)

// Регрессия сортировки: очередь обязана идти по EUR-цене, а не по сырой
// (RSD-номиналы в ~117 раз больше и раньше ломали порядок «сначала дорогое»).
func TestSelectCandidatesOrdersByEUR(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	schema := `
CREATE TABLE research_ads (
	ad_id INTEGER PRIMARY KEY,
	title TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	attrs_json TEXT NOT NULL DEFAULT '',
	price REAL NOT NULL DEFAULT 0,
	currency TEXT NOT NULL DEFAULT 'EUR',
	fetch_status TEXT NOT NULL DEFAULT 'OK'
);
CREATE TABLE research_specs (
	ad_id INTEGER PRIMARY KEY,
	cpu_score REAL NOT NULL DEFAULT 0,
	source TEXT NOT NULL DEFAULT 'regex'
);`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		id       int64
		price    float64
		currency string
	}{
		{1, 3000, "EUR"},   // €3000
		{2, 468800, "RSD"}, // €4000 — должен быть ПЕРВЫМ
		{3, 6000, "EUR"},   // €6000 — выше потолка 5000, отфильтрован
		{4, 590000, "RSD"}, // ~€5034 — выше потолка, отфильтрован
		{5, 5, "EUR"},      // €5 — ниже порога 10, отфильтрован
		{6, 999999, "RSD"}, // распознан CPU — исключён
		{7, 999998, "RSD"}, // уже обработан Gemini — исключён
		{8, 999997, "RSD"},
		{9, 999996, "RSD"},
	}
	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO research_ads (ad_id, title, description, price, currency) VALUES (?,?,?,?,?)`,
			r.id, "t", "desc", r.price, r.currency); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO research_specs (ad_id, cpu_score) VALUES (6, 100)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO research_specs (ad_id, cpu_score, source) VALUES (7, 0, 'gemini-text')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO research_specs (ad_id, cpu_score, source) VALUES (8, 0, 'model-catalog')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO research_specs (ad_id, cpu_score, source) VALUES (9, 0, 'regex-no-score')`); err != nil {
		t.Fatal(err)
	}

	cands := selectCandidates(context.Background(), db, 10, 117.2)
	if len(cands) != 2 {
		t.Fatalf("кандидатов %d, жду 2: %+v", len(cands), cands)
	}
	if cands[0].AdID != 2 {
		t.Errorf("первым должен быть RSD-лот €4000 (ad_id=2), got ad_id=%d", cands[0].AdID)
	}
	if cands[1].AdID != 1 {
		t.Errorf("вторым должен быть EUR-лот €3000 (ad_id=1), got ad_id=%d", cands[1].AdID)
	}
}

func TestWriteBackMergesPartialSpecs(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
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
	updated_at INTEGER NOT NULL DEFAULT 0,
	source TEXT NOT NULL DEFAULT 'regex'
);`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO research_specs (ad_id, cpu_model, cpu_score, ram_gb, ssd_gb, source)
VALUES (10, 'i7-10850H', 7198, 32, 512, 'regex')`); err != nil {
		t.Fatal(err)
	}

	if err := writeBack(context.Background(), db, 10, geminiSpecs{LaptopModel: "Fujitsu Celsius H7510", GPU: "NVIDIA Quadro T1000"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	var laptopModel, cpu, gpu, source string
	var ram, ssd int
	if err := db.QueryRow(`
SELECT laptop_model, cpu_model, ram_gb, ssd_gb, gpu_model, source FROM research_specs WHERE ad_id=10`).
		Scan(&laptopModel, &cpu, &ram, &ssd, &gpu, &source); err != nil {
		t.Fatal(err)
	}
	if laptopModel != "Fujitsu Celsius H7510" || cpu != "i7-10850H" || ram != 32 || ssd != 512 || gpu != "NVIDIA Quadro T1000" || source != "gemini-text+regex" {
		t.Fatalf("merged row = model=%q cpu=%q ram=%d ssd=%d gpu=%q source=%q", laptopModel, cpu, ram, ssd, gpu, source)
	}
}

func TestWriteBackMatchesRyzenProAlias(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
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
	updated_at INTEGER NOT NULL DEFAULT 0,
	source TEXT NOT NULL DEFAULT 'regex'
);`); err != nil {
		t.Fatal(err)
	}

	cpus := map[string]hw.CPU{
		hw.Key("AMD Ryzen 7 8840U"): {Name: "AMD Ryzen 7 8840U", Score: 14125},
	}
	if err := writeBack(context.Background(), db, 11, geminiSpecs{CPU: "Ryzen 7 PRO 8840U"}, cpus, nil); err != nil {
		t.Fatal(err)
	}

	var cpu, source string
	var score float64
	if err := db.QueryRow(`SELECT cpu_model, cpu_score, source FROM research_specs WHERE ad_id=11`).Scan(&cpu, &score, &source); err != nil {
		t.Fatal(err)
	}
	if cpu != "AMD Ryzen 7 8840U" || score != 14125 || source != "gemini-text" {
		t.Fatalf("writeBack cpu=%q score=%.0f source=%q", cpu, score, source)
	}
}

func TestDeterministicSpecsUsesExactModelCatalog(t *testing.T) {
	sp, source, ok := deterministicSpecs(candidate{
		Title: "Lenovo ThinkPad E14 Gen 6 Ryzen 7 16GB 512GB",
		Desc:  "Model 21M3003PCX",
	}, nil)
	if !ok {
		t.Fatal("deterministicSpecs did not match exact model catalog")
	}
	if source != "model-catalog" {
		t.Fatalf("source = %q, want model-catalog", source)
	}
	if sp.LaptopModel != "Lenovo ThinkPad E14 Gen 6 21M3003PCX" ||
		sp.CPU != "Ryzen 7 7735HS" ||
		sp.RAMGB != 16 ||
		sp.SSDGB != 512 ||
		!sp.GPUIntegrated() {
		t.Fatalf("specs = %+v", sp)
	}
}

func TestDeterministicSpecsUsesRegexWhenCPUIsExact(t *testing.T) {
	cpus := map[string]hw.CPU{
		hw.Key("Intel Core i7-10850H"): {Name: "Intel Core i7-10850H", Score: 7198},
	}
	sp, source, ok := deterministicSpecs(candidate{
		Title: "Fujitsu H7510 i7-10850H,32gb ddr4,512NVMe,15.6,Nvidia-4gb",
		Desc:  "NVIDIA Quadro T1000",
	}, cpus)
	if !ok {
		t.Fatal("deterministicSpecs did not use regex CPU")
	}
	if source != "regex" {
		t.Fatalf("source = %q, want regex", source)
	}
	if sp.CPU != "i7-10850H" || sp.RAMGB != 32 || sp.SSDGB != 512 || sp.GPU != "Quadro T1000" {
		t.Fatalf("specs = %+v", sp)
	}
}

func TestDeterministicSpecsMarksRegexNoScore(t *testing.T) {
	sp, source, ok := deterministicSpecs(candidate{
		Title: "Lenovo Yoga Ryzen AI 7 350 16GB 512GB Radeon 860M",
		Desc:  "excellent",
	}, nil)
	if !ok {
		t.Fatal("deterministicSpecs did not use exact CPU text")
	}
	if source != "regex-no-score" {
		t.Fatalf("source = %q, want regex-no-score", source)
	}
	if sp.CPU != "Ryzen AI 7 350" || sp.GPU != "integrated" {
		t.Fatalf("specs = %+v", sp)
	}
}

func TestDeterministicSpecsNeedsGeminiWhenCPUIsMissing(t *testing.T) {
	if sp, source, ok := deterministicSpecs(candidate{
		Title: "Lenovo ThinkPad E14 Gen 6 16GB 512GB",
		Desc:  "bez oznake procesora",
	}, nil); ok {
		t.Fatalf("deterministicSpecs = %+v source=%q, want no deterministic specs", sp, source)
	}
}

func TestWriteBackSourceUsesExplicitSource(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
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
	updated_at INTEGER NOT NULL DEFAULT 0,
	source TEXT NOT NULL DEFAULT 'regex'
);`); err != nil {
		t.Fatal(err)
	}

	cpus := map[string]hw.CPU{
		hw.Key("AMD Ryzen 7 7735HS"): {Name: "AMD Ryzen 7 7735HS", Score: 18729},
	}
	if err := writeBackSource(context.Background(), db, 12, geminiSpecs{CPU: "Ryzen 7 7735HS"}, "model-catalog", cpus, nil); err != nil {
		t.Fatal(err)
	}

	var cpu, source string
	var score float64
	if err := db.QueryRow(`SELECT cpu_model, cpu_score, source FROM research_specs WHERE ad_id=12`).Scan(&cpu, &score, &source); err != nil {
		t.Fatal(err)
	}
	if cpu != "AMD Ryzen 7 7735HS" || score != 18729 || source != "model-catalog" {
		t.Fatalf("writeBackSource cpu=%q score=%.0f source=%q", cpu, score, source)
	}
}
