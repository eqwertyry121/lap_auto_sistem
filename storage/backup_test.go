package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"kpbot/models"
)

func TestBackupDailyCreatesConsistentBackupAndSkipsExisting(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	listing := models.Listing{
		AdID:      42,
		Title:     "Backup Laptop",
		Price:     321,
		Currency:  "EUR",
		URL:       "https://kp/42",
		CreatedAt: time.Now(),
	}
	if err := st.UpsertDiscovered(ctx, listing); err != nil {
		t.Fatalf("upsert discovered: %v", err)
	}

	backupDir := t.TempDir()
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	res, err := st.BackupDaily(ctx, backupDir, now)
	if err != nil {
		t.Fatalf("backup daily: %v", err)
	}
	if !res.Created {
		t.Fatal("first backup was not created")
	}
	if filepath.Base(res.Path) != "test-20260811.db" {
		t.Fatalf("backup path = %q", res.Path)
	}

	db, err := sql.Open("sqlite", res.Path)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM market_listings WHERE ad_id = 42`).Scan(&count); err != nil {
		t.Fatalf("query backup: %v", err)
	}
	if count != 1 {
		t.Fatalf("backup listing count = %d, want 1", count)
	}

	res2, err := st.BackupDaily(ctx, backupDir, now)
	if err != nil {
		t.Fatalf("second backup daily: %v", err)
	}
	if res2.Created {
		t.Fatal("second same-day backup should be skipped")
	}
	if res2.Path != res.Path {
		t.Fatalf("second backup path = %q, want %q", res2.Path, res.Path)
	}
}

func TestBackupFileNameSanitizesDatabaseName(t *testing.T) {
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	got := backupFileName(filepath.Join("data", "kp bot:main.sqlite"), now)
	if got != "kp_bot_main-20260811.sqlite" {
		t.Fatalf("backup file name = %q", got)
	}
}
