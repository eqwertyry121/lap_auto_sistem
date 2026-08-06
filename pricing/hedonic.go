package pricing

import "math"

// Hedonic — гедоническая OLS-модель цены (PLAN_v4 §3.5, Решение ③):
//
//	price = β0 + β1·(cpu_score/1000) + β2·ram_gb + β3·(ssd_gb/256) + β4·(gpu_score/1000)
//
// Без внешних библиотек: нормальные уравнения + метод Гаусса с частичным
// выбором ведущего элемента. Признаки с нулевой дисперсией (например, в
// окне нет ни одного лота с дискретной видеокартой) исключаются из
// регрессии — информации в них нет, а матрицу они вырождают. Используется
// только при R² ≥ 0.60.
type Hedonic struct {
	Beta   [5]float64
	R2     float64
	N      int
	Usable bool
}

const r2UsableThreshold = 0.60

const numFeatures = 5

func features(l Lot) [numFeatures]float64 {
	return [numFeatures]float64{
		1,
		l.CPUScore / 1000,
		float64(l.RAMGB),
		float64(l.SSDGB) / 256,
		l.GPUScore / 1000,
	}
}

// FitHedonic обучает модель на выборке. При недостатке данных или
// вырожденной матрице возвращает модель с Usable=false (честный отказ).
func FitHedonic(lots []Lot) *Hedonic {
	h := &Hedonic{N: len(lots)}
	if len(lots) < 20 { // меньше 20 точек на 5 коэффициентов — несерьёзно
		return h
	}

	// Отбор информативных признаков. Свободный член (0) активен всегда.
	active := []int{0}
	for j := 1; j < numFeatures; j++ {
		if featureVariance(lots, j) > 1e-12 {
			active = append(active, j)
		}
	}
	if len(active) < 2 {
		return h
	}

	// Нормальные уравнения: (XᵀX)β = Xᵀy по активным признакам.
	n := len(active)
	xtx := make([][]float64, n)
	for i := range xtx {
		xtx[i] = make([]float64, n)
	}
	xty := make([]float64, n)
	var ySum float64
	for _, l := range lots {
		x := features(l)
		for i := range active {
			for j := range active {
				xtx[i][j] += x[active[i]] * x[active[j]]
			}
			xty[i] += x[active[i]] * l.Price
		}
		ySum += l.Price
	}

	solved, ok := solveLinear(xtx, xty)
	if !ok {
		return h // точная коллинеарность оставшихся признаков
	}
	for i, col := range active {
		h.Beta[col] = solved[i]
	}

	// R² = 1 − SS_res/SS_tot.
	yMean := ySum / float64(len(lots))
	var ssRes, ssTot float64
	for _, l := range lots {
		pred := h.Predict(l)
		ssRes += (l.Price - pred) * (l.Price - pred)
		ssTot += (l.Price - yMean) * (l.Price - yMean)
	}
	if ssTot <= 0 {
		return h
	}
	h.R2 = 1 - ssRes/ssTot
	h.Usable = h.R2 >= r2UsableThreshold
	return h
}

// Predict — предсказанная рыночная цена (может быть ≤0 для мусорных
// конфигураций — вызывающий код проверяет).
func (h *Hedonic) Predict(l Lot) float64 {
	x := features(l)
	p := 0.0
	for i := 0; i < numFeatures; i++ {
		p += h.Beta[i] * x[i]
	}
	return p
}

func featureVariance(lots []Lot, j int) float64 {
	var sum, sq float64
	for _, l := range lots {
		v := features(l)[j]
		sum += v
		sq += v * v
	}
	n := float64(len(lots))
	mean := sum / n
	return sq/n - mean*mean
}

// solveLinear — метод Гаусса с частичным выбором ведущего элемента.
func solveLinear(a [][]float64, b []float64) ([]float64, bool) {
	n := len(b)
	m := make([][]float64, n) // рабочая копия
	for i := range a {
		m[i] = append([]float64(nil), a[i]...)
	}
	y := append([]float64(nil), b...)

	for col := 0; col < n; col++ {
		pivot := col
		for row := col + 1; row < n; row++ {
			if math.Abs(m[row][col]) > math.Abs(m[pivot][col]) {
				pivot = row
			}
		}
		if math.Abs(m[pivot][col]) < 1e-9 {
			return nil, false // вырожденная матрица
		}
		if pivot != col {
			m[col], m[pivot] = m[pivot], m[col]
			y[col], y[pivot] = y[pivot], y[col]
		}
		for row := col + 1; row < n; row++ {
			factor := m[row][col] / m[col][col]
			for j := col; j < n; j++ {
				m[row][j] -= factor * m[col][j]
			}
			y[row] -= factor * y[col]
		}
	}

	x := make([]float64, n)
	for row := n - 1; row >= 0; row-- {
		sum := y[row]
		for j := row + 1; j < n; j++ {
			sum -= m[row][j] * x[j]
		}
		x[row] = sum / m[row][row]
	}
	return x, true
}
