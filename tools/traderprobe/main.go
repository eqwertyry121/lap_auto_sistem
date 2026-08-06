// traderprobe — зонд семантики user.trader в ответе /eds/ (аудит 2026-08-06).
// is_trader=1 у 100% лотов с деталями: проверяем, что KP реально шлёт частникам —
// объект trader с пустым title (предикат «!= nil» врёт) или отсутствие поля.
// Берёт кандидатов-частников из SEARCH-строк research.db (kp_izlog=0,
// is_renewed=0, посвежее) плюс один контрольный магазин, печатает для каждого
// присутствие поля и его title.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"

	"kpbot/collector"
)

// Контрольный магазин из tools/eds_sample.json (точно Trgovac).
const controlShopID = 151982322

func main() {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "data/research.db")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout=8000`); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	rows, err := db.QueryContext(ctx, `
SELECT ad_id, title
FROM research_ads
WHERE fetch_status='SEARCH' AND kp_izlog=0 AND is_renewed=0
ORDER BY ad_id DESC
LIMIT 12`)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	var candidates []struct {
		id    int64
		title string
	}
	for rows.Next() {
		var id int64
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		candidates = append(candidates, struct {
			id    int64
			title string
		}{id, title})
	}
	if err := rows.Err(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	rows.Close()
	if len(candidates) == 0 {
		fmt.Println("кандидатов-частников нет")
		os.Exit(1)
	}

	kp := collector.NewClient()
	probed := 0

	fmt.Println("=== контрольный магазин (ожидается trader с title) ===")
	if probe(ctx, kp, controlShopID, "Polovni Laptopovi (контроль)") {
		probed++
	}

	fmt.Println("\n=== кандидаты-частники (из SEARCH-строк, без витрины и автообновления) ===")
	for _, c := range candidates {
		if probed >= 4 {
			break
		}
		time.Sleep(jitterPause())
		if probe(ctx, kp, c.id, c.title) {
			probed++
		}
	}
}

// probe делает один вежливый запрос /eds/ и печатает состояние поля trader.
// Возвращает true, если запрос прошёл (успех или 404 не в счёт — челлендж стоп).
func probe(ctx context.Context, kp *collector.Client, adID int64, title string) bool {
	fmt.Printf("\n--- /eds/%d %q\n", adID, title)
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	d, err := kp.FetchDetail(cctx, adID)
	if err != nil {
		if errors.Is(err, collector.ErrChallenge) {
			fmt.Println("антибот-челлендж — зонд останавливается, чтобы не злить KP")
			os.Exit(0)
		}
		if errors.Is(err, collector.ErrNotFound) {
			fmt.Println("лот уже удалён (404)")
			return false
		}
		fmt.Println("ошибка:", err)
		return false
	}
	if d.User.Trader == nil {
		fmt.Printf("trader: ПОЛЕ ОТСУТСТВУЕТ · продавец=%q reviews=%d kpizlog=%v\n",
			d.Seller(), int(d.User.Reviews), d.KPIzlog)
	} else {
		fmt.Printf("trader: ОБЪЕКТ title=%q · продавец=%q reviews=%d kpizlog=%v\n",
			d.User.Trader.Title, d.Seller(), int(d.User.Reviews), d.KPIzlog)
	}
	return true
}

// jitterPause — пауза между запросами, как у research (6с ±40%).
func jitterPause() time.Duration {
	base := 6000
	j := base * 40 / 100
	return time.Duration(base-j+int(time.Now().UnixNano()%int64(2*j+1))) * time.Millisecond
}
