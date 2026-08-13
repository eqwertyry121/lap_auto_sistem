// descprobe — диагностика: почему у лотов частников пустые описания.
// Печатает строки из research.db и один живой запрос /eds/ по такому лоту.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"

	"kpbot/collector"
	"kpbot/config"
)

func main() {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "data/research.db")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	rows, err := db.QueryContext(ctx, `
SELECT ad_id, title, seller, length(description), length(attrs_json), posted, fetched_at
FROM research_ads
WHERE fetch_status='OK' AND is_trader=0 AND kp_izlog=0
LIMIT 5`)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	var firstID int64
	fmt.Println("=== строки частников в research.db ===")
	for rows.Next() {
		var adID int64
		var title, seller, posted string
		var descLen, attrsLen int
		var fetchedAt int64
		_ = rows.Scan(&adID, &title, &seller, &descLen, &attrsLen, &posted, &fetchedAt)
		fmt.Printf("ad_id=%d desc_len=%d attrs_len=%d fetched_at=%d posted=%s seller=%q title=%q\n",
			adID, descLen, attrsLen, fetchedAt, posted, seller, title)
		if firstID == 0 {
			firstID = adID
		}
	}
	rows.Close()

	// Сколько OK-строк с нулевым fetched_at (не из ветки деталей)?
	var okZero, okAll int
	_ = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM research_ads WHERE fetch_status='OK' AND fetched_at=0`).Scan(&okZero)
	_ = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM research_ads WHERE fetch_status='OK'`).Scan(&okAll)
	fmt.Printf("\nOK-строк всего: %d, из них с fetched_at=0: %d\n", okAll, okZero)

	if firstID == 0 {
		fmt.Println("частников нет")
		return
	}

	fmt.Printf("\n=== живой /eds/%d ===\n", firstID)
	cfg := config.Load()
	kp := collector.NewClient(collector.WithSharedCooldown(
		cfg.KPCooldownPath, cfg.KPRateCooldown, cfg.KPChallengeCooldown,
	))
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	d, err := kp.FetchDetail(cctx, firstID)
	if err != nil {
		fmt.Println("ошибка:", err)
		return
	}
	fmt.Printf("description_len=%d condition=%q seller=%q trader=%v kpizlog=%v\n",
		len(d.Description), d.Condition, d.Seller(), d.IsTrader(), d.KPIzlog)
	if len(d.Description) > 0 {
		end := 300
		if len(d.Description) < end {
			end = len(d.Description)
		}
		fmt.Printf("начало описания: %q\n", d.Description[:end])
	}
	fmt.Printf("атрибутов: %d\n", len(d.Attributes))
}
