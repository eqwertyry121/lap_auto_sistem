// market-stats — быстрый обзор собранной рыночной базы: сколько лотов,
// распределение цен по валютам, медиана и квартили.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"

	_ "modernc.org/sqlite"

	"kpbot/storage"
)

func main() {
	dbPath := flag.String("db", envOr("DB_PATH", "data/kp_bot.db"), "путь к SQLite-базе")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	slog.SetDefault(log)

	store, err := storage.Open(*dbPath)
	if err != nil {
		fmt.Println("не удалось открыть БД:", err)
		os.Exit(1)
	}
	defer store.Close()

	ctx := context.Background()
	total, _ := store.Count(ctx)

	// Последние 90 дней, цены в EUR (как в кэше цен).
	rows, err := store.RecentPrices(ctx, 90, 100000)
	if err != nil {
		fmt.Println("ошибка выборки цен:", err)
		os.Exit(1)
	}

	fmt.Printf("Всего лотов в базе:            %d\n", total)
	fmt.Printf("Лотов с ценой EUR (90 дней):   %d\n", len(rows))
	if len(rows) == 0 {
		return
	}

	prices := make([]float64, 0, len(rows))
	for _, r := range rows {
		if r.Price > 0 {
			prices = append(prices, r.Price)
		}
	}
	sort.Float64s(prices)

	fmt.Printf("Из них с положительной ценой:  %d\n", len(prices))
	if len(prices) == 0 {
		return
	}
	fmt.Printf("Мин:    €%.0f\n", prices[0])
	fmt.Printf("Медиана:€%.0f\n", percentile(prices, 0.5))
	fmt.Printf("P25:    €%.0f\n", percentile(prices, 0.25))
	fmt.Printf("P75:    €%.0f\n", percentile(prices, 0.75))
	fmt.Printf("Макс:   €%.0f\n", prices[len(prices)-1])

	fmt.Println("\nЦеновые корзины:")
	bins := [][2]float64{{0, 100}, {100, 200}, {200, 300}, {300, 500}, {500, 800}, {800, 1500}, {1500, 1e9}}
	for _, b := range bins {
		n := 0
		for _, p := range prices {
			if p >= b[0] && p < b[1] {
				n++
			}
		}
		label := fmt.Sprintf("€%.0f–%.0f", b[0], b[1])
		if b[1] >= 1e9 {
			label = fmt.Sprintf("€%.0f+", b[0])
		}
		fmt.Printf("  %-12s %5d  %s\n", label, n, bar(n, len(prices)))
	}
}

func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(q * float64(len(sorted)-1))
	return sorted[idx]
}

func bar(n, total int) string {
	if total == 0 {
		return ""
	}
	w := n * 40 / total
	out := make([]byte, 0, w)
	for i := 0; i < w; i++ {
		out = append(out, '#')
	}
	return string(out)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
