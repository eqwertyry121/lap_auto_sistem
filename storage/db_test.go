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
	if name != "storage-main-v4" {
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
	var status, process string
	if err := st.db.QueryRowContext(ctx, `SELECT status, process_state FROM market_listings WHERE ad_id = 1`).Scan(&status, &process); err != nil {
		t.Fatalf("read listing state: %v", err)
	}
	if status != string(models.StatusNew) || process != string(models.ProcessDetailPending) {
		t.Fatalf("status=%s process=%s, want NEW/DETAIL_PENDING", status, process)
	}
}

func TestDiscoveredListingPreservesUserIDForSellerFilters(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	l := models.Listing{
		AdID: 77, UserID: 12345, Title: "Laptop", Price: 200, Currency: "EUR", URL: "https://kp/77",
		CreatedAt: time.Now(),
	}
	if err := st.UpsertDiscovered(ctx, l); err != nil {
		t.Fatalf("upsert discovered: %v", err)
	}

	withoutUser := l
	withoutUser.UserID = 0
	withoutUser.Price = 190
	if err := st.UpsertDiscovered(ctx, withoutUser); err != nil {
		t.Fatalf("upsert discovered without user id: %v", err)
	}

	got, err := st.ClaimDueListings(ctx, []models.ProcessState{models.ProcessDetailPending}, 10, time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("claimed %+v, want one listing", got)
	}
	if got[0].UserID != 12345 {
		t.Fatalf("claimed UserID = %d, want preserved seller id 12345", got[0].UserID)
	}
	if got[0].Price != 190 {
		t.Fatalf("claimed Price = %.0f, want latest price 190", got[0].Price)
	}
}

