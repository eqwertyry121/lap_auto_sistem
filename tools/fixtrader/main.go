// fixtrader — разовая починка данных после аудита (2026-08-06).
//
// is_trader / sellers.trader_seen писались сломанным предикатом «поле
// user.trader присутствует в ответе /eds/». KP присылает этот объект ВСЕМ
// продавцам (частникам — с title «Nije trgovac»), поэтому 100% лотов с
// деталями оказались «магазинами», и весь детальный слой выпал из пула
// медиан pricing (statsPool/hedonicTraining исключают IsShop).
//
// Сырой title в БД не сохранялся, поэтому восстановить правду из данных
// нельзя — обнуляем неверные флаги: неизвестный продавец трактуется как
// частник (kp_izlog, витрина, остаётся надёжным маркером профессионала).
// Новые закачки пишут корректные значения: models.IsTrader теперь
// сравнивает title с «Trgovac» (регрессия в models/models_test.go).
// Идемпотентен: повторный запуск затронет 0 строк.
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
	dbPath := flag.String("db", "data/research.db", "SQLite-файл датасета рынка")
	flag.Parse()

	ctx := context.Background()
	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// БД может быть занята живым research/kpbot — запускать при остановленных
	// процессах, но на всякий случай ждём, а не глотаем ошибки.
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout=8000`); err != nil {
		fmt.Println("busy_timeout:", err)
		os.Exit(1)
	}

	beforeAds := count(ctx, db, `SELECT COUNT(*) FROM research_ads WHERE is_trader=1`)
	beforeSellers := count(ctx, db, `SELECT COUNT(*) FROM sellers WHERE trader_seen=1`)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		fmt.Println("tx:", err)
		os.Exit(1)
	}
	resAds, err := tx.ExecContext(ctx, `UPDATE research_ads SET is_trader=0 WHERE is_trader=1`)
	if err != nil {
		_ = tx.Rollback()
		fmt.Println("update research_ads:", err)
		os.Exit(1)
	}
	nAds, _ := resAds.RowsAffected()
	resSellers, err := tx.ExecContext(ctx, `UPDATE sellers SET trader_seen=0 WHERE trader_seen=1`)
	if err != nil {
		_ = tx.Rollback()
		fmt.Println("update sellers:", err)
		os.Exit(1)
	}
	nSellers, _ := resSellers.RowsAffected()
	if err := tx.Commit(); err != nil {
		fmt.Println("commit:", err)
		os.Exit(1)
	}

	afterAds := count(ctx, db, `SELECT COUNT(*) FROM research_ads WHERE is_trader=1`)
	afterSellers := count(ctx, db, `SELECT COUNT(*) FROM sellers WHERE trader_seen=1`)

	fmt.Printf("research_ads.is_trader=1:  было %d → обнулено %d → стало %d\n",
		beforeAds, nAds, afterAds)
	fmt.Printf("sellers.trader_seen=1:     было %d → обнулено %d → стало %d\n",
		beforeSellers, nSellers, afterSellers)
	if afterAds != 0 || afterSellers != 0 {
		fmt.Println("ВНИМАНИЕ: после починки остались ненулевые флаги")
		os.Exit(1)
	}
	fmt.Println("готово: маркером магазина остался kp_izlog; новые /eds/ пишут честный is_trader")
}

func count(ctx context.Context, db *sql.DB, query string) int {
	var n int
	if err := db.QueryRowContext(ctx, query).Scan(&n); err != nil {
		fmt.Println(query, ":", err)
		os.Exit(1)
	}
	return n
}
