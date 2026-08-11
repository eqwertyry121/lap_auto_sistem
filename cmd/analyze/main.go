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
	GPUScore float64
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

func (l lot) pricingLot() pricing.Lot {
	return pricing.Lot{
		AdID: l.AdID, Title: l.Title, URL: l.URL, Price: l.eur(), Kind: l.Kind,
		CPUModel: l.CPUModel, CPUScore: l.CPUScore,
		RAMGB: l.RAMGB, SSDGB: l.SSDGB,
		GPUModel: l.GPUModel, GPUScore: l.GPUScore,
	}
}

func pricingSpecLabel(l pricing.Lot) string {
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
	market, err := pricing.LoadMarket(ctx, *dbPath, pricing.DefaultOptions())
	if err != nil {
		fmt.Println("рынок:", err)
		os.Exit(1)
	}

	if *lotID > 0 {
		analyzeLot(lots, market, *lotID)
		return
	}
	fullReport(lots, market, *topN)
}

func loadLots(ctx context.Context, db *sql.DB) ([]lot, error) {
	rows, err := db.QueryContext(ctx, `
SELECT a.ad_id, a.title, a.price, a.currency, a.url, a.kind,
	COALESCE(s.cpu_model,''), COALESCE(s.cpu_score,0), COALESCE(s.ram_gb,0),
	COALESCE(s.ssd_gb,0), COALESCE(s.gpu_model,''), COALESCE(s.gpu_score,0)
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
			&l.CPUModel, &l.CPUScore, &l.RAMGB, &l.SSDGB, &l.GPUModel, &l.GPUScore); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func fullReport(lots []lot, market *pricing.Market, topN int) {
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
	for _, l := range market.Pool() {
		if l.CPUScore == 0 {
			continue
		}
		eur := l.Price
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

	// Лучшие сделки: только тот же clean pool и тот же pricing.Evaluate, что в funnel.
	type deal struct {
		lot pricing.Lot
		ev  pricing.MarketEvaluation
	}
	var deals []deal
	for _, l := range market.Pool() {
		ev := market.Evaluate(l)
		if ev.DevOK && ev.Deviation <= -0.15 && ev.DominatedBy == nil {
			deals = append(deals, deal{lot: l, ev: ev})
		}
	}
	sort.Slice(deals, func(i, j int) bool { return deals[i].ev.Deviation < deals[j].ev.Deviation })
	if len(deals) > topN {
		deals = deals[:topN]
	}

	fmt.Printf("\n=== ЛУЧШИЕ СДЕЛКИ: дешевле comparable-медианы по Market.Evaluate (≥15%%, без dominance) ===\n")
	for _, d := range deals {
		fmt.Printf("  %4.0f%%  %7.0f€ (comparable %4.0f€, n=%d %s)  %-26s  %s\n",
			d.ev.Deviation*100, d.lot.Price, d.ev.ComparableMedian, d.ev.Estimate.N, d.ev.Estimate.Level,
			pricingSpecLabel(d.lot), truncate(d.lot.Title, 60))
	}
	if len(deals) == 0 {
		fmt.Println("  (ничего не нашлось — возможно, рынок эффективен 🙂)")
	}
	fmt.Printf("\nПроверить конкретный лот: go run ./cmd/analyze -lot <ad_id>\n")
}

// analyzeLot — вердикт по лоту: отклонение от медианы конфигурации и
// конкретные альтернативы (дешевле то же железо; мощнее за те же деньги).
func analyzeLot(lots []lot, market *pricing.Market, adID int64) {
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

	pl := target.pricingLot()
	ev := market.Evaluate(pl)
	if !ev.DevOK {
		fmt.Println("РЫНОК: в clean market pool нет достаточной сравнимой группы — авто-вердикт ненадёжен.")
		return
	}
	fmt.Printf("РЫНОК: comparable-медиана %.0f€ · p25 %.0f€ · n=%d · уровень %s · confidence=%s\n",
		ev.ComparableMedian, ev.ComparableP25, ev.Estimate.N, ev.Estimate.Level, ev.Confidence)
	fmt.Printf("Отклонение цены: %.0f%%\n", ev.Deviation*100)
	if ev.DominatedBy != nil && ev.DominatedBy.URL != "" {
		fmt.Printf("DOMINANCE: максимум CHECK — есть более мощный clean private лот %.0f€ · индекс %.0f\n  %s\n  %s\n\n",
			ev.DominatedBy.Price, ev.DominatedBy.Composite(), truncate(ev.DominatedBy.Title, 70), ev.DominatedBy.URL)
	} else if ev.Deviation <= -0.15 {
		fmt.Printf("ВЕРДИКТ: кандидат дешевле comparable-медианы на %.0f%%\n\n", -ev.Deviation*100)
	} else if ev.Deviation > 0 {
		fmt.Printf("ВЕРДИКТ: дороже comparable-медианы на %.0f%%\n\n", ev.Deviation*100)
	} else {
		fmt.Printf("ВЕРДИКТ: около рынка\n\n")
	}

	// 1) То же железо дешевле: только clean private USED pool с URL.
	fmt.Printf("--- То же железо дешевле ---\n")
	shown := 0
	for _, l := range market.CheaperSameCPU(pl, 60, 5) {
		fmt.Printf("  %6.0f€  %-50s  %s\n", l.Price, truncate(l.Title, 50), l.URL)
		shown++
	}
	if shown == 0 {
		fmt.Println("  (нет вариантов заметно дешевле)")
	}

	// 2) Мощнее за те же деньги (>=+20% индекса при цене <=+10%).
	fmt.Printf("\n--- Мощнее за те же деньги (≥+20%% индекса, цена ≤ +10%%) ---\n")
	shown = 0
	for _, l := range market.StrongerForBudget(pl, 60, 5, 1.1) {
		fmt.Printf("  %+.0f%% index  %6.0f€  %-22s  %s\n",
			(l.Composite()/pl.Composite()-1)*100, l.Price, truncate(l.CPUModel, 22), l.URL)
		shown++
	}
	if shown == 0 {
		fmt.Println("  (нет вариантов заметно мощнее за эти деньги)")
	}
	if ev.StepUp != nil && ev.StepUp.URL != "" {
		fmt.Printf("\n--- Шаг вверх ---\n")
		fmt.Printf("  %.0f€ (+%.0f€), +%.0f индекса: %s\n  %s\n",
			ev.StepUp.Price, ev.StepUp.Price-pl.Price, ev.StepUp.Composite()-pl.Composite(),
			truncate(ev.StepUp.Title, 70), ev.StepUp.URL)
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
		ev := m.Evaluate(l)
		est := ev.Estimate
		if est.Level == "" {
			levels["none"]++
			continue
		}
		levels[est.Level]++
		if ev.DevOK {
			devs = append(devs, ev.Deviation)
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