func TestProcessStateDoesNotOverwriteBusinessStatus(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	l := models.Listing{
		AdID: 12, Title: "Laptop", Price: 300, Currency: "EUR", URL: "https://kp/12",
		CreatedAt: time.Now(),
	}
	if err := st.UpsertDiscovered(ctx, l); err != nil {
		t.Fatalf("upsert discovered: %v", err)
	}
	if err := st.MarkProcessState(ctx, 12, models.ProcessEvaluating); err != nil {
		t.Fatalf("mark evaluating: %v", err)
	}
	var status, process string
	if err := st.db.QueryRowContext(ctx, `SELECT status, process_state FROM market_listings WHERE ad_id = 12`).Scan(&status, &process); err != nil {
		t.Fatalf("read evaluating: %v", err)
	}
	if status != string(models.StatusNew) || process != string(models.ProcessEvaluating) {
		t.Fatalf("status=%s process=%s, want NEW/EVALUATING", status, process)
	}
	if err := st.RetryProcess(ctx, 12, models.ProcessEvaluating, "temporary error"); err != nil {
		t.Fatalf("retry evaluating: %v", err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT status, process_state FROM market_listings WHERE ad_id = 12`).Scan(&status, &process); err != nil {
		t.Fatalf("read retry: %v", err)
	}
	if status != string(models.StatusNew) || process != string(models.ProcessEvaluating) {
		t.Fatalf("status=%s process=%s after retry, want NEW/EVALUATING", status, process)
	}
}

func TestRetryProcessKeepsTransientFailuresRetryable(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	l := models.Listing{
		AdID: 16, Title: "Laptop", Price: 300, Currency: "EUR", URL: "https://kp/16",
		CreatedAt: time.Now(),
	}
	if err := st.UpsertDiscovered(ctx, l); err != nil {
		t.Fatalf("upsert discovered: %v", err)
	}

	for i := 0; i < 8; i++ {
		if err := st.RetryProcess(ctx, 16, models.ProcessDetailPending, "kp temporarily unavailable"); err != nil {
			t.Fatalf("retry %d: %v", i+1, err)
		}
	}

	var status, process, lastErr string
	var attempts int
	var nextAttemptAt, leaseUntil int64
	if err := st.db.QueryRowContext(ctx, `
SELECT status, process_state, attempt_count, next_attempt_at, lease_until, last_error
FROM market_listings
WHERE ad_id = 16`).Scan(&status, &process, &attempts, &nextAttemptAt, &leaseUntil, &lastErr); err != nil {
		t.Fatalf("read listing state: %v", err)
	}
	if status != string(models.StatusNew) || process != string(models.ProcessDetailPending) {
		t.Fatalf("status=%s process=%s, want NEW/DETAIL_PENDING", status, process)
	}
	if attempts != 8 {
		t.Fatalf("attempt_count = %d, want 8", attempts)
	}
	if nextAttemptAt <= time.Now().Unix() {
		t.Fatalf("next_attempt_at = %d, want future retry", nextAttemptAt)
	}
	if leaseUntil != 0 {
		t.Fatalf("lease_until = %d, want released lease", leaseUntil)
	}
	if lastErr != "kp temporarily unavailable" {
		t.Fatalf("last_error = %q", lastErr)
	}
}

func TestMarkDeadIsExplicitPermanentFailure(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	l := models.Listing{
		AdID: 17, Title: "Removed Laptop", Price: 300, Currency: "EUR", URL: "https://kp/17",
		CreatedAt: time.Now(),
	}
	if err := st.UpsertDiscovered(ctx, l); err != nil {
		t.Fatalf("upsert discovered: %v", err)
	}
	if err := st.MarkDead(ctx, 17, "listing not found"); err != nil {
		t.Fatalf("mark dead: %v", err)
	}

	var status, process, lastErr string
	if err := st.db.QueryRowContext(ctx, `
SELECT status, process_state, last_error
FROM market_listings
WHERE ad_id = 17`).Scan(&status, &process, &lastErr); err != nil {
		t.Fatalf("read listing state: %v", err)
	}
	if status != string(models.StatusError) || process != string(models.ProcessDead) {
		t.Fatalf("status=%s process=%s, want ERROR/DEAD", status, process)
	}
	if lastErr != "listing not found" {
		t.Fatalf("last_error = %q", lastErr)
	}
}

func TestStorageMigrationNormalizesActiveProcessStatuses(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	if _, err := st.db.ExecContext(ctx, `
INSERT INTO market_listings (ad_id, title, status, process_state, created_at)
VALUES (13, 'old active row', 'EVALUATING', 'EVALUATING', ?)`, time.Now().Unix()); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
INSERT INTO market_listings (ad_id, title, status, process_state, created_at)
VALUES (14, 'old retry row', 'ERROR', 'DETAIL_PENDING', ?)`, time.Now().Unix()); err != nil {
		t.Fatalf("insert legacy retry row: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
INSERT INTO market_listings (ad_id, title, status, process_state, created_at)
VALUES (15, 'dead row', 'ERROR', 'DEAD', ?)`, time.Now().Unix()); err != nil {
		t.Fatalf("insert dead row: %v", err)
	}
	if err := applyStorageSchema(st.db); err != nil {
		t.Fatalf("reapply schema: %v", err)
	}
	cases := []struct {
		adID        int64
		wantStatus  string
		wantProcess string
	}{
		{13, string(models.StatusNew), string(models.ProcessEvaluating)},
		{14, string(models.StatusNew), string(models.ProcessDetailPending)},
		{15, string(models.StatusError), string(models.ProcessDead)},
	}
	for _, c := range cases {
		var status, process string
		if err := st.db.QueryRowContext(ctx, `SELECT status, process_state FROM market_listings WHERE ad_id = ?`, c.adID).Scan(&status, &process); err != nil {
			t.Fatalf("read migrated row %d: %v", c.adID, err)
		}
		if status != c.wantStatus || process != c.wantProcess {
			t.Fatalf("ad %d status=%s process=%s, want %s/%s", c.adID, status, process, c.wantStatus, c.wantProcess)
		}
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
	if status != string(models.StatusNew) || process != string(models.ProcessAlertPending) {
		t.Fatalf("status=%s process=%s, want NEW/ALERT_PENDING before delivery", status, process)
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
