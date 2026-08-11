// Package pricing — движок «цена/качество» (PLAN_v4, Фаза 4А).
//
// Два слоя предсказания рыночной цены конфигурации:
//  1. Робастные медианы по иерархии групп (K0 cpu+ram+ssd+gpu → K1 cpu+ram →
//     K2 cpu) с двухпроходным MAD-отсечением выбросов;
//  2. Гедоническая OLS-модель (Решение ③) — универсальный fallback для
//     редких конфигураций, используется только при R² ≥ 0.60.
//
// Источник данных — research.db (полный рынок). Пакет только читает.
package pricing

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	_ "modernc.org/sqlite"

	"kpbot/filters"
	"kpbot/hw"
)

// Lot — лот рынка с ценой в EUR и распознанным железом.
type Lot struct {
	AdID     int64
	Title    string
	URL      string
	Price    float64 // нормализовано в EUR
	Posted   time.Time
	Kind     string // NEW / USED / BROKEN / UNKNOWN
	IsShop   bool   // is_trader или kp_izlog
	CPUModel string
	CPUScore float64
	RAMGB    int
	SSDGB    int
	GPUModel string
	GPUScore float64
}

// Options — параметры построения рыночной модели.
type Options struct {
	MedianWindowDays  int     // окно свежести для медиан (план: 60)
	HedonicWindowDays int     // окно обучения OLS (план: 90)
	RSDEurRate        float64 // курс RSD→EUR; 0 = авто (kv-кэш → API → константа)
	DGPUOnly          bool    // PLAN_v5: пул только из лотов с дискретной графикой
}

// DefaultOptions — пороги из PLAN_v4 §3.5; курс — автоматический (§6.3).
func DefaultOptions() Options {
	return Options{
		MedianWindowDays:  60,
		HedonicWindowDays: 90,
		RSDEurRate:        0,
	}
}

// ToEUR — единая нормализация валют (PLAN_v4 §6.3: один владелец, ноль дублей).
func ToEUR(price float64, currency string, rsdRate float64) float64 {
	switch currency {
	case "EUR":
		return price
	case "RSD", "DIN":
		if rsdRate > 0 {
			return price / rsdRate
		}
		return price / 117.2
	case "BAM", "KM":
		return price / 1.95583
	case "USD":
		return price * 0.92
	}
	return 0
}

// Market — готовая к запросам модель рынка.
type Market struct {
	lots             []Lot
	groups           map[string]groupStat
	hedonic          *Hedonic
	builtAt          time.Time
	medianWindowDays int
	rsdEurRate       float64
	rateSource       string
	dgpuOnly         bool // PLAN_v5: рынок собран только по dGPU-лотам
}

// groupStat — цены группы: сырые точки (для leave-one-out), медиана и n.
type groupStat struct {
	items  []groupPrice
	median float64
	n      int
}

type groupPrice struct {
	adID  int64
	price float64
}

// minGroupN — минимальные размеры групп (PLAN_v4 §3.5).
const (
	minN_K0 = 8
	minN_K1 = 5
	minN_K2 = 5
)

// Ценовые границы статистики (PLAN_v4 §3.5, L0).
const (
	priceFloor = 10.0
	priceCeil  = 5000.0
)

// LoadMarket читает research.db и строит модель (группы + OLS).
func LoadMarket(ctx context.Context, dbPath string, opts Options) (*Market, error) {
	rate, rateSrc := opts.RSDEurRate, "config"
	if rate <= 0 {
		rate, rateSrc = ResolveRate(ctx, dbPath)
	}
	lots, err := loadLots(ctx, dbPath, rate)
	if err != nil {
		return nil, err
	}
	m := &Market{
		lots: lots, builtAt: time.Now(), medianWindowDays: opts.MedianWindowDays,
		rsdEurRate: rate, rateSource: rateSrc, dgpuOnly: opts.DGPUOnly,
	}
	m.buildGroups(opts.MedianWindowDays)
	m.hedonic = FitHedonic(m.hedonicTraining(opts.HedonicWindowDays))
	return m, nil
}

