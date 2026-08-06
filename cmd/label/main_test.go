package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// Регрессия: экспорт пишет BOM для Excel — импорт обязан такой файл принять
// (раньше первая колонка заголовка читалась как «\ufefftarget_type»).
func TestImportLabelsWithBOM(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "labels.csv")
	content := "\ufefftarget_type,target_id,ads,trader_seen,kpizlog_seen,reviews,user_created,sample_title,label,note\n" +
		"seller,12345,3,1,0,10,2020-01-01 10:00:00,Test laptop,SHOP,явный магазин\n"
	if err := os.WriteFile(csvPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := ensureTables(ctx, db); err != nil {
		t.Fatal(err)
	}

	importLabels(ctx, db, csvPath)

	var label string
	if err := db.QueryRowContext(ctx,
		`SELECT label FROM labels WHERE target_type='seller' AND target_id=12345`).Scan(&label); err != nil {
		t.Fatalf("разметка не импортирована: %v", err)
	}
	if label != "SHOP" {
		t.Fatalf("label = %q, want SHOP", label)
	}
}
