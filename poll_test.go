package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"kpbot/models"
	"kpbot/notifier"
	"kpbot/storage"
)

func TestShouldFetchNextLiveSearchPage(t *testing.T) {
	withAds := []models.SearchAd{{AdID: 1}}
	cases := []struct {
		name     string
		res      *models.SearchResults
		page     int
		maxPages int
		want     bool
	}{
		{"nil result", nil, 1, 2, false},
		{"next page allowed", &models.SearchResults{Ads: withAds, Pages: 3}, 1, 2, true},
		{"configured limit reached", &models.SearchResults{Ads: withAds, Pages: 3}, 2, 2, false},
		{"kp pages reached", &models.SearchResults{Ads: withAds, Pages: 1}, 1, 5, false},
		{"empty page", &models.SearchResults{Ads: nil, Pages: 3}, 1, 5, false},
		{"kp max flag", &models.SearchResults{Ads: withAds, Pages: 3, HasReachedMax: true}, 1, 5, false},
		{"kp limit flag", &models.SearchResults{Ads: withAds, Pages: 3, HasReachedLimit: true}, 1, 5, false},
		{"bad config", &models.SearchResults{Ads: withAds, Pages: 3}, 1, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldFetchNextLiveSearchPage(c.res, c.page, c.maxPages); got != c.want {
				t.Fatalf("shouldFetchNextLiveSearchPage = %v, want %v", got, c.want)
			}
		})
	}
}

func TestDrainTelegramOutboxDisabledKeepsPending(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	listing := models.Listing{
		AdID:      42,
		Title:     "Laptop",
		Price:     200,
		Currency:  "EUR",
		URL:       "https://kp/42",
		CreatedAt: time.Now(),
	}
	if err := store.UpsertDiscovered(ctx, listing); err != nil {
		t.Fatalf("upsert discovered: %v", err)
	}
	verdict := storage.FunnelVerdict{Code: "DIAMOND", Deviation: -0.2, GroupN: 8, Reason: "test", Specs: "i7"}
	if err := store.SaveFunnelAlertPending(ctx, 42, verdict, "<b>alert</b>", "https://kp/42", models.StatusAlerted); err != nil {
		t.Fatalf("save pending alert: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	drainTelegramOutbox(ctx, store, notifier.New("", ""), log, &botState{}, 10)

	pending, err := store.PendingTelegramOutbox(ctx)
	if err != nil {
		t.Fatalf("pending outbox: %v", err)
	}
	if pending != 1 {
		t.Fatalf("pending outbox = %d, want 1", pending)
	}
	alerts, err := store.RecentAlerts(ctx, time.Now().Add(-time.Hour), 10)
	if err != nil {
		t.Fatalf("recent alerts: %v", err)
	}
	if len(alerts) != 0 {
		t.Fatalf("disabled Telegram must not mark alerts delivered: %+v", alerts)
	}
}