// kvSchema — таблица настроек датасета (PLAN_v4 §2.2). Владелец файла —
// research; CREATE IF NOT EXISTS безопасен для всех читателей.
const kvSchema = `CREATE TABLE IF NOT EXISTS kv (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL DEFAULT '',
	updated INTEGER NOT NULL DEFAULT 0
);`

// ResolveRate — курс RSD→EUR по приоритету: свежий kv-кэш (<24ч) →
// frankfurter.app → fallback-константа (PLAN_v4 §6.3).
func ResolveRate(ctx context.Context, dbPath string) (float64, string) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return FetchRsdEur(ctx, "")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, _ = db.ExecContext(ctx, `PRAGMA busy_timeout=5000`)
	if _, err := db.ExecContext(ctx, kvSchema); err != nil {
		return FetchRsdEur(ctx, "")
	}
	var value string
	var updated int64
	err = db.QueryRowContext(ctx, `SELECT value, updated FROM kv WHERE key='rsd_eur_rate'`).
		Scan(&value, &updated)
	if err == nil {
		if r, perr := strconv.ParseFloat(value, 64); perr == nil &&
			r >= 100 && r <= 140 && time.Since(time.Unix(updated, 0)) < 24*time.Hour {
			return r, "kv-cache"
		}
	}
	rate, src := FetchRsdEur(ctx, "")
	if src != "fallback" {
		_, _ = db.ExecContext(ctx, `
INSERT INTO kv (key, value, updated) VALUES ('rsd_eur_rate', ?, strftime('%s','now'))
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated=excluded.updated`,
			strconv.FormatFloat(rate, 'f', 4, 64))
	}
	return rate, src
}

// RsdEurRate / RateSource — курс, с которым построена модель (для отчётов).
func (m *Market) RsdEurRate() float64 { return m.rsdEurRate }
func (m *Market) RateSource() string  { return m.rateSource }

