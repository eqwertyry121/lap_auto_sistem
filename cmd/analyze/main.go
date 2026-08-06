// analyze — срез датасета рынка + движок «цена/качество».
//
// Использует обогащённый датасет (go run ./cmd/research -enrich):
// распознанное железо (specs) сверено с эталонной базой мощности (hw.db).
//
// Режимы:
//
//	go run ./cmd/analyze            # общий срез: покрытие, цены по железу, лучшие сделки
//	go run ./cmd/analyze -lot 12345 # вердикт по конкретному лоту + альтернативы
//	go run ./cmd/analyze -pricing   # разведочный отчёт ценового движка (Фаза 4А)
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"kpbot/pricing"
	"os"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

// rsdToEurRate — курс для нормализации цен: живой из pricing (kv-кэш →
// frankfurter.app → fallback), единый владелец — pricing (§6.3).
var rsdToEurRate = pricing.FallbackRsdEur

type lot struct {
	AdID     int64
	Title    string
	Price    float64
	Currency string
	URL      string
	Kind     string
	CPUModel string
	CPUScore float64
	RAMGB    int
	SSDGB    int
	GPUModel string
}

func (l lot) eur() float64 { return toEUR(l.Price, l.Currency) }

func (l lot) specLabel() string {
	parts := []string{}
	if l.CPUModel != "" {
		parts = append(parts, l.CPUModel)
	}
	if l.RAMGB > 0 {
		parts = append(parts, fmt.Sprintf("%dGB", l.RAMGB))
	}
	if l.SSDGB > 0 {
		parts = append(parts, fmt.Sprintf("SSD %dGB", l.SSDGB))
	}
	if l.GPUModel != "" {
		parts = append(parts, l.GPUModel)
	}
	if len(parts) == 0 {
		return "железо не распознано"
	}
	return strings.Join(parts, " / ")
}

func main() {
	var (
		dbPath    = flag.String("db", "data/research.db", "SQLite-файл датасета рынка")
		lotID     = flag.Int64("lot", 0, "оценить конкретный лот по ad_id")
		topN      = flag.Int("top", 15, "сколько лучших сделок показывать")
		doPricing = flag.Bool("pricing", false, "разведочный отчёт ценового движка (Фаза 4А: покрытие групп, OLS, девиации)")
		doMarkers = flag.Bool("markers", false, "частотный анализ описаний: маркеры магазинов vs частники (Фаза 2)")
		markerCSV = flag.String("markers-csv", "data/marker_candidates.csv", "куда сохранить кандидатов маркеров")
		minLift   = flag.Float64("min-lift", 3.0, "минимальный lift кандидата-маркера")
		minSupp   = flag.Float64("min-support", 10.0, "минимальная поддержка в когорте магазинов, %%")
	)
	flag.Parse()

	ctx := context.Background()

	// Живой курс RSD→EUR (kv-кэш → frankfurter → fallback). Источник
	// печатается в отчёте -pricing; здесь достаточно самого значения.
	rsdToEurRate, _ = pricing.ResolveRate(ctx, *dbPath)

	if *doMarkers {
		markerReport(ctx, *dbPath, *markerCSV, *minLift, *minSupp)
		return
	}

	if *doPricing {
		pricingReport(ctx, *dbPath)
		return
	}

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		fmt.Println("датасет:", err)
		os.Exit(1)
	}
	defer db.Close()

	lots, err := loadLots(ctx, db)
	if err != nil {
		fmt.Println("выборка:", err)
		os.Exit(1)
	}

	if *lotID > 0 {
		analyzeLot(lots, *lotID)
		return
	}
	fullReport(lots, *topN)
}

