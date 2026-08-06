package pricing

import (
	"math"
	"testing"
	"time"
)

// ---------- валюта ----------

func TestToEUR(t *testing.T) {
	cases := []struct {
		price float64
		cur   string
		want  float64
	}{
		{100, "EUR", 100},
		{11720, "RSD", 100},
		{11720, "DIN", 100},
		{195.583, "BAM", 100},
		{100, "USD", 92},
		{100, "GBP", 0}, // неизвестная валюта → 0 (не нормализована)
	}
	for _, c := range cases {
		got := ToEUR(c.price, c.cur, 117.2)
		if math.Abs(got-c.want) > 0.01 {
			t.Errorf("ToEUR(%.2f %s) = %.2f, want %.2f", c.price, c.cur, got, c.want)
		}
	}
}

// ---------- полосы RAM/SSD ----------

func TestBands(t *testing.T) {
	if RAMBand(16) != 16 || RAMBand(13) != 0 || RAMBand(0) != 0 {
		t.Error("RAMBand работает неверно")
	}
	cases := []struct{ in, want int }{
		{120, 128}, {128, 128}, {240, 256}, {256, 256}, {480, 512},
		{512, 512}, {800, 1024}, {1000, 1024}, {2000, 2048}, {0, 0},
	}
	for _, c := range cases {
		if got := SSDBand(c.in); got != c.want {
			t.Errorf("SSDBand(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// ---------- робастная медиана ----------

func TestRobustMedianRejectsOutliers(t *testing.T) {
	// 10 точек около 300€ и две дикие: 1€ (шутка) и 4000€ (мультилистинг).
	vals := []float64{280, 290, 300, 300, 305, 310, 315, 320, 295, 285, 1, 4000}
	med, n := robustMedian(vals)
	if med < 290 || med > 315 {
		t.Errorf("медиана %.0f не устойчива к выбросам (жду ~300)", med)
	}
	if n >= len(vals) {
		t.Errorf("выбросы не отсечены: n=%d из %d", n, len(vals))
	}
}

func TestRobustMedianDegenerate(t *testing.T) {
	// Все точки одинаковые (MAD=0) — медиана равна точке, ничего не отброшено.
	med, n := robustMedian([]float64{250, 250, 250, 250})
	if med != 250 || n != 4 {
		t.Errorf("вырожденный случай: med=%.0f n=%d, want 250/4", med, n)
	}
	if med, n := robustMedian(nil); med != 0 || n != 0 {
		t.Error("пустой вход должен давать 0/0")
	}
}

// ---------- иерархия групп ----------

// marketWith — синтетический рынок без SQLite: лоты уже «загружены».
func marketWith(t *testing.T, lots []Lot, windowDays int) *Market {
	t.Helper()
	m := &Market{lots: lots, builtAt: time.Now()}
	m.buildGroups(windowDays)
	return m
}

func mkLot(adID int64, cpu string, score float64, ram, ssd int, price float64, daysAgo int) Lot {
	return Lot{
		AdID: adID, Title: "t", Price: price, Kind: "USED",
		CPUModel: cpu, CPUScore: score, RAMGB: ram, SSDGB: ssd,
		Posted: time.Now().AddDate(0, 0, -daysAgo),
	}
}

func TestEstimateHierarchyFallback(t *testing.T) {
	var lots []Lot
	// 8 лотов одной ТОЧНОЙ конфигурации (K0 проходит порог n≥8)
	for i := 0; i < 8; i++ {
		lots = append(lots, mkLot(int64(i+1), "i5-1135G7", 10000, 16, 512, 300+float64(i), 5))
	}
	// 5 лотов той же CPU, но другой RAM (K1 для них: всего 5)
	for i := 0; i < 5; i++ {
		lots = append(lots, mkLot(int64(100+i), "i5-1135G7", 10000, 8, 256, 250+float64(i), 5))
	}
	m := marketWith(t, lots, 60)

	// Точная конфигурация → K0.
	est := m.EstimateFor(mkLot(999, "i5-1135G7", 10000, 16, 512, 300, 1))
	if est.Level != "K0" {
		t.Errorf("жду K0, got %q", est.Level)
	}

	// Редкая конфигурация той же CPU: K0 нет, K1 (cpu+ram=8GB) n=5 → K1.
	est = m.EstimateFor(mkLot(998, "i5-1135G7", 10000, 8, 999, 250, 1))
	if est.Level != "K1" {
		t.Errorf("жду K1, got %q", est.Level)
	}

	// RAM-полоса 0 (нестандартный 13GB): K0/K1 мимо, K2 (cpu) n=13 → K2.
	est = m.EstimateFor(mkLot(997, "i5-1135G7", 10000, 13, 512, 280, 1))
	if est.Level != "K2" {
		t.Errorf("жду K2, got %q", est.Level)
	}

	// Неизвестная CPU без гедоники → пусто.
	est = m.EstimateFor(mkLot(996, "unknown-cpu", 5000, 16, 512, 300, 1))
	if est.Level != "" {
		t.Errorf("жду пустой уровень, got %q", est.Level)
	}
}

func TestShopsAndBrokenExcluded(t *testing.T) {
	var lots []Lot
	for i := 0; i < 8; i++ {
		l := mkLot(int64(i+1), "i5-1135G7", 10000, 16, 512, 300, 5)
		if i < 4 {
			l.IsShop = true
		}
		lots = append(lots, l)
	}
	lots = append(lots, func() Lot {
		l := mkLot(50, "i5-1135G7", 10000, 16, 512, 300, 5)
		l.Kind = "BROKEN"
		return l
	}())
	m := marketWith(t, lots, 60)
	// Частных свежих лотов только 4 → K0 не проходит (n≥8), K1/K2 тоже n=4 < 5.
	est := m.EstimateFor(mkLot(999, "i5-1135G7", 10000, 16, 512, 300, 1))
	if est.Level != "" {
		t.Errorf("магазины/хлам должны быть исключены: got %q n=%d", est.Level, est.N)
	}
}

func TestStaleLotsExcluded(t *testing.T) {
	var lots []Lot
	for i := 0; i < 8; i++ {
		lots = append(lots, mkLot(int64(i+1), "i5-1135G7", 10000, 16, 512, 300, 120)) // старше 60 дней
	}
	m := marketWith(t, lots, 60)
	est := m.EstimateFor(mkLot(999, "i5-1135G7", 10000, 16, 512, 300, 1))
	if est.Level != "" {
		t.Errorf("старые лоты не должны питать медианы: got %q", est.Level)
	}
}

// ---------- OLS ----------

// TestHedonicRecoversLinear — на идеально линейных данных модель обязана
// вернуть β исходного процесса и R²≈1.
func TestHedonicRecoversLinear(t *testing.T) {
	var lots []Lot
	for i := 0; i < 200; i++ {
		score := 2000 + float64(i%50)*300 // 2000..16700
		ram := 4 + (i%5)*4                // 4..20
		ssd := 128 + (i%4)*128            // 128..512
		gpuScore := 0.0
		if i%3 == 0 {
			gpuScore = 5000
		}
		// Истинная зависимость: price = 50 + 0.04·score + 3·ram + 40·(ssd/256) + 0.02·gpu
		price := 50 + 0.04*score + 3*float64(ram) + 40*float64(ssd)/256 + 0.02*gpuScore
		l := Lot{
			AdID: int64(i + 1), Kind: "USED",
			CPUScore: score, RAMGB: ram, SSDGB: ssd, GPUScore: gpuScore,
			Price: price, Posted: time.Now(),
		}
		lots = append(lots, l)
	}
	h := FitHedonic(lots)
	if !h.Usable || h.R2 < 0.999 {
		t.Fatalf("на линейных данных R²=%.4f usable=%v, жду ≈1/true", h.R2, h.Usable)
	}
	// features() нормализует: score/1000 и gpu/1000 — коэффициенты при них
	// в 1000 раз больше «физических».
	want := [5]float64{50, 40, 3, 40, 20}
	for i := range want {
		if math.Abs(h.Beta[i]-want[i]) > 1e-6 {
			t.Errorf("β%d = %.6f, want %.6f", i, h.Beta[i], want[i])
		}
	}

	// Предсказание для новой конфигурации.
	probe := Lot{CPUScore: 10000, RAMGB: 16, SSDGB: 512, GPUScore: 0}
	got := h.Predict(probe)
	wantPrice := 50 + 0.04*10000 + 3*16 + 40*512.0/256
	if math.Abs(got-wantPrice) > 1e-6 {
		t.Errorf("Predict = %.4f, want %.4f", got, wantPrice)
	}
}

func TestHedonicTooFewPoints(t *testing.T) {
	lots := []Lot{
		{AdID: 1, Kind: "USED", CPUScore: 10000, RAMGB: 16, SSDGB: 512, Price: 300, Posted: time.Now()},
		{AdID: 2, Kind: "USED", CPUScore: 12000, RAMGB: 16, SSDGB: 512, Price: 350, Posted: time.Now()},
	}
	h := FitHedonic(lots)
	if h.Usable {
		t.Error("на 2 точках модель не должна считаться пригодной")
	}
}

func TestHedonicNoisyButUsable(t *testing.T) {
	// Сильный шум, но тренд есть: R² умеренный, модель честна о себе.
	// RAM/SSD обязаны варьироваться (константы дают точную коллинеарность
	// со свободным членом — см. TestHedonicCollinearGraceful). Период шума
	// (17) не кратен периодам признаков (2 и 3) — иначе OLS поглотит шум.
	var lots []Lot
	for i := 0; i < 100; i++ {
		score := 3000 + float64(i)*100
		noise := float64((i*37)%17-8) * 25 // ±200€
		lots = append(lots, Lot{
			AdID: int64(i + 1), Kind: "USED",
			CPUScore: score, RAMGB: 8 + (i%3)*8, SSDGB: 256 + (i%2)*256,
			Price: 100 + 0.03*score + noise, Posted: time.Now(),
		})
	}
	h := FitHedonic(lots)
	if h.R2 <= 0 || h.R2 >= 1 {
		t.Errorf("R²=%.4f вне осмысленного диапазона", h.R2)
	}
	// β1 должен быть положительным (дороже баллы → дороже ноутбук).
	if h.Beta[1] <= 0 {
		t.Errorf("β1 = %.4f, жду положительный", h.Beta[1])
	}
}

func TestHedonicDropsZeroVarianceFeatures(t *testing.T) {
	// RAM/SSD/GPU постоянны во всей выборке — модель обязана исключить их
	// (β=0) и остаться пригодной, а не деградировать в Usable=false.
	var lots []Lot
	for i := 0; i < 50; i++ {
		score := 3000 + float64(i)*100
		lots = append(lots, Lot{
			AdID: int64(i + 1), Kind: "USED",
			CPUScore: score, RAMGB: 16, SSDGB: 512,
			Price: 100 + 0.03*score, Posted: time.Now(),
		})
	}
	h := FitHedonic(lots)
	if !h.Usable {
		t.Fatalf("модель должна остаться пригодной: R²=%.4f", h.R2)
	}
	for _, j := range []int{2, 3, 4} {
		if h.Beta[j] != 0 {
			t.Errorf("β%d = %.4f, жду 0 (признак исключён)", j, h.Beta[j])
		}
	}
}

func TestHedonicCollinearGraceful(t *testing.T) {
	// Точная коллинеарность АКТИВНЫХ признаков: ssd/256 = 2·(score/1000).
	// Модель обязана честно сказать «непригодна», а не выдать мусор.
	var lots []Lot
	for i := 0; i < 40; i++ {
		score := 5000 + float64(i)*500
		lots = append(lots, Lot{
			AdID: int64(i + 1), Kind: "USED",
			CPUScore: score,
			RAMGB:    8 + (i%3)*8,          // варьируется
			SSDGB:    int(score/500) * 256, // точно ssd/256 = 2·(score/1000)
			GPUScore: float64(i%2) * 3000,  // варьируется
			Price:    100 + 0.03*score,
			Posted:   time.Now(),
		})
	}
	h := FitHedonic(lots)
	if h.Usable {
		t.Error("при точной коллинеарности модель не должна считаться пригодной")
	}
}

// ---------- девиация и альтернативы ----------

func TestDeviation(t *testing.T) {
	var lots []Lot
	for i := 0; i < 8; i++ {
		lots = append(lots, mkLot(int64(i+1), "i5-1135G7", 10000, 16, 512, 300, 5))
	}
	m := marketWith(t, lots, 60)

	dev, ok := m.Deviation(mkLot(999, "i5-1135G7", 10000, 16, 512, 240, 1))
	if !ok || dev >= -0.19 || dev <= -0.21 {
		t.Errorf("dev = %.3f (ok=%v), жду ≈ −0.20", dev, ok)
	}
	if _, ok := m.Deviation(mkLot(998, "no-cpu", 0, 16, 512, 240, 1)); ok {
		t.Error("без CPU девиации быть не должно")
	}
}

func TestDeviationLeaveOneOut(t *testing.T) {
	// 6 лотов одной конфигурации; целевой (150€) сам входит в группу.
	// Без leave-one-out медиана группы (150+200)/2=175 → dev −14.3%;
	// с leave-one-out медиана остальных пяти 200 → dev −25%.
	lots := []Lot{
		mkLot(1, "i5-1135G7", 10000, 16, 512, 100, 5),
		mkLot(2, "i5-1135G7", 10000, 16, 512, 100, 5),
		mkLot(3, "i5-1135G7", 10000, 16, 512, 150, 5),
		mkLot(4, "i5-1135G7", 10000, 16, 512, 200, 5),
		mkLot(5, "i5-1135G7", 10000, 16, 512, 300, 5),
		mkLot(6, "i5-1135G7", 10000, 16, 512, 300, 5),
	}
	m := marketWith(t, lots, 60)

	dev, ok := m.Deviation(lots[2]) // 150€, в датасете
	if !ok || math.Abs(dev-(-0.25)) > 1e-9 {
		t.Fatalf("leave-one-out: dev=%.4f ok=%v, жду −0.25", dev, ok)
	}

	// Внешний лот той же конфигурации: медиана берётся целиком (175).
	external := mkLot(999, "i5-1135G7", 10000, 16, 512, 150, 1)
	dev, ok = m.Deviation(external)
	want := 150.0/175.0 - 1
	if !ok || math.Abs(dev-want) > 1e-9 {
		t.Fatalf("внешний лот: dev=%.4f ok=%v, жду %.4f", dev, ok, want)
	}
}

func TestDeviationLeaveOneOutDropsLevel(t *testing.T) {
	// K2-группа ровно из 5 лотов (RAM различается → K0/K1 ниже порогов).
	// Целевой лот в группе: после исключения остаётся 4 < 5 — сравнение
	// недостоверно, оценка невозможна (без гедоники).
	lots := []Lot{
		mkLot(1, "i5-1135G7", 10000, 8, 256, 200, 5),
		mkLot(2, "i5-1135G7", 10000, 16, 256, 250, 5),
		mkLot(3, "i5-1135G7", 10000, 16, 512, 300, 5),
		mkLot(4, "i5-1135G7", 10000, 32, 512, 350, 5),
		mkLot(5, "i5-1135G7", 10000, 32, 1024, 400, 5),
	}
	m := marketWith(t, lots, 60)
	if _, ok := m.Deviation(lots[2]); ok {
		t.Fatal("после leave-one-out группа ниже порога: оценка должна быть невозможна")
	}
	// Внешний лот: K2 n=5 ≥ порога → оценка есть.
	if _, ok := m.Deviation(mkLot(999, "i5-1135G7", 10000, 16, 512, 300, 1)); !ok {
		t.Fatal("внешний лот должен получить оценку по K2")
	}
}

func TestAlternatives(t *testing.T) {
	target := mkLot(1, "i5-1135G7", 10000, 16, 512, 300, 1)
	lots := []Lot{
		target,
		mkLot(2, "i5-1135G7", 10000, 16, 512, 270, 1), // A1: то же железо дешевле
		mkLot(3, "i5-1135G7", 10000, 16, 512, 295, 1), // слишком близко (−1.7%)
		mkLot(4, "i7-11800H", 15000, 16, 512, 320, 1), // A2: мощнее (+50%), ≤ +10% цены
		mkLot(5, "i7-11800H", 15000, 16, 512, 500, 1), // A2 мимо: дорого
		mkLot(6, "ryzen-5-4600h", 11000, 16, 512, 310, 1),
	}
	m := marketWith(t, lots, 60)

	a1 := m.CheaperSameCPU(target, 60, 3)
	if len(a1) != 1 || a1[0].AdID != 2 {
		t.Errorf("A1 = %v, жду лот 2", a1)
	}
	a2 := m.StrongerForBudget(target, 60, 3, 1.1)
	if len(a2) != 1 || a2[0].AdID != 4 {
		t.Errorf("A2 = %v, жду лот 4", a2)
	}
	a3 := m.BestValueNearPrice(target, 60, 3)
	// €/1000 баллов: лот4 = 320/15 ≈ 21.3 — лучший в окне ±20%.
	if len(a3) == 0 || a3[0].AdID != 4 {
		t.Errorf("A3 = %v, жду первым лот 4", a3)
	}
}

// ---------- PLAN_v5: dGPU-пул, GPU-измерение, гейты цена/качество ----------

func mkLotGPU(adID int64, cpu string, cpuScore float64, ram, ssd int, gpu string, gpuScore float64, price float64, daysAgo int) Lot {
	l := mkLot(adID, cpu, cpuScore, ram, ssd, price, daysAgo)
	l.GPUModel = gpu
	l.GPUScore = gpuScore
	return l
}

func marketWithDGPU(t *testing.T, lots []Lot, windowDays int) *Market {
	t.Helper()
	m := &Market{lots: lots, builtAt: time.Now(), medianWindowDays: windowDays, dgpuOnly: true}
	m.buildGroups(windowDays)
	return m
}

func TestStatsPoolDGPUOnly(t *testing.T) {
	lots := []Lot{
		mkLotGPU(1, "i5-1135G7", 10000, 16, 512, "RTX 3050", 6000, 500, 5), // dGPU
		mkLot(2, "i5-1135G7", 10000, 16, 512, 300, 5),                      // без GPU (офисный)
	}
	full := marketWith(t, lots, 60)
	full.medianWindowDays = 60
	if got := len(full.Pool()); got != 2 {
		t.Errorf("полный пул = %d, жду 2", got)
	}
	dgpu := marketWithDGPU(t, lots, 60)
	pool := dgpu.Pool()
	if len(pool) != 1 || pool[0].AdID != 1 {
		t.Errorf("dGPU-пул = %v, жду только лот 1", pool)
	}
}

func TestMedianKeepsGPUDimension(t *testing.T) {
	// Один CPU/RAM, две разные GPU: медианы НЕ должны смешиваться.
	var lots []Lot
	for i := 0; i < 5; i++ {
		lots = append(lots, mkLotGPU(int64(i+1), "i7-11800H", 15000, 16, 512, "RTX 3050", 6000, 700+float64(i), 5))
	}
	for i := 0; i < 5; i++ {
		lots = append(lots, mkLotGPU(int64(100+i), "i7-11800H", 15000, 16, 512, "RTX 3060", 8000, 900+float64(i), 5))
	}
	m := marketWithDGPU(t, lots, 60)

	est3050 := m.EstimateFor(mkLotGPU(999, "i7-11800H", 15000, 16, 512, "RTX 3050", 6000, 750, 1))
	if est3050.Level == "" || est3050.Median < 690 || est3050.Median > 720 {
		t.Errorf("RTX 3050: оценка %+v — жду медиану своей GPU-группы (~702)", est3050)
	}
	est3060 := m.EstimateFor(mkLotGPU(998, "i7-11800H", 15000, 16, 512, "RTX 3060", 8000, 950, 1))
	if est3060.Median < 890 || est3060.Median > 920 {
		t.Errorf("RTX 3060: оценка %+v — жду медиану своей GPU-группы (~902)", est3060)
	}
}

func TestStrongerCPUGPUForBudget(t *testing.T) {
	target := mkLotGPU(1, "i5-12450H", 10000, 16, 512, "RTX 3050", 5000, 600, 1)
	lots := []Lot{
		target,
		mkLotGPU(2, "i7-12700H", 12000, 16, 512, "RTX 3050", 5000, 620, 1), // мощнее CPU в коридоре
		mkLotGPU(3, "i5-12450H", 10000, 16, 512, "RTX 3060", 7000, 650, 1), // мощнее GPU в коридоре
		mkLotGPU(4, "i9-12900H", 15000, 16, 512, "RTX 3070", 9000, 800, 1), // вне коридора (+33%)
		mkLotGPU(5, "i7-12700H", 12000, 16, 512, "RTX 3060", 7000, 560, 1), // мощнее оба, в коридоре
		mkLotGPU(6, "i5-12450H", 10000, 16, 512, "RTX 3050", 5000, 610, 1), // равное железо — не мощнее
	}
	m := marketWithDGPU(t, lots, 60)

	cpu := m.StrongerCPUForBudget(target, 60, 5, 10, 10)
	if len(cpu) != 2 {
		t.Errorf("мощнее CPU: %d лотов, жду 2 (лоты 2,5)", len(cpu))
	}
	gpu := m.StrongerGPUForBudget(target, 60, 5, 10, 10)
	if len(gpu) != 2 {
		t.Errorf("мощнее GPU: %d лотов, жду 2 (лоты 3,5)", len(gpu))
	}

	// Цель без балла GPU — GPU-гейт честно пропускается.
	noGPU := mkLotGPU(9, "i5-12450H", 10000, 16, 512, "Неизвестная GPU", 0, 600, 1)
	if got := m.StrongerGPUForBudget(noGPU, 60, 5, 10, 10); got != nil {
		t.Errorf("без балла GPU гейт должен быть nil, got %v", got)
	}
}

// ---------- PLAN_v6: композитная мощность и «шаг вверх» ----------

func mkLotFull(adID int64, cpuScore, gpuScore, price float64, daysAgo int) Lot {
	l := mkLot(adID, "cpu", cpuScore, 16, 512, price, daysAgo)
	l.GPUScore = gpuScore
	l.Title = "lot"
	return l
}

func TestCompositeAndValue(t *testing.T) {
	l := Lot{CPUScore: 6871, GPUScore: 47062, Price: 550}
	if l.Composite() != 53933 {
		t.Errorf("Composite = %.0f, жду 53933", l.Composite())
	}
	// 53933 / 550 * 1000 ≈ 98060
	if v := l.ValuePer1000(); v < 98059 || v > 98061 {
		t.Errorf("ValuePer1000 = %.0f, жду ~98060", v)
	}
}

func TestBestStepUp(t *testing.T) {
	// Кандидат: 53933 баллов (CPU 6871 + GPU 47062), €550.
	target := mkLotFull(1, 6871, 47062, 550, 1)
	lots := []Lot{
		target,
		// Мощнее на +10 баллов и дороже на +€1 — «ловушка»: €/балл = 0.10.
		mkLotFull(2, 6881, 47062, 551, 1),
		// Мощнее на +1000 баллов и дороже на +€1 — выгодно: €/балл = 0.001.
		mkLotFull(3, 6871, 48062, 551, 1),
		// Мощнее, но дешевле — не «шаг вверх» (не дороже).
		mkLotFull(4, 6871, 50000, 500, 1),
		// Слабее и дороже — не подходит.
		mkLotFull(5, 6000, 40000, 600, 1),
	}
	m := marketWithDGPU(t, lots, 60)

	steps := m.BestStepUp(target, 60, 2)
	if len(steps) == 0 {
		t.Fatal("шаг вверх не найден")
	}
	// Первым должен быть лот 3 (+1000 баллов за +€1 — минимальная цена за прирост),
	// а не лот 2 (+10 баллов за +€1).
	if steps[0].AdID != 3 {
		t.Errorf("первый шаг = лот %d, жду лот 3 (выгоднейший прирост)", steps[0].AdID)
	}
}

func TestBestStepUpNothingStronger(t *testing.T) {
	target := mkLotFull(1, 20000, 90000, 500, 1) // самый мощный
	lots := []Lot{
		target,
		mkLotFull(2, 10000, 40000, 600, 1), // слабее
		mkLotFull(3, 15000, 50000, 700, 1), // слабее
	}
	m := marketWithDGPU(t, lots, 60)
	if steps := m.BestStepUp(target, 60, 2); len(steps) != 0 {
		t.Errorf("мощнее нет, но вернулось %d шагов", len(steps))
	}
}
