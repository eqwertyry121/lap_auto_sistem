package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kpbot/models"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestOpenConfiguresSQLitePragmasAndIntegrityCheck(t *testing.T) {
	st := openTestStore(t)

	var journalMode string
	if err := st.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if strings.ToLower(journalMode) != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}

	var busyTimeout int
	if err := st.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", busyTimeout)
	}

	var foreignKeys int
	if err := st.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}

	if err := checkSQLiteIntegrity(st.db); err != nil {
		t.Fatalf("integrity check: %v", err)
	}
}

func TestDiscoveredListingCanBeClaimedAfterExistingRow(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	l := models.Listing{
		AdID: 1, Title: "Laptop", Price: 200, Currency: "EUR", URL: "https://kp/1",
		CreatedAt: time.Now(),
	}
	if err := st.UpsertDiscovered(ctx, l); err != nil {
		t.Fatalf("upsert discovered: %v", err)
	}
	if err := st.UpsertDiscovered(ctx, l); err != nil {
		t.Fatalf("second upsert discovered: %v", err)
	}
	got, err := st.ClaimDueListings(ctx, []models.ProcessState{models.ProcessDetailPending}, 10, time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(got) != 1 || got[0].AdID != 1 {
		t.Fatalf("claimed %+v, want one listing 1", got)
	}
	byState, err := st.CountByProcessState(ctx)
	if err != nil {
		t.Fatalf("count by process state: %v", err)
	}
	if byState[string(models.ProcessDetailPending)] != 1 {
		t.Fatalf("process states = %+v, want one DETAIL_PENDING", byState)
	}
}

func TestFunnelAlertPendingBecomesAlertedOnlyAfterOutboxSent(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	l := models.Listing{
		AdID: 2, Title: "Laptop", Price: 200, Currency: "EUR", URL: "https://kp/2",
		CreatedAt: time.Now(),
	}
	if err := st.UpsertDiscovered(ctx, l); err != nil {
		t.Fatalf("upsert discovered: %v", err)
	}
	v := FunnelVerdict{Code: "DIAMOND", Deviation: -0.2, GroupN: 8, Reason: "test", Specs: "i7"}
	if err := st.SaveFunnelAlertPending(ctx, 2, v, "<b>alert</b>", "https://kp/2", models.StatusAlerted); err != nil {
		t.Fatalf("save pending: %v", err)
	}
	var status, process string
	if err := st.db.QueryRowContext(ctx, `SELECT status, process_state FROM market_listings WHERE ad_id = 2`).Scan(&status, &process); err != nil {
		t.Fatalf("read listing: %v", err)
	}
	if status == string(models.StatusAlerted) || process != string(models.ProcessAlertPending) {
		t.Fatalf("status=%s process=%s, want not-alerted pending", status, process)
	}
	items, err := st.ClaimTelegramOutbox(ctx, 10, time.Minute)
	if err != nil {
		t.Fatalf("claim outbox: %v", err)
	}
	if len(items) != 1 || items[0].AdID != 2 {
		t.Fatalf("outbox items %+v, want one item for ad 2", items)
	}
	if err := st.MarkTelegramOutboxSent(ctx, items[0], 123); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT status, process_state FROM market_listings WHERE ad_id = 2`).Scan(&status, &process); err != nil {
		t.Fatalf("read listing after send: %v", err)
	}
	if status != string(models.StatusAlerted) || process != string(models.ProcessDone) {
		t.Fatalf("status=%s process=%s, want ALERTED/DONE", status, process)
	}
	last, err := st.LastTelegramDelivery(ctx)
	if err != nil {
		t.Fatalf("last telegram delivery: %v", err)
	}
	if last.IsZero() || time.Since(last) > time.Minute {
		t.Fatalf("last telegram delivery = %s", last)
	}
}
