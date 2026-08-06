// label — управление золотой выборкой (PLAN_v4, Фаза 2, веха 2.3).
//
//	go run ./cmd/label -export-top 30      # топ продавцов по числу лотов
//	                                       # (кандидаты в SHOP) — на ручную разметку
//	go run ./cmd/label -export-random 30   # продавцы с 1–2 лотами (кандидаты в PRIVATE)
//	go run ./cmd/label -import data/labels_review.csv  # разметка обратно в БД
//	go run ./cmd/label -show               # текущая разметка
//
// Формат CSV (заполняются колонки label и note):
// target_type,target_id,ads,trader_seen,kpizlog_seen,reviews,user_created,sample_title,label,note
// label для продавцов: SHOP | PRIVATE; пометки лотов (target_type=ad): JUNK | CLEAN.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"flag"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	var (
		dbPath       = flag.String("db", "data/research.db", "SQLite-файл датасета")
		exportTop    = flag.Int("export-top", 0, "выгрузить N самых многолотовых продавцов на разметку")
		exportRandom = flag.Int("export-random", 0, "выгрузить N случайных продавцов с 1–2 лотами")
		importPath   = flag.String("import", "", "импорт размеченного CSV обратно в БД")
		show         = flag.Bool("show", false, "показать текущую разметку")
		outPath      = flag.String("out", "data/labels_review.csv", "куда писать экспорт")
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
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		fmt.Println("busy_timeout:", err)
		os.Exit(1)
	}
	if err := ensureTables(ctx, db); err != nil {
		fmt.Println("таблицы:", err)
		os.Exit(1)
	}

	switch {
	case *exportTop > 0:
		exportSellers(ctx, db, *outPath, *exportTop, false)
	case *exportRandom > 0:
		exportSellers(ctx, db, *outPath, *exportRandom, true)
	case *importPath != "":
		importLabels(ctx, db, *importPath)
	case *show:
		showLabels(ctx, db)
	default:
		flag.Usage()
	}
}

// ensureTables — владелец схемы research.db — cmd/research (researchSchema);
// здесь CREATE IF NOT EXISTS, чтобы инструмент работал и до первого запуска
// обновлённого research.
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

func exportSellers(ctx context.Context, db *sql.DB, outPath string, n int, random bool) {
	order := `ORDER BY ads DESC`
	having := ``
	if random {
		order = `ORDER BY RANDOM()`
		having = `HAVING ads <= 2`
	}
	q := fmt.Sprintf(`
SELECT a.user_id, COUNT(*) AS ads,
	COALESCE(MAX(s.trader_seen),0), COALESCE(MAX(s.kpizlog_seen),0),
	COALESCE(MAX(s.reviews),0), COALESCE(MAX(s.user_created),''),
	MAX(a.title)
FROM research_ads a
LEFT JOIN sellers s ON s.user_id = a.user_id
WHERE a.user_id > 0
GROUP BY a.user_id %s %s
LIMIT ?`, having, order)

	rows, err := db.QueryContext(ctx, q, n)
	if err != nil {
		fmt.Println("выборка:", err)
		os.Exit(1)
	}
	defer rows.Close()

	f, err := os.Create(outPath)
	if err != nil {
		fmt.Println("csv:", err)
		os.Exit(1)
	}
	defer f.Close()
	f.WriteString("\ufeff") // BOM для Excel
	w := csv.NewWriter(f)
	_ = w.Write([]string{"target_type", "target_id", "ads", "trader_seen", "kpizlog_seen",
		"reviews", "user_created", "sample_title", "label", "note"})
	count := 0
	for rows.Next() {
		var (
			userID                      int64
			ads, trader, izlog, reviews int
			created, title              string
		)
		if err := rows.Scan(&userID, &ads, &trader, &izlog, &reviews, &created, &title); err != nil {
			fmt.Println("scan:", err)
			os.Exit(1)
		}
		_ = w.Write([]string{"seller", fmt.Sprint(userID), fmt.Sprint(ads),
			fmt.Sprint(trader), fmt.Sprint(izlog), fmt.Sprint(reviews), created, title, "", ""})
		count++
	}
	w.Flush()
	fmt.Printf("Выгружено %d продавцов → %s\nЗаполните колонки label/note и запустите -import.\n",
		count, outPath)
}

func importLabels(ctx context.Context, db *sql.DB, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Println("csv:", err)
		os.Exit(1)
	}
	// Экспорт пишет BOM для Excel; импорт обязан его снять, иначе первая
	// колонка заголовка читается как «\ufefftarget_type» и файл отвергается.
	data = bytes.TrimPrefix(data, []byte("\ufeff"))
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil {
		fmt.Println("разбор csv:", err)
		os.Exit(1)
	}
	if len(recs) < 2 {
		fmt.Println("пустой csv")
		return
	}
	header := recs[0]
	idx := map[string]int{}
	for i, h := range header {
		idx[h] = i
	}
	for _, col := range []string{"target_type", "target_id", "label"} {
		if _, ok := idx[col]; !ok {
			fmt.Printf("в csv нет обязательной колонки %q\n", col)
			os.Exit(1)
		}
	}

	imported, skipped := 0, 0
	for _, rec := range recs[1:] {
		label := field(rec, idx, "label")
		if label == "" {
			skipped++
			continue
		}
		var id int64
		if _, err := fmt.Sscanf(field(rec, idx, "target_id"), "%d", &id); err != nil {
			fmt.Printf("строка %v: target_id не число — пропуск\n", rec)
			skipped++
			continue
		}
		note := field(rec, idx, "note")
		if _, err := db.ExecContext(ctx, `
INSERT INTO labels (target_type, target_id, label, note, created)
VALUES (?,?,?,?,strftime('%s','now'))
ON CONFLICT(target_type, target_id) DO UPDATE SET
	label=excluded.label, note=excluded.note, created=excluded.created`,
			field(rec, idx, "target_type"), id, label, note); err != nil {
			fmt.Println("запись:", err)
			os.Exit(1)
		}
		imported++
	}
	fmt.Printf("Импортировано разметок: %d (пропущено пустых: %d)\n", imported, skipped)
}

func showLabels(ctx context.Context, db *sql.DB) {
	rows, err := db.QueryContext(ctx,
		`SELECT target_type, target_id, label, note FROM labels ORDER BY target_type, target_id`)
	if err != nil {
		fmt.Println("разметка:", err)
		os.Exit(1)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var t, label, note string
		var id int64
		_ = rows.Scan(&t, &id, &label, &note)
		fmt.Printf("  %-7s %-12d %-8s %s\n", t, id, label, note)
		n++
	}
	fmt.Printf("Всего: %d\n", n)
}

func field(rec []string, idx map[string]int, name string) string {
	i, ok := idx[name]
	if !ok || i >= len(rec) {
		return ""
	}
	return rec[i]
}