func loadLots(ctx context.Context, db *sql.DB) ([]lot, error) {
	rows, err := db.QueryContext(ctx, `
SELECT a.ad_id, a.title, a.price, a.currency, a.url, a.kind,
	COALESCE(s.cpu_model,''), COALESCE(s.cpu_score,0), COALESCE(s.ram_gb,0),
	COALESCE(s.ssd_gb,0), COALESCE(s.gpu_model,'')
FROM research_ads a
LEFT JOIN research_specs s ON s.ad_id = a.ad_id
WHERE a.fetch_status IN ('SEARCH','OK')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []lot
	for rows.Next() {
		var l lot
		if err := rows.Scan(&l.AdID, &l.Title, &l.Price, &l.Currency, &l.URL, &l.Kind,
			&l.CPUModel, &l.CPUScore, &l.RAMGB, &l.SSDGB, &l.GPUModel); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func fullReport(lots []lot, topN int) {
	total := len(lots)
	var cpuOK, ramOK, ssdOK, gpuOK, withDesc int
	var zeroPrice, hugePrice int

	for _, l := range lots {
		if l.CPUScore > 0 {
			cpuOK++
		}
		if l.RAMGB > 0 {
			ramOK++
		}
		if l.SSDGB > 0 {
			ssdOK++
		}
		if l.GPUModel != "" {
			gpuOK++
		}
		eur := l.eur()
		if eur <= 1 {
			zeroPrice++
		} else if eur >= 5000 {
			hugePrice++
		}
	}
	_ = withDesc

	pct := func(n int) string { return fmt.Sprintf("%.0f%%", 100*float64(n)/float64(max(total, 1))) }

	fmt.Printf("=== ДАТАСЕТ ===\nВсего лотов: %d\n\n", total)
	fmt.Printf("=== ПОКРЫТИЕ (regex-слой, сверено с эталоном) ===\n")
	fmt.Printf("CPU распознан и найден в эталоне: %d (%s)\n", cpuOK, pct(cpuOK))
	fmt.Printf("RAM: %d (%s) | SSD: %d (%s) | GPU: %d (%s)\n\n", ramOK, pct(ramOK), ssdOK, pct(ssdOK), gpuOK, pct(gpuOK))

	fmt.Printf("=== АНОМАЛИИ ЦЕН (исключаются из медиан) ===\n")
	fmt.Printf("≤€1: %d (%s) | ≥€5000: %d (%s)\n\n", zeroPrice, pct(zeroPrice), hugePrice, pct(hugePrice))

	// Цены по моделям CPU.
	type cpuStat struct {
		model  string
		score  float64
		prices []float64
	}
	byCPU := make(map[string]*cpuStat)
	for _, l := range lots {
		if l.CPUScore == 0 {
			continue
		}
		eur := l.eur()
		if eur <= 10 || eur >= 5000 {
			continue
		}
		st := byCPU[l.CPUModel]
		if st == nil {
			st = &cpuStat{model: l.CPUModel, score: l.CPUScore}
			byCPU[l.CPUModel] = st
		}
		st.prices = append(st.prices, eur)
	}
	list := make([]*cpuStat, 0, len(byCPU))
	for _, st := range byCPU {
		if len(st.prices) >= 3 {
			list = append(list, st)
		}
	}
	sort.Slice(list, func(i, j int) bool { return len(list[i].prices) > len(list[j].prices) })
	if len(list) > 25 {
		list = list[:25]
	}

	fmt.Printf("=== КАКОЕ ЖЕЛЕЗО СКОЛЬКО СТОИТ (топ по числу лотов, ≥3 шт) ===\n")
	fmt.Printf("%-28s %6s %9s %9s %12s\n", "CPU", "лотов", "балл", "медиана", "€/1000б")
	for _, st := range list {
		med := median(st.prices)
		fmt.Printf("%-28s %6d %9.0f %8.0f€ %11.1f€\n",
			truncate(st.model, 28), len(st.prices), st.score, med, med/(st.score/1000))
	}

	// Лучшие сделки: дешевле медианы своей конфигурации.
	type deal struct {
		lot lot
		dev float64 // <0 — дешевле медианы
	}
	var deals []deal
	for _, l := range lots {
		if l.CPUScore == 0 {
			continue
		}
		eur := l.eur()
		if eur <= 10 || eur >= 5000 {
			continue
		}
		st := byCPU[l.CPUModel]
		if st == nil || len(st.prices) < 5 {
			continue
		}
		med := median(st.prices)
		if med <= 0 {
			continue
		}
		dev := eur/med - 1
		if dev <= -0.15 {
			deals = append(deals, deal{lot: l, dev: dev})
		}
	}
	sort.Slice(deals, func(i, j int) bool { return deals[i].dev < deals[j].dev })
	if len(deals) > topN {
		deals = deals[:topN]
	}

	fmt.Printf("\n=== ЛУЧШИЕ СДЕЛКИ: дешевле медианы своей конфигурации (≥15%%, группа ≥5 лотов) ===\n")
	for _, d := range deals {
		st := byCPU[d.lot.CPUModel]
		fmt.Printf("  %4.0f%%  %7.0f€ (медиана %4.0f€)  %-26s  %s\n",
			d.dev*100, d.lot.eur(), median(st.prices), d.lot.specLabel(), truncate(d.lot.Title, 60))
	}
	if len(deals) == 0 {
		fmt.Println("  (ничего не нашлось — возможно, рынок эффективен 🙂)")
	}
	fmt.Printf("\nПроверить конкретный лот: go run ./cmd/analyze -lot <ad_id>\n")
}

// analyzeLot — вердикт по лоту: отклонение от медианы конфигурации и
// конкретные альтернативы (дешевле то же железо; мощнее за те же деньги).
func analyzeLot(lots []lot, adID int64) {
	var target *lot
	for i := range lots {
		if lots[i].AdID == adID {
			target = &lots[i]
			break
		}
	}
	if target == nil {
		fmt.Printf("Лот %d не найден в датасете.\n", adID)
		os.Exit(1)
	}

	fmt.Printf("=== ЛОТ %d ===\n%s\nЦена: %.0f %s (%.0f€)\nЖелезо: %s\n\n",
		target.AdID, target.Title, target.Price, target.Currency, target.eur(), target.specLabel())

	if target.CPUScore == 0 {
		fmt.Println("⚠️ CPU не распознан/нет в эталоне — авто-вердикт невозможен, нужно посмотреть вручную.")
		return
	}

	// Рынок той же модели CPU.
	var sameCPU []lot
	for _, l := range lots {
		if l.CPUModel == target.CPUModel {
			eur := l.eur()
			if eur > 10 && eur < 5000 {
				sameCPU = append(sameCPU, l)
			}
		}
	}
	if len(sameCPU) < 3 {
		fmt.Printf("На рынке меньше 3 лотов с %s — сравнение ненадёжно.\n", target.CPUModel)
		return
	}
	prices := make([]float64, 0, len(sameCPU))
	for _, l := range sameCPU {
		prices = append(prices, l.eur())
	}
	med := median(prices)
	dev := target.eur()/med - 1

	fmt.Printf("РЫНОК %s: %d лотов, медиана %.0f€, диапазон %.0f–%.0f€\n",
		target.CPUModel, len(sameCPU), med, min64(prices), max64(prices))
	if dev < 0 {
		fmt.Printf("ВЕРДИКТ: ✅ дешевле медианы своей конфигурации на %.0f%%\n\n", -dev*100)
	} else {
		fmt.Printf("ВЕРДИКТ: ⚠️ дороже медианы своей конфигурации на %.0f%%\n\n", dev*100)
	}

	// 1) То же железо дешевле.
	fmt.Printf("--- То же железо дешевле ---\n")
	shown := 0
	sort.Slice(sameCPU, func(i, j int) bool { return sameCPU[i].eur() < sameCPU[j].eur() })
	for _, l := range sameCPU {
		if l.AdID == target.AdID || l.eur() >= target.eur()*0.95 {
			continue
		}
		fmt.Printf("  %6.0f€  %-50s  %s\n", l.eur(), truncate(l.Title, 50), l.URL)
		if shown++; shown == 5 {
			break
		}
	}
	if shown == 0 {
		fmt.Println("  (нет вариантов заметно дешевле)")
	}

	// 2) Мощнее за те же деньги (≥+20% баллов при цене ≤+10%).
	fmt.Printf("\n--- Мощнее за те же деньги (≥+20%% мощности, цена ≤ +10%%) ---\n")
	var stronger []lot
	for _, l := range lots {
		if l.AdID == target.AdID || l.CPUScore == 0 || l.CPUModel == target.CPUModel {
			continue
		}
		eur := l.eur()
		if eur <= 10 || eur >= 5000 {
			continue
		}
		if l.CPUScore >= target.CPUScore*1.2 && eur <= target.eur()*1.1 {
			stronger = append(stronger, l)
		}
	}
	sort.Slice(stronger, func(i, j int) bool { return stronger[i].CPUScore > stronger[j].CPUScore })
	shown = 0
	for _, l := range stronger {
		fmt.Printf("  %+.0f%% мощности  %6.0f€  %-22s  %s\n",
			(l.CPUScore/target.CPUScore-1)*100, l.eur(), truncate(l.CPUModel, 22), truncate(l.Title, 44))
		if shown++; shown == 5 {
			break
		}
	}
	if shown == 0 {
		fmt.Println("  (нет вариантов заметно мощнее за эти деньги)")
	}
}

func toEUR(price float64, currency string) float64 {
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "EUR":
		return price
	case "RSD", "DIN":
		return price / rsdToEurRate
	case "BAM", "KM":
		return price / 1.95583
	case "USD":
		return price * 0.92
	}
	return 0
}

func median(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	v := append([]float64(nil), vals...)
	sort.Float64s(v)
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return (v[n/2-1] + v[n/2]) / 2
}

func min64(v []float64) float64 {
	m := v[0]
	for _, x := range v {
		if x < m {
			m = x
		}
	}
	return m
}

func max64(v []float64) float64 {
	m := v[0]
	for _, x := range v {
		if x > m {
			m = x
		}
	}
	return m
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// pricingReport — разведочный отчёт ценового движка (PLAN_v4, Фаза 4А):
// покрытие иерархии групп, пригодность OLS-модели, распределение девиаций.
// Эти цифры — вход для калибровки порогов (веха Д), не решение само по себе.
func pricingReport(ctx context.Context, dbPath string) {
	m, err := pricing.LoadMarket(ctx, dbPath, pricing.DefaultOptions())
	if err != nil {
		fmt.Println("ценовой движок:", err)
		os.Exit(1)
	}

	fmt.Printf("=== ЦЕНОВОЙ ДВИЖОК — РАЗВЕДКА (Фаза 4А) ===\n")
	fmt.Printf("Курс RSD→EUR: %.2f (%s)\n", m.RsdEurRate(), m.RateSource())
	fmt.Printf("Строк в датасете: %d\n", len(m.Lots()))

	pool := m.Pool()
	fmt.Printf("Статистический пул (свежие ≤60д, без магазинов/хлама, CPU распознан): %d\n\n", len(pool))

	levels := map[string]int{}
	var devs []float64
	for _, l := range pool {
		est := m.EstimateFor(l)
		if est.Level == "" {
			levels["none"]++
			continue
		}
		levels[est.Level]++
		if est.Median > 0 {
			devs = append(devs, l.Price/est.Median-1)
		}
	}
	fmt.Printf("Покрытие уровней предсказания (пул %d):\n", len(pool))
	for _, lv := range []string{"K0", "K1", "K2", "K3", "none"} {
		fmt.Printf("  %-5s %5d\n", lv, levels[lv])
	}

	if len(devs) > 0 {
		sort.Float64s(devs)
		p := func(q float64) float64 { return devs[int(q*float64(len(devs)-1))] }
		fmt.Printf("\nРаспределение девиаций (цена/медиана − 1):\n")
		fmt.Printf("  p5=%.0f%%  p25=%.0f%%  p50=%.0f%%  p75=%.0f%%  p95=%.0f%%\n",
			p(.05)*100, p(.25)*100, p(.5)*100, p(.75)*100, p(.95)*100)
		fmt.Printf("  (хвост слева — кандидаты в «алмазы»; пороги калибруются на вехе Д)\n")
	}

	h := m.Hedonic()
	fmt.Printf("\nГедоническая OLS-модель:\n")
	fmt.Printf("  обучающая выборка N=%d, R²=%.3f, пригодна=%v (порог R²≥0.60)\n", h.N, h.R2, h.Usable)
	fmt.Printf("  β0=%.1f  β1(cpu/1000)=%.2f  β2(ram)=%.2f  β3(ssd/256)=%.2f  β4(gpu/1000)=%.2f\n",
		h.Beta[0], h.Beta[1], h.Beta[2], h.Beta[3], h.Beta[4])
	if !h.Usable {
		fmt.Printf("  → модель НЕ используется; работает иерархия медиан K0→K2\n")
	}
}
