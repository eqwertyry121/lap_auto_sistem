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

func TestOpenRecordsSchemaMigrationVersion(t *testing.T) {
	st := openTestStore(t)

	var name string
	var appliedAt int64
	if err := st.db.QueryRow(
		`SELECT name, applied_at FROM schema_migrations WHERE version = ?`,
		storageSchemaVersion,
	).Scan(&name, &appliedAt); err != nil {
		t.Fatalf("read schema migration: %v", err)
	}
	if name != "storage-main-v2" {
		t.Fatalf("migration name = %q", name)
	}
	if appliedAt <= 0 {
		t.Fatalf("applied_at = %d", appliedAt)
	}
	version, err := st.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if version != storageSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, storageSchemaVersion)
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

func TestUpsertDiscoveredRecordsObservationHistoryAndPriceChanges(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	firstSeen := time.Unix(1000, 0)
	lastSeen := time.Unix(2000, 0)
	l := models.Listing{
		AdID: 10, Title: "Dell Latitude 3510 i7", Price: 200, Currency: "eur", URL: "https://kp/10",
		CreatedAt: firstSeen,
	}
	if err := st.UpsertDiscovered(ctx, l); err != nil {
		t.Fatalf("upsert discovered: %v", err)
	}
	l.Price = 220
	l.CreatedAt = lastSeen
	if err := st.UpsertDiscovered(ctx, l); err != nil {
		t.Fatalf("second upsert discovered: %v", err)
	}

	count, err := st.ObservationCount(ctx, 10)
	if err != nil {
		t.Fatalf("observation count: %v", err)
	}
	if count != 2 {
		t.Fatalf("observation count = %d, want 2", count)
	}
	lifecycle, err := st.ListingLifecycle(ctx, 10)
	if err != nil {
		t.Fatalf("listing lifecycle: %v", err)
	}
	if lifecycle.FirstSeen.Unix() != firstSeen.Unix() || lifecycle.LastSeen.Unix() != lastSeen.Unix() {
		t.Fatalf("lifecycle seen range = %d..%d, want %d..%d",
			lifecycle.FirstSeen.Unix(), lifecycle.LastSeen.Unix(), firstSeen.Unix(), lastSeen.Unix())
	}
	if lifecycle.LastPrice != 220 || lifecycle.LastCurrency != "EUR" {
		t.Fatalf("last price = %.0f %s, want 220 EUR", lifecycle.LastPrice, lifecycle.LastCurrency)
	}
	if lifecycle.SeenCount != 2 || lifecycle.PriceChangeCount != 1 {
		t.Fatalf("seen=%d price_changes=%d, want 2 and 1", lifecycle.SeenCount, lifecycle.PriceChangeCount)
	}
	if lifecycle.LastFingerprint == "" {
		t.Fatal("empty listing fingerprint")
	}
}

func TestUpdateDetailsRecordsDetailObservation(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	l := models.Listing{
		AdID: 11, Title: "Lenovo ThinkPad E14 Gen 6", Price: 680, Currency: "EUR", URL: "https://kp/11",
		CreatedAt: time.Unix(1000, 0),
	}
	if err := st.UpsertDiscovered(ctx, l); err != nil {
		t.Fatalf("upsert discovered: %v", err)
	}
	if err := st.UpdateDetails(ctx, 11, "Ryzen 7, 16GB, 512GB", "Marko", 682, "eur"); err != nil {
		t.Fatalf("update details: %v", err)
	}

	count, err := st.ObservationCount(ctx, 11)
	if err != nil {
		t.Fatalf("observation count: %v", err)
	}
	if count != 2 {
		t.Fatalf("observation count = %d, want 2", count)
	}
	lifecycle, err := st.ListingLifecycle(ctx, 11)
	if err != nil {
		t.Fatalf("listing lifecycle: %v", err)
	}
	if lifecycle.LastPrice != 682 || lifecycle.PriceChangeCount != 1 || lifecycle.LastCurrency != "EUR" {
		t.Fatalf("lifecycle after detail = %+v, want price 682 EUR and one price change", lifecycle)
	}
}

func TestListingLifecycleDetectsPotentialRelistFingerprint(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	first := models.Listing{
		AdID: 21, Title: "Dell Latitude 3510 i7-10510U Nvidia MX230", Price: 210, Currency: "EUR", URL: "https://kp/21",
		CreatedAt: time.Unix(1000, 0),
	}
	second := models.Listing{
		AdID: 22, Title: "Dell Latitude 3510 i7 10510U NVIDIA MX230", Price: 200, Currency: "EUR", URL: "https://kp/22",
		CreatedAt: time.Unix(2000, 0),
	}
	if err := st.UpsertDiscovered(ctx, first); err != nil {
		t.Fatalf("upsert first: %v", err)
	}
	if err := st.UpsertDiscovered(ctx, second); err != nil {
		t.Fatalf("upsert second: %v", err)
	}
	firstLifecycle, err := st.ListingLifecycle(ctx, 21)
	if err != nil {
		t.Fatalf("first lifecycle: %v", err)
	}
	secondLifecycle, err := st.ListingLifecycle(ctx, 22)
	if err != nil {
		t.Fatalf("second lifecycle: %v", err)
	}
	if firstLifecycle.LastFingerprint == "" || firstLifecycle.LastFingerprint != secondLifecycle.LastFingerprint {
		t.Fatalf("fingerprints = %q and %q, want same non-empty fingerprint",
			firstLifecycle.LastFingerprint, secondLifecycle.LastFingerprint)
	}
	if secondLifecycle.RelistCount != 1 {
		t.Fatalf("second relist count = %d, want 1", secondLifecycle.RelistCount)
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
