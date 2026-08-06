package pricing

import "sort"

// Альтернативы для алерта (PLAN_v4 §3.5): обязательная часть вердикта —
// «алмаз» должен выдерживать сравнение с рынком, а не только с медианой.

// candidates — пул лотов для подбора альтернатив: статистический пул
// (свежие, без магазинов/хлама, с распознанным CPU).
func (m *Market) candidates(windowDays int) []Lot { return m.statsPool(windowDays) }

// CheaperSameCPU — A1: то же железо дешевле (≤ −5% цены лота), топ по цене.
func (m *Market) CheaperSameCPU(target Lot, windowDays, limit int) []Lot {
	var out []Lot
	for _, l := range m.candidates(windowDays) {
		if l.AdID == target.AdID || l.CPUModel != target.CPUModel {
			continue
		}
		if l.Price <= target.Price*0.95 {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Price < out[j].Price })
	return cut(out, limit)
}

// StrongerForBudget — A2: мощнее за те же деньги (≥ +20% баллов, цена
// ≤ priceCapRatio×цены лота: 1.0 = «те же деньги» для алмазного гейта,
// 1.1 = «≤ +10%» для алерта-альтернатив).
func (m *Market) StrongerForBudget(target Lot, windowDays, limit int, priceCapRatio float64) []Lot {
	var out []Lot
	for _, l := range m.candidates(windowDays) {
		if l.AdID == target.AdID || l.CPUScore <= 0 {
			continue
		}
		if l.CPUScore >= target.CPUScore*1.2 && l.Price <= target.Price*priceCapRatio {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CPUScore > out[j].CPUScore })
	return cut(out, limit)
}

// StrongerCPUForBudget — PLAN_v5, гейт «CPU»: есть ли в ценовом коридоре
// ±bandPct% лот с CPU мощнее на ≥strongerPct% (по бенчмарк-баллам).
// Пул уже ограничен dGPU-лотами при сборке рынка (Options.DGPUOnly).
func (m *Market) StrongerCPUForBudget(target Lot, windowDays, limit int, bandPct, strongerPct float64) []Lot {
	if target.CPUScore <= 0 {
		return nil
	}
	lo, hi := target.Price*(1-bandPct/100), target.Price*(1+bandPct/100)
	var out []Lot
	for _, l := range m.candidates(windowDays) {
		if l.AdID == target.AdID || l.CPUScore <= 0 {
			continue
		}
		if l.Price >= lo && l.Price <= hi && l.CPUScore >= target.CPUScore*(1+strongerPct/100) {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CPUScore > out[j].CPUScore })
	return cut(out, limit)
}

// StrongerGPUForBudget — PLAN_v5, гейт «GPU»: есть ли в ценовом коридоре
// ±bandPct% лот с GPU мощнее на ≥strongerPct%. Если у целевого лота нет
// балла GPU в эталоне — гейт честно пропускается (nil).
func (m *Market) StrongerGPUForBudget(target Lot, windowDays, limit int, bandPct, strongerPct float64) []Lot {
	if target.GPUScore <= 0 {
		return nil
	}
	lo, hi := target.Price*(1-bandPct/100), target.Price*(1+bandPct/100)
	var out []Lot
	for _, l := range m.candidates(windowDays) {
		if l.AdID == target.AdID || l.GPUScore <= 0 {
			continue
		}
		if l.Price >= lo && l.Price <= hi && l.GPUScore >= target.GPUScore*(1+strongerPct/100) {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GPUScore > out[j].GPUScore })
	return cut(out, limit)
}

// BestValueNearPrice — A3: лучший €/1000 баллов в цене ±20% от лота.
func (m *Market) BestValueNearPrice(target Lot, windowDays, limit int) []Lot {
	var out []Lot
	for _, l := range m.candidates(windowDays) {
		if l.AdID == target.AdID || l.CPUScore <= 0 {
			continue
		}
		if l.Price >= target.Price*0.8 && l.Price <= target.Price*1.2 {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return valuePer1000(out[i]) < valuePer1000(out[j])
	})
	return cut(out, limit)
}

func valuePer1000(l Lot) float64 { return l.Price / (l.CPUScore / 1000) }

func cut(lots []Lot, limit int) []Lot {
	if limit > 0 && len(lots) > limit {
		return lots[:limit]
	}
	return lots
}

// BestStepUp — PLAN_v6: «шаг вверх» — ближайший смыслóвой апгрейд.
//
// Среди лотов, которые мощнее кандидата в целом (CPU+GPU) И дороже него,
// выбирается тот, у кого минимальна ЦЕНА ЗА ЕДИНИЦУ ПРИРОСТА мощности:
//
//	cost = (цена − цена_кандидата) / (баллы − баллы_кандидата)   [€/балл]
//
// Это решает дилемму «первого мощнее»: наивный минимум цены обманывается
// вариантом «+10 баллов за +€1», тогда как «+1000 баллов за +€1» на порядки
// выгоднее и метрика выбирает именно его. Возвращаются топ-limit шагов,
// отсортированных по выгодности (дешевле за прирост — раньше).
func (m *Market) BestStepUp(target Lot, windowDays, limit int) []Lot {
	targetComp := target.Composite()
	if targetComp <= 0 {
		return nil
	}
	type cand struct {
		lot  Lot
		cost float64 // € за 1 балл прироста
	}
	var pool []cand
	for _, l := range m.candidates(windowDays) {
		if l.AdID == target.AdID {
			continue
		}
		dComp := l.Composite() - targetComp
		dPrice := l.Price - target.Price
		if dComp <= 0 || dPrice <= 0 {
			continue // мощнее И дороже — только такой «шаг вверх» имеет смысл
		}
		pool = append(pool, cand{lot: l, cost: dPrice / dComp})
	}
	sort.Slice(pool, func(i, j int) bool {
		if pool[i].cost != pool[j].cost {
			return pool[i].cost < pool[j].cost
		}
		return pool[i].lot.Price < pool[j].lot.Price
	})
	out := make([]Lot, 0, limit)
	for i, c := range pool {
		if limit > 0 && i >= limit {
			break
		}
		out = append(out, c.lot)
	}
	return out
}