func loadLots(ctx context.Context, dbPath string, rsdRate float64) ([]Lot, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		return nil, err
	}

	recentSince := time.Now().Add(-30 * 24 * time.Hour).Unix()
	rows, err := db.QueryContext(ctx, `
SELECT a.ad_id, a.title, a.url, a.price, a.currency, a.posted, a.kind,
	a.description, a.seller, a.is_trader, a.kp_izlog, a.is_renewed,
	COALESCE(sa.ads_count,0), COALESCE(sra.recent_ads_count,0),
	COALESCE(sel.trader_seen,0), COALESCE(sel.kpizlog_seen,0),
	COALESCE(sel.reviews,0), COALESCE(sel.user_created,''),
	COALESCE(sp.cpu_model,''), COALESCE(sp.cpu_score,0), COALESCE(sp.ram_gb,0),
	COALESCE(sp.ssd_gb,0), COALESCE(sp.gpu_model,''), COALESCE(sp.gpu_score,0)
FROM research_ads a
LEFT JOIN research_specs sp ON sp.ad_id = a.ad_id
LEFT JOIN sellers sel ON sel.user_id = a.user_id
LEFT JOIN (
	SELECT user_id, COUNT(*) AS ads_count
	FROM research_ads
	WHERE user_id != 0
	GROUP BY user_id
) sa ON sa.user_id = a.user_id
LEFT JOIN (
	SELECT user_id, COUNT(*) AS recent_ads_count
	FROM research_ads
	WHERE user_id != 0 AND fetched_at >= ?
	GROUP BY user_id
) sra ON sra.user_id = a.user_id
WHERE a.fetch_status='OK'
  AND a.kind='USED'
  AND a.url != ''`, recentSince)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Lot
	for rows.Next() {
		var (
			l                                      Lot
			price                                  float64
			currency, posted, desc, seller, joined string
			isTrader, kpIzlog, isRenewed           int
			sellerAds, sellerRecentAds             int
			sellerTraderSeen, sellerKPIzlogSeen    int
			reviews                                int
		)
		if err := rows.Scan(&l.AdID, &l.Title, &l.URL, &price, &currency, &posted, &l.Kind,
			&desc, &seller, &isTrader, &kpIzlog, &isRenewed,
			&sellerAds, &sellerRecentAds, &sellerTraderSeen, &sellerKPIzlogSeen, &reviews, &joined,
			&l.CPUModel, &l.CPUScore, &l.RAMGB, &l.SSDGB,
			&l.GPUModel, &l.GPUScore); err != nil {
			return nil, err
		}
		l.Price = ToEUR(price, currency, rsdRate)
		l.IsShop = isTrader != 0 || kpIzlog != 0
		if !l.IsShop {
			v := filters.L1(filters.AdFacts{
				Title:             l.Title,
				Description:       filters.StripHTML(desc),
				Seller:            seller,
				IsTrader:          isTrader != 0,
				KPIzlog:           kpIzlog != 0,
				IsRenewed:         isRenewed != 0,
				SellerAds:         sellerAds,
				SellerRecentAds:   sellerRecentAds,
				SellerAgeDays:     sellerAgeDays(joined),
				Reviews:           reviews,
				SellerTraderSeen:  sellerTraderSeen != 0,
				SellerKPIzlogSeen: sellerKPIzlogSeen != 0,
			})
			l.IsShop = v.Class == filters.ClassShop
		}
		if posted != "" {
			if t, perr := time.Parse("2006-01-02 15:04:05", posted); perr == nil {
				l.Posted = t
			}
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func sellerAgeDays(created string) int {
	if created == "" {
		return -1
	}
	t, err := time.Parse("2006-01-02 15:04:05", created)
	if err != nil {
		return -1
	}
	return int(time.Since(t).Hours() / 24)
}

// statsPool — лоты, участвующие в рыночной статистике (PLAN_v4 §3.5):
// EUR 1–5000, свежие, без магазинов и хлама, с распознанным CPU.
func (m *Market) statsPool(windowDays int) []Lot {
	cutoff := m.builtAt.AddDate(0, 0, -windowDays)
	out := make([]Lot, 0, len(m.lots)/2)
	for _, l := range m.lots {
		if l.CPUScore <= 0 || l.IsShop || l.Kind != "USED" || l.URL == "" {
			continue
		}
		// PLAN_v5: рынок ноутбуков с дискретной графикой — без dGPU лоты
		// в статистику не входят (иначе офисные смешиваются с игровыми).
		if m.dgpuOnly && l.GPUModel == "" && l.GPUScore <= 0 {
			continue
		}
		if l.Price < priceFloor || l.Price > priceCeil {
			continue
		}
		if filters.L2(filters.AdFacts{Title: l.Title, PriceEUR: l.Price}).Class == filters.JunkPartsOnly {
			continue
		}
		if l.Posted.IsZero() || l.Posted.Before(cutoff) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// hedonicTraining — обучающая выборка OLS (PLAN_v4 §3.5): USED, частники,
// 10–5000€, cpu_score>0, возраст ≤ 90 дней.
func (m *Market) hedonicTraining(windowDays int) []Lot {
	cutoff := m.builtAt.AddDate(0, 0, -windowDays)
	var out []Lot
	for _, l := range m.lots {
		if l.Kind != "USED" || l.IsShop || l.CPUScore <= 0 || l.URL == "" {
			continue
		}
		if l.Price < priceFloor || l.Price > priceCeil {
			continue
		}
		if filters.L2(filters.AdFacts{Title: l.Title, PriceEUR: l.Price}).Class == filters.JunkPartsOnly {
			continue
		}
		if l.Posted.IsZero() || l.Posted.Before(cutoff) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// Ключи группировки (иерархия K0 → K2).

// RAMBand — типовой объём RAM: точные значения из плана, нестандартные → 0.
func RAMBand(ramGB int) int {
	switch ramGB {
	case 2, 3, 4, 6, 8, 12, 16, 24, 32, 48, 64, 96, 128:
		return ramGB
	}
	return 0
}

// SSDBand — ближайший типовой объём SSD (240→256, 480→512, 1000→1024…).
func SSDBand(ssdGB int) int {
	switch {
	case ssdGB <= 0:
		return 0
	case ssdGB < 160:
		return 128
	case ssdGB < 384:
		return 256
	case ssdGB < 768:
		return 512
	case ssdGB < 1536:
		return 1024
	default:
		return 2048
	}
}

func keyK0(l Lot) string {
	gpu := 0
	if l.GPUModel != "" {
		gpu = 1
	}
	return fmt.Sprintf("%s|%d|%d|%d", l.CPUModel, RAMBand(l.RAMGB), SSDBand(l.SSDGB), gpu)
}

func keyK1(l Lot) string { return fmt.Sprintf("%s|%d", l.CPUModel, RAMBand(l.RAMGB)) }

func keyK2(l Lot) string { return l.CPUModel }

// Ключи групп в dGPU-режиме (PLAN_v5): GPU-измерение сохраняется на ВСЕХ
// уровнях иерархии, чтобы игровой ноут не сравнивался с офисным.
// GPU нормализуется через hw.Key (единая система координат с эталоном).

func (m *Market) gpuKeyPart(l Lot) string {
	if l.GPUModel != "" {
		return hw.Key(l.GPUModel)
	}
	return "?"
}

func (m *Market) groupKeyK0(l Lot) string {
	if m.dgpuOnly {
		return fmt.Sprintf("%s|%d|%d|%s", l.CPUModel, RAMBand(l.RAMGB), SSDBand(l.SSDGB), m.gpuKeyPart(l))
	}
	return keyK0(l)
}

func (m *Market) groupKeyK1(l Lot) string {
	if m.dgpuOnly {
		return fmt.Sprintf("%s|%d|%s", l.CPUModel, RAMBand(l.RAMGB), m.gpuKeyPart(l))
	}
	return keyK1(l)
}

func (m *Market) groupKeyK2(l Lot) string {
	if m.dgpuOnly {
		return l.CPUModel + "|" + m.gpuKeyPart(l)
	}
	return keyK2(l)
}

func (m *Market) buildGroups(windowDays int) {
	pool := m.statsPool(windowDays)
	items := map[string][]groupPrice{}
	for _, l := range pool {
		it := groupPrice{adID: l.AdID, price: l.Price}
		items[m.groupKeyK0(l)] = append(items[m.groupKeyK0(l)], it)
		items[m.groupKeyK1(l)] = append(items[m.groupKeyK1(l)], it)
		items[m.groupKeyK2(l)] = append(items[m.groupKeyK2(l)], it)
	}
	m.groups = make(map[string]groupStat, len(items))
	for key, its := range items {
		med, n := robustMedian(pricesOf(its))
		m.groups[key] = groupStat{items: its, median: med, n: n}
	}
}

func pricesOf(items []groupPrice) []float64 {
	out := make([]float64, len(items))
	for i, it := range items {
		out[i] = it.price
	}
	return out
}

// robustMedian — двухпроходная робастная медиана (PLAN_v4 §3.5):
// медиана → MAD → отсечение точек за ±3σ̂ → медиана очищенного.
func robustMedian(vals []float64) (float64, int) {
	if len(vals) == 0 {
		return 0, 0
	}
	m1 := median(vals)
	mad := medianAbsDev(vals, m1)
	if mad == 0 {
		return m1, len(vals) // вырожденный случай: все точки одинаковые
	}
	sigma := 1.4826 * mad
	kept := make([]float64, 0, len(vals))
	for _, v := range vals {
		if math.Abs(v-m1) <= 3*sigma {
			kept = append(kept, v)
		}
	}
	if len(kept) == 0 {
		return m1, len(vals)
	}
	return median(kept), len(kept)
}

func median(vals []float64) float64 {
	v := append([]float64(nil), vals...)
	sort.Float64s(v)
	n := len(v)
	switch {
	case n == 0:
		return 0
	case n%2 == 1:
		return v[n/2]
	default:
		return (v[n/2-1] + v[n/2]) / 2
	}
}

func percentile(vals []float64, q float64) float64 {
	v := append([]float64(nil), vals...)
	sort.Float64s(v)
	n := len(v)
	if n == 0 {
		return 0
	}
	if q <= 0 {
		return v[0]
	}
	if q >= 1 {
		return v[n-1]
	}
	pos := q * float64(n-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return v[lo]
	}
	return v[lo] + (v[hi]-v[lo])*(pos-float64(lo))
}

func medianAbsDev(vals []float64, med float64) float64 {
	devs := make([]float64, len(vals))
	for i, v := range vals {
		devs[i] = math.Abs(v - med)
	}
	return median(devs)
}

// PriceEstimate — предсказание рыночной цены для лота по иерархии
// K0 → K1 → K2 → гедоническая модель. Level: K0/K1/K2/K3/"".
type PriceEstimate struct {
	P25            float64 // comparable 25th percentile when a group backs the estimate
	Median         float64 // рыночный ориентир после конкурентного потолка
	RawMedian      float64 // сырая медиана группы или предсказание OLS до потолка
	N              int     // размер опорной выборки
	Level          string  // K0/K1/K2/K3; пусто — предсказать нечем
	CompetitiveCap float64 // цена более мощного конкурента, если он ограничил ориентир
	CapLot         *Lot    // лот, поставивший конкурентный потолок
}

// EstimateFor — рыночная цена конфигурации лота.
func (m *Market) EstimateFor(l Lot) PriceEstimate {
	return m.withCompetitiveCap(l, m.estimateFor(l, false))
}

func (e PriceEstimate) Capped() bool {
	return e.RawMedian > 0 && e.CompetitiveCap > 0 && e.CompetitiveCap < e.RawMedian && e.CapLot != nil
}

// MarketEvaluation keeps comparable market stats separate from stronger-lot ceilings.
type MarketEvaluation struct {
	Estimate           PriceEstimate
	ComparableMedian   float64
	ComparableP25      float64
	OpportunityCeiling float64
	OpportunityBy      *Lot
	DominatedBy        *Lot
	StepUp             *Lot
	Confidence         string
	Deviation          float64
	DevOK              bool
}

func (m *Market) Evaluate(l Lot) MarketEvaluation {
	raw := m.estimateFor(l, true)
	est := m.withCompetitiveCap(l, raw)
	ev := MarketEvaluation{
		Estimate:         est,
		ComparableMedian: raw.Median,
		ComparableP25:    raw.P25,
		Confidence:       confidenceFor(est),
	}
	if est.Median > 0 {
		ev.Deviation = l.Price/est.Median - 1
		ev.DevOK = true
		if capLot, ok := m.competitiveCapLot(l, est.Median); ok {
			ev.OpportunityCeiling = capLot.Price
			ev.OpportunityBy = &capLot
		}
	}
	if dom, ok := m.dominanceLot(l); ok {
		ev.DominatedBy = &dom
		if ev.OpportunityCeiling <= 0 || dom.Price < ev.OpportunityCeiling {
			ev.OpportunityCeiling = dom.Price
			ev.OpportunityBy = &dom
		}
	}
	if steps := m.BestStepUp(l, m.marketWindowDays(), 1); len(steps) > 0 && steps[0].URL != "" {
		step := steps[0]
		ev.StepUp = &step
	}
	return ev
}

func confidenceFor(est PriceEstimate) string {
	switch {
	case est.Level == "K0" && est.N >= minN_K0:
		return "HIGH"
	case (est.Level == "K1" || est.Level == "K2") && est.N >= minN_K1:
		return "MEDIUM"
	case est.Level == "K3" && est.N > 0:
		return "LOW"
	default:
		return "NONE"
	}
}

func (m *Market) estimateFor(l Lot, leaveOneOut bool) PriceEstimate {
	if l.CPUScore <= 0 {
		return PriceEstimate{}
	}
	for _, c := range []struct {
		key   string
		level string
		minN  int
	}{
		{m.groupKeyK0(l), "K0", minN_K0},
		{m.groupKeyK1(l), "K1", minN_K1},
		{m.groupKeyK2(l), "K2", minN_K2},
	} {
		if g, ok := m.groups[c.key]; ok && g.n >= c.minN {
			prices := pricesOf(g.items)
			if leaveOneOut {
				prices = pricesExcluding(g.items, l.AdID)
			}
			if len(prices) < c.minN {
				continue
			}
			med, n := robustMedian(prices)
			if med <= 0 || n < c.minN {
				continue
			}
			return PriceEstimate{Median: med, RawMedian: med, P25: percentile(prices, 0.25), N: n, Level: c.level}
		}
	}
	if m.hedonic != nil && m.hedonic.Usable {
		pred := m.hedonic.Predict(l)
		if pred <= 0 {
			return PriceEstimate{}
		}
		return PriceEstimate{Median: pred, RawMedian: pred, N: m.hedonic.N, Level: "K3"}
	}
	return PriceEstimate{}
}

func pricesExcluding(items []groupPrice, adID int64) []float64 {
	out := make([]float64, 0, len(items))
	for _, it := range items {
		if it.adID == adID {
			continue
		}
		out = append(out, it.price)
	}
	return out
}

const competitiveCapStrongerPct = 0.20

const (
	stepUpPowerGainMinMarket  = 0.50
	stepUpValueRatioMinMarket = 1.25
	maxMarginalEURPer1000     = 2.0
)

func (m *Market) marketWindowDays() int {
	if m.medianWindowDays > 0 {
		return m.medianWindowDays
	}
	return 60
}

// withCompetitiveCap не даёт медиане слабой конфигурации оторваться от живого
// рынка: если в том же свежем частном пуле есть существенно более мощный лот
// дешевле сырого ориентира, он становится верхней границей оценки.
func (m *Market) withCompetitiveCap(target Lot, est PriceEstimate) PriceEstimate {
	if est.Median <= 0 || target.Composite() <= 0 {
		return est
	}
	capLot, ok := m.competitiveCapLot(target, est.Median)
	if !ok {
		return est
	}
	est.CompetitiveCap = capLot.Price
	est.CapLot = &capLot
	est.Median = capLot.Price
	return est
}

func (m *Market) competitiveCapLot(target Lot, maxPrice float64) (Lot, bool) {
	if target.Composite() <= 0 || maxPrice <= 0 {
		return Lot{}, false
	}
	var (
		best Lot
		ok   bool
	)
	for _, l := range m.candidates(m.marketWindowDays()) {
		if l.AdID == target.AdID || l.Price <= 0 || l.Price >= maxPrice {
			continue
		}
		if !outclassesValue(target, l) {
			continue
		}
		if !ok || l.Price < best.Price || (l.Price == best.Price && l.Composite() > best.Composite()) {
			best, ok = l, true
		}
	}
	return best, ok
}

// Deviation — отклонение цены лота от рыночной: price/median − 1.
// Отрицательное = дешевле рынка.
//
// Leave-one-out: если лот сам входит в датасет, его цена исключается из
// медианы своей группы — иначе при малых n лот сам себе искажает
// отклонение (особенно в «алмазном» пороге −15%). Порог размера группы
// проверяется ПОСЛЕ исключения (n−1): сравнивать не с чем — уровень
// понижается по иерархии. Для лотов вне датасета (живой бот) медиана
// берётся целиком. NaN-защита: без оценки возвращает 0 и false.
func (m *Market) dominanceLot(target Lot) (Lot, bool) {
	if target.Composite() <= 0 || target.Price <= 0 {
		return Lot{}, false
	}
	var (
		best Lot
		ok   bool
	)
	for _, l := range m.candidates(m.marketWindowDays()) {
		if l.AdID == target.AdID || l.Price <= 0 {
			continue
		}
		if !outclassesValue(target, l) {
			continue
		}
		if !ok || l.Price < best.Price || (l.Price == best.Price && l.ValuePer1000() > best.ValuePer1000()) {
			best, ok = l, true
		}
	}
	return best, ok
}

func outclassesValue(target, candidate Lot) bool {
	if target.Price <= 0 || candidate.Price <= 0 {
		return false
	}
	if candidate.CPUScore <= 0 || target.CPUScore <= 0 || candidate.CPUScore < target.CPUScore {
		return false
	}
	if target.GPUScore > 0 && candidate.GPUScore < target.GPUScore {
		return false
	}
	if target.RAMGB > 0 && candidate.RAMGB < target.RAMGB {
		return false
	}
	if target.SSDGB > 0 && candidate.SSDGB < target.SSDGB {
		return false
	}
	targetComp := target.Composite()
	candComp := candidate.Composite()
	if targetComp <= 0 || candComp <= targetComp {
		return false
	}
	powerGain := (candComp - targetComp) / targetComp
	valueRatio := candidate.ValuePer1000() / target.ValuePer1000()
	marginalEURPer1000 := (candidate.Price - target.Price) / ((candComp - targetComp) / 1000)
	if candidate.Price <= target.Price {
		return powerGain >= competitiveCapStrongerPct
	}
	return powerGain >= stepUpPowerGainMinMarket && (valueRatio >= stepUpValueRatioMinMarket || marginalEURPer1000 <= maxMarginalEURPer1000)
}

func (m *Market) Deviation(l Lot) (float64, bool) {
	ev := m.Evaluate(l)
	return ev.Deviation, ev.DevOK
}

// Lots — все загруженные лоты (для альтернатив и отчётов).
func (m *Market) Lots() []Lot { return m.lots }

// Composite — суммарная мощность ноутбука по бенчмаркам (CPU+GPU).
// PLAN_v6: единая метрика «мощнее/слабее» — игровой ноут с сильным GPU не
// бракуется из-за CPU двух поколений назад, и наоборот.
func (l Lot) Composite() float64 { return l.CPUScore + l.GPUScore }

// ValuePer1000 — баллов на €1000 цены: чем выше, тем выгоднее лот.
// PLAN_v6: показывается в алерте цифрами, без прилагательных.
func (l Lot) ValuePer1000() float64 {
	if l.Price <= 0 {
		return 0
	}
	return l.Composite() / l.Price * 1000
}

// CPUGroup — сводка по модели CPU для отчётов (топ рынка).
type CPUGroup struct {
	Model  string
	Score  float64
	N      int
	Median float64
}

// TopCPUGroups — топ моделей CPU по числу лотов в статистическом пуле
// (группа K2), только с достаточным n (≥3) — для пульта и отчётов.
func (m *Market) TopCPUGroups(limit int) []CPUGroup {
	type acc struct {
		score  float64
		prices []float64
	}
	byCPU := map[string]*acc{}
	for _, l := range m.statsPool(m.medianWindowDays) {
		a := byCPU[l.CPUModel]
		if a == nil {
			a = &acc{score: l.CPUScore}
			byCPU[l.CPUModel] = a
		}
		a.prices = append(a.prices, l.Price)
	}
	out := make([]CPUGroup, 0, len(byCPU))
	for model, a := range byCPU {
		if len(a.prices) < 3 {
			continue
		}
		med, _ := robustMedian(a.prices)
		out = append(out, CPUGroup{Model: model, Score: a.score, N: len(a.prices), Median: med})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Model < out[j].Model
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Pool — лоты, питающие медианы (свежие, без магазинов/хлама, с CPU):
// нужно для отчётов и калибровки.
func (m *Market) Pool() []Lot { return m.statsPool(m.medianWindowDays) }

// Hedonic — fitted OLS-модель (или nil).
func (m *Market) Hedonic() *Hedonic { return m.hedonic }
