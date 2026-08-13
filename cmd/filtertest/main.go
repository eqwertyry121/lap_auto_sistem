// filtertest — регрессионный прогон фильтров на золотой выборке
// (PLAN_v4 §3.6): обязательный шаг любого изменения словарей/порогов.
//
//	go run ./cmd/filtertest            # метрики по текущей разметке
//
// Пороги прохождения (PLAN_v4 §3.2, §3.3):
//
//	L1: recall(SHOP) ≥ 0.95, precision(SHOP) ≥ 0.90, ложных на PRIVATE ≤ 5%;
//	L2: 100% размеченных JUNK не проходят как CLEAN/DEFECT.
//
// Выход 1 при провале порогов; при недостатке разметки — предупреждение и 0
// (нельзя валить проверку там, где ещё нет эталона).
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"

	"kpbot/filters"
)

func main() {
	dbPath := flag.String("db", "data/research.db", "SQLite-файл датасета")
	flag.Parse()

	ctx := context.Background()
	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		fmt.Println("датасет:", err)
		os.Exit(1)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		fmt.Println("busy_timeout:", err)
		os.Exit(1)
	}
	if err := ensureTables(ctx, db); err != nil {
		fmt.Println("таблицы:", err)
		os.Exit(1)
	}

	sellerLabels := loadLabels(ctx, db, "seller")
	adLabels := loadLabels(ctx, db, "ad")
	if len(sellerLabels) < 5 && len(adLabels) < 5 {
		fmt.Println("Золотая выборка пуста/мала. Сначала: go run ./cmd/label -export-top 30,")
		fmt.Println("затем разметить CSV и: go run ./cmd/label -import data/labels_review.csv")
		return
	}

	failed := false

	// ---------- L1: продавцы ----------
	if len(sellerLabels) >= 5 {
		var tp, fp, fn, tn int
		type errCase struct {
			userID  int64
			want    string
			got     string
			reasons string
		}
		var errs []errCase
		for userID, want := range sellerLabels {
			if want != "SHOP" && want != "PRIVATE" {
				continue
			}
			got, reasons := classifySeller(ctx, db, userID)
			switch {
			case want == "SHOP" && got == "SHOP":
				tp++
			case want == "SHOP" && got != "SHOP":
				fn++
				errs = append(errs, errCase{userID, want, got, reasons})
			case want == "PRIVATE" && got == "SHOP":
				fp++
				errs = append(errs, errCase{userID, want, got, reasons})
			default:
				tn++
			}
		}
		recall, precision, fpr := safeRatio(tp, tp+fn), safeRatio(tp, tp+fp), safeRatio(fp, fp+tn)
		fmt.Printf("=== L1: фильтр магазинов ===\n")
		fmt.Printf("Размечено продавцов: %d (SHOP %d, PRIVATE %d)\n",
			tp+fp+fn+tn, tp+fn, fp+tn)
		fmt.Printf("TP=%d FP=%d FN=%d TN=%d\n", tp, fp, fn, tn)
		fmt.Printf("recall(SHOP)    = %.2f  (порог ≥ 0.95)\n", recall)
		fmt.Printf("precision(SHOP) = %.2f  (порог ≥ 0.90)\n", precision)
		fmt.Printf("false-pos на PRIVATE = %.2f  (порог ≤ 0.05)\n", fpr)
		for _, e := range errs {
			fmt.Printf("  ОШИБКА: продавец %d — ждали %s, получили %s (%s)\n",
				e.userID, e.want, e.got, e.reasons)
		}
		if tp+fn > 0 && (recall < 0.95 || precision < 0.90 || fpr > 0.05) {
			fmt.Println("❌ L1 НЕ ПРОШЁЛ пороги")
			failed = true
		} else {
			fmt.Println("✅ L1 в порогах")
		}
	} else {
		fmt.Println("L1: размеченных продавцов < 5 — пропускаю (нужна выборка)")
	}

	// ---------- L2: хлам ----------
	if len(adLabels) >= 5 {
		var pass, fail int
		for adID, want := range adLabels {
			if want != "JUNK" && want != "CLEAN" {
				continue
			}
			got, reasons := classifyAd(ctx, db, adID)
			blocked := got == filters.JunkPartsOnly || got == filters.JunkUncertain
			if want == "JUNK" && blocked {
				pass++
			} else if want == "CLEAN" && got == filters.JunkClean {
				pass++
			} else {
				fail++
				fmt.Printf("  ОШИБКА: лот %d — ждали %s, получили %s (%s)\n",
					adID, want, got, reasons)
			}
		}
		fmt.Printf("\n=== L2: фильтр хлама ===\nРазмечено лотов: %d, ошибок: %d\n", pass+fail, fail)
		if fail > 0 {
			fmt.Println("❌ L2: размеченный хлам/чистые проходят неправильно")
			failed = true
		} else {
			fmt.Println("✅ L2 без ошибок на выборке")
		}
	} else {
		fmt.Println("L2: размеченных лотов < 5 — пропускаю (нужна выборка)")
	}

	fmt.Printf("\nВерсия словарей: %d\n", filters.MarkersVersion)
	if failed {
		os.Exit(1)
	}
}

