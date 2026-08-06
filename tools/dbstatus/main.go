// dbstatus — быстрый снимок покрытия датасета: сколько каркаса, сколько
// деталей, сколько железа распознано. Никуда не пишет, только читает.
//
//	go run ./tools/dbstatus
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	var (
		dbPath = flag.String("db", "data/research.db", "SQLite-файл датасета рынка")
		hwPath = flag.String("hw", "data/hw.db", "SQLite-файл эталонной базы железа")
	)
	flag.Parse()

	ctx := context.Background()
	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		fmt.Println("датасет:", err)
		os.Exit(1)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// БД может быть занята живым research/kpbot — ждём, а не глотаем ошибки.
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout=8000`); err != nil {
		fmt.Println("busy_timeout:", err)
		os.Exit(1)
	}

	var total int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM research_ads`).Scan(&total); err != nil {
		fmt.Println("count:", err)
		os.Exit(1)
	}
	fmt.Printf("=== ДАТАСЕТ (%s) ===\nВсего лотов: %d\n\n", *dbPath, total)

	rows, err := db.QueryContext(ctx,
		`SELECT fetch_status, COUNT(*) FROM research_ads GROUP BY fetch_status ORDER BY COUNT(*) DESC`)
	if err != nil {
		fmt.Println("статусы:", err)
		os.Exit(1)
	}
	fmt.Println("По статусам закачки:")
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			fmt.Println("статусы:", err)
			os.Exit(1)
		}
		label := st
		if label == "" {
			label = "(пусто)"
		}
		fmt.Printf("  %-12s %6d  (%.1f%%)\n", label, n, 100*float64(n)/float64(max(total, 1)))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		fmt.Println("статусы:", err)
		os.Exit(1)
	}

	// Детали: состояние, магазины.
	fmt.Println("\nСреди лотов с деталями (fetch_status='OK'):")
	varQuery := func(label, where string) {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM research_ads WHERE fetch_status='OK' AND (`+where+`)`).Scan(&n); err != nil {
			fmt.Printf("  %s: %v\n", label, err)
			return
		}
		fmt.Printf("  %-28s %6d\n", label, n)
	}
	varQuery("состояние NEW", `condition='new'`)
	varQuery("состояние USED", `condition='used'`)
	varQuery("маркеры хлама (kind=BROKEN)", `kind='BROKEN'`)
	varQuery("Trgovac (is_trader=1)", `is_trader=1`)
	varQuery("KP Izlog (kp_izlog=1)", `kp_izlog=1`)
	varQuery("магазин (любой из двух)", `is_trader=1 OR kp_izlog=1`)
	varQuery("частник (без меток)", `is_trader=0 AND kp_izlog=0`)
	varQuery("частник с описанием", `is_trader=0 AND kp_izlog=0 AND description != ''`)
	varQuery("магазин с описанием", `(is_trader=1 OR kp_izlog=1) AND description != ''`)

	rows2, err := db.QueryContext(ctx,
		`SELECT kind, COUNT(*) FROM research_ads WHERE fetch_status='OK' GROUP BY kind ORDER BY COUNT(*) DESC`)
	if err != nil {
		fmt.Println("разбивка kind:", err)
		os.Exit(1)
	}
	fmt.Println("  Разбивка kind:")
	for rows2.Next() {
		var kind string
		var n int
		if err := rows2.Scan(&kind, &n); err != nil {
			fmt.Println("разбивка kind:", err)
			os.Exit(1)
		}
		fmt.Printf("    %-10s %6d\n", kind, n)
	}
	rows2.Close()
	if err := rows2.Err(); err != nil {
		fmt.Println("разбивка kind:", err)
		os.Exit(1)
	}

	// Распознанное железо.
	countOne := func(label, query string) int {
		var n int
		if err := db.QueryRowContext(ctx, query).Scan(&n); err != nil {
			fmt.Printf("%s: %v\n", label, err)
			os.Exit(1)
		}
		return n
	}
	specsTotal := countOne("research_specs: всего строк", `SELECT COUNT(*) FROM research_specs`)
	cpuScored := countOne("research_specs: cpu_score", `SELECT COUNT(*) FROM research_specs WHERE cpu_score>0`)
	gpuScored := countOne("research_specs: gpu_score", `SELECT COUNT(*) FROM research_specs WHERE gpu_score>0`)
	fmt.Printf("\nРаспознанное железо (research_specs):\n")
	fmt.Printf("  строк всего:                %6d\n", specsTotal)
	fmt.Printf("  CPU сверен с эталоном:      %6d\n", cpuScored)
	fmt.Printf("  GPU сверен с эталоном:      %6d\n", gpuScored)

	// Цепочка фильтров обучения гедонической модели (PLAN_v4 §3.5).
	var usedAll, usedPrivate, usedPP, usedFinal int
	if err := db.QueryRowContext(ctx, `
WITH t AS (
	SELECT a.ad_id, a.price, a.currency, a.posted, a.is_trader, a.kp_izlog,
		COALESCE(s.cpu_score,0) AS cpu_score
	FROM research_ads a LEFT JOIN research_specs s ON s.ad_id=a.ad_id
	WHERE a.fetch_status='OK' AND a.kind='USED'
)
SELECT COUNT(*),
	COALESCE(SUM(CASE WHEN is_trader=0 AND kp_izlog=0 THEN 1 ELSE 0 END),0),
	COALESCE(SUM(CASE WHEN is_trader=0 AND kp_izlog=0 AND price>=10 AND price<=5000 THEN 1 ELSE 0 END),0),
	COALESCE(SUM(CASE WHEN is_trader=0 AND kp_izlog=0 AND price>=10 AND price<=5000 AND cpu_score>0 AND posted>date('now','-90 day') THEN 1 ELSE 0 END),0)
FROM t`).Scan(&usedAll, &usedPrivate, &usedPP, &usedFinal); err != nil {
		fmt.Println("цепочка OLS:", err)
		os.Exit(1)
	}
	fmt.Printf("\nЦепочка обучения OLS (kind=USED, fetch OK):\n")
	fmt.Printf("  всего USED:                 %6d\n", usedAll)
	fmt.Printf("  без магазинов:              %6d\n", usedPrivate)
	fmt.Printf("  + цена 10–5000:             %6d\n", usedPP)
	fmt.Printf("  + CPU и свежее 90д:         %6d\n", usedFinal)

	// Реестр продавцов (Фаза 2).
	sellersN := countOne("sellers: всего", `SELECT COUNT(*) FROM sellers`)
	sellersTraders := countOne("sellers: с метками магазина",
		`SELECT COUNT(*) FROM sellers WHERE trader_seen=1 OR kpizlog_seen=1`)
	withUser := countOne("research_ads: лоты с user_id", `SELECT COUNT(*) FROM research_ads WHERE user_id>0`)
	fmt.Printf("\nРеестр продавцов: %d (из них с метками магазина: %d)\n", sellersN, sellersTraders)
	fmt.Printf("Лотов с user_id: %d из %d (бэкфилл: go run ./cmd/research -search-refresh)\n",
		withUser, total)

	// Эталон.
	hwdb, err := sql.Open("sqlite", *hwPath)
	if err != nil {
		fmt.Println("\nэталон:", err)
		os.Exit(1)
	}
	defer hwdb.Close()
	hwdb.SetMaxOpenConns(1)
	// hw.db раз в месяц атомарно перезаписывается cmd/hwdb — ждём, а не глотаем.
	if _, err := hwdb.ExecContext(ctx, `PRAGMA busy_timeout=8000`); err != nil {
		fmt.Println("эталон: busy_timeout:", err)
		os.Exit(1)
	}
	countHW := func(label, query string) int {
		var n int
		if err := hwdb.QueryRowContext(ctx, query).Scan(&n); err != nil {
			fmt.Printf("эталон: %s: %v\n", label, err)
			os.Exit(1)
		}
		return n
	}
	cpus := countHW("hw_cpu", `SELECT COUNT(*) FROM hw_cpu`)
	gpus := countHW("hw_gpu", `SELECT COUNT(*) FROM hw_gpu`)
	fmt.Printf("\nЭталон мощности (%s): CPU %d, GPU %d\n", *hwPath, cpus, gpus)
}
