package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadSellerInfoCountsRecentAds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE sellers (
	user_id INTEGER PRIMARY KEY,
	ads_seen INTEGER NOT NULL DEFAULT 0,
	trader_seen INTEGER NOT NULL DEFAULT 0,
	kpizlog_seen INTEGER NOT NULL DEFAULT 0,
	reviews INTEGER NOT NULL DEFAULT 0,
	user_created TEXT NOT NULL DEFAULT ''
);
CREATE TABLE research_ads (
	user_id INTEGER NOT NULL DEFAULT 0,
	fetched_at INTEGER NOT NULL DEFAULT 0
)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	old := time.Now().Add(-45 * 24 * time.Hour).Unix()
	if _, err := db.Exec(`INSERT INTO sellers (user_id, ads_seen, trader_seen, kpizlog_seen, reviews, user_created) VALUES (1, 1, 0, 0, 7, '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO research_ads (user_id, fetched_at) VALUES (1, ?), (1, ?), (1, ?)`, now, now, old); err != nil {
		t.Fatal(err)
	}

	info, err := LoadSellerInfo(context.Background(), path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if info.AdsCount != 3 {
		t.Fatalf("AdsCount = %d, want 3", info.AdsCount)
	}
	if info.RecentAdsCount != 2 {
		t.Fatalf("RecentAdsCount = %d, want 2", info.RecentAdsCount)
	}
}