// ensureTables — владелец схемы research.db — cmd/research; здесь CREATE IF
// NOT EXISTS, чтобы фильтр-тест работал независимо от состояния процесса.
func ensureTables(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS sellers (
	user_id      INTEGER PRIMARY KEY,
	first_seen   INTEGER NOT NULL DEFAULT 0,
	last_seen    INTEGER NOT NULL DEFAULT 0,
	ads_seen     INTEGER NOT NULL DEFAULT 0,
	trader_seen  INTEGER NOT NULL DEFAULT 0,
	kpizlog_seen INTEGER NOT NULL DEFAULT 0,
	reviews      INTEGER NOT NULL DEFAULT 0,
	user_created TEXT NOT NULL DEFAULT '',
	verdict      TEXT NOT NULL DEFAULT '',
	label        TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS labels (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	target_type TEXT NOT NULL,
	target_id   INTEGER NOT NULL,
	label       TEXT NOT NULL,
	note        TEXT NOT NULL DEFAULT '',
	created     INTEGER NOT NULL DEFAULT 0,
	UNIQUE(target_type, target_id)
)`)
	return err
}

func loadLabels(ctx context.Context, db *sql.DB, targetType string) map[int64]string {
	out := map[int64]string{}
	rows, err := db.QueryContext(ctx,
		`SELECT target_id, label FROM labels WHERE target_type = ?`, targetType)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var label string
		if err := rows.Scan(&id, &label); err == nil {
			out[id] = label
		}
	}
	return out
}

// classifySeller — прогноз L1 по всем лотам продавца: берутся худший для
// магазина случай (максимум сигналов) — как это будет в живой воронке.
func classifySeller(ctx context.Context, db *sql.DB, userID int64) (string, string) {
	rows, err := db.QueryContext(ctx, `
SELECT title, description, condition, is_trader, kp_izlog
FROM research_ads WHERE user_id = ?`, userID)
	if err != nil {
		return "PRIVATE", "ошибка выборки: " + err.Error()
	}
	defer rows.Close()

	var facts []filters.AdFacts
	for rows.Next() {
		var f filters.AdFacts
		var trader, izlog int
		if err := rows.Scan(&f.Title, &f.Description, &f.Condition, &trader, &izlog); err != nil {
			return "PRIVATE", err.Error()
		}
		f.IsTrader = trader == 1
		f.KPIzlog = izlog == 1
		facts = append(facts, f)
	}
	if len(facts) == 0 {
		return "PRIVATE", "лотов продавца нет в датасете"
	}

	ads, _ := countSellerAds(ctx, db, userID)
	recentAds, _ := countSellerRecentAds(ctx, db, userID)
	age := sellerAgeDays(ctx, db, userID)
	for i := range facts {
		facts[i].SellerAds = ads
		facts[i].SellerRecentAds = recentAds
		facts[i].SellerAgeDays = age
	}

	// Вердикт продавца = самый «магазинный» вердикт среди его лотов.
	best := filters.Verdict{Class: filters.ClassUnknown}
	for _, f := range facts {
		v := filters.L1(f)
		if v.Class == filters.ClassShop {
			best = v
			break
		}
		if len(v.Reasons) > len(best.Reasons) {
			best = v
		}
	}
	if best.Class == filters.ClassShop {
		return filters.ClassShop, joinReasons(best.Reasons)
	}
	return filters.ClassPrivate, joinReasons(best.Reasons)
}

func classifyAd(ctx context.Context, db *sql.DB, adID int64) (string, string) {
	var f filters.AdFacts
	err := db.QueryRowContext(ctx, `
SELECT title, description, condition FROM research_ads WHERE ad_id = ?`, adID).
		Scan(&f.Title, &f.Description, &f.Condition)
	if err != nil {
		return filters.JunkClean, "лот не найден: " + err.Error()
	}
	v := filters.L2(f)
	return v.Class, joinReasons(v.Reasons)
}

func countSellerAds(ctx context.Context, db *sql.DB, userID int64) (int, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM research_ads WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}

func countSellerRecentAds(ctx context.Context, db *sql.DB, userID int64) (int, error) {
	var n int
	recentSince := time.Now().Add(-30 * 24 * time.Hour).Unix()
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM research_ads WHERE user_id = ? AND fetched_at >= ?`,
		userID, recentSince).Scan(&n)
	return n, err
}

func sellerAgeDays(ctx context.Context, db *sql.DB, userID int64) int {
	var created string
	err := db.QueryRowContext(ctx,
		`SELECT user_created FROM sellers WHERE user_id = ?`, userID).Scan(&created)
	if err != nil || created == "" {
		return -1
	}
	t, err := time.Parse("2006-01-02 15:04:05", created)
	if err != nil {
		return -1
	}
	return int(time.Since(t).Hours() / 24)
}

func joinReasons(rs []string) string {
	out := ""
	for i, r := range rs {
		if i > 0 {
			out += "; "
		}
		out += r
		if i == 2 {
			out += "; …"
			break
		}
	}
	return out
}

func safeRatio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}
