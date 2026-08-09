// marketsim — офлайн-симуляция лота против рыночной модели воронки.
// Воспроизводит ступень L4 (pricing.LoadMarket с DGPUOnly, как в живом боте)
// БЕЗ запросов к KP: по заданным спекам показывает, найдётся ли рыночная
// группа (K0→K2), каков фолбэк гедоники K3 и отклонение цены.
//
//	go run ./tools/marketsim -cpu "Ryzen 7 7735HS" -gpu "RTX 4060" -ram 16 -ssd 1024 -price 1100
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"kpbot/hw"
	"kpbot/pricing"
)

func main() {
	var (
		dbPath  = flag.String("db", "data/research.db", "датасет рынка")
		hwPath  = flag.String("hw", "data/hw.db", "эталон железа")
		cpuName = flag.String("cpu", "", "модель CPU (как в объявлении)")
		gpuName = flag.String("gpu", "", "модель дискретной GPU")
		ramGB   = flag.Int("ram", 0, "RAM, GB")
		ssdGB   = flag.Int("ssd", 0, "SSD, GB")
		price   = flag.Float64("price", 0, "цена лота, EUR")
	)
	flag.Parse()
	if *cpuName == "" || *price <= 0 {
		flag.Usage()
		os.Exit(1)
	}

	ctx := context.Background()
	cpus, err := hw.LoadCPUs(ctx, *hwPath)
	if err != nil {
		fmt.Println("hw_cpu:", err)
		os.Exit(1)
	}
	gpus, err := hw.LoadGPUs(ctx, *hwPath)
	if err != nil {
		fmt.Println("hw_gpu:", err)
		os.Exit(1)
	}

	cpuScore, cpuKey := matchCPU(cpus, *cpuName)
	gpuScore, gpuKey := 0.0, ""
	if *gpuName != "" {
		gpuScore, gpuKey = matchGPU(gpus, *gpuName)
	}
	fmt.Printf("CPU: %q → балл %.0f (%s)\n", *cpuName, cpuScore, orDash(cpuKey))
	if *gpuName != "" {
		fmt.Printf("GPU: %q → балл %.0f (%s)\n", *gpuName, gpuScore, orDash(gpuKey))
	}
	if cpuScore <= 0 {
		fmt.Println("CPU вне эталона — воронка дала бы CHECK/MANUAL, симуляция не нужна")
		os.Exit(0)
	}

	opts := pricing.DefaultOptions()
	opts.DGPUOnly = true // как в живом боте (REQUIRE_DGPU=1)
	m, err := pricing.LoadMarket(ctx, *dbPath, opts)
	if err != nil {
		fmt.Println("рынок:", err)
		os.Exit(1)
	}
	hed := m.Hedonic()
	fmt.Printf("\nРынок (dGPU-режим): пул %d лотов · гедоника N=%d R²=%.3f usable=%v\n",
		len(m.Pool()), hed.N, hed.R2, hed.Usable)

	lot := pricing.Lot{
		AdID:     -1, // вне датасета, как живой лот
		Title:    "(симуляция)",
		Price:    *price,
		Kind:     "USED",
		CPUModel: *cpuName, CPUScore: cpuScore,
		RAMGB: *ramGB, SSDGB: *ssdGB,
		GPUModel: *gpuName, GPUScore: gpuScore,
	}
	est := m.EstimateFor(lot)
	dev, devOK := m.Deviation(lot)
	fmt.Printf("\nОценка рынка: уровень=%s медиана/предсказание=%.0f€ n=%d\n",
		orDash(est.Level), est.Median, est.N)
	if !devOK {
		fmt.Println("Отклонение: НЕТ (devOK=false) → воронка дала бы RARE_NO_MARKET")
		return
	}
	fmt.Printf("Отклонение: %+.0f%% (devOK=true) → воронка считала бы вердикт L5\n", dev*100)
	switch {
	case dev > 0.05:
		fmt.Println("L5: EXPENSIVE (тихо)")
	case dev < -0.40:
		fmt.Println("L5: DIAMOND_SUSPECT (алерт)")
	case dev <= -0.15:
		fmt.Println("L5: DIAMOND (алерт, если n≥5; иначе CHECK)")
	default:
		fmt.Println("L5: FAIR (тихо)")
	}
}

// matchCPU/matchGPU — нормализация имени и поиск балла в эталоне: точный
// ключ, затем двусторонний поиск по подстроке (как в воронке/research).
func matchCPU(ref map[string]hw.CPU, name string) (float64, string) {
	key := hw.Key(name)
	if c, ok := ref[key]; ok {
		return c.Score, key
	}
	var best float64
	var bestKey string
	for k, c := range ref {
		if strings.Contains(k, key) || strings.Contains(key, k) {
			if bestKey == "" || len(k) > len(bestKey) {
				best, bestKey = c.Score, k
			}
		}
	}
	return best, bestKey
}

func matchGPU(ref map[string]hw.GPU, name string) (float64, string) {
	key := hw.Key(name)
	if g, ok := ref[key]; ok {
		return g.Score, key
	}
	var best float64
	var bestKey string
	for k, g := range ref {
		if strings.Contains(k, key) || strings.Contains(key, k) {
			if bestKey == "" || len(k) > len(bestKey) {
				best, bestKey = g.Score, k
			}
		}
	}
	return best, bestKey
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
