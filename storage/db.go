package storage

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite"

	"kpbot/models"
)

// Store — единая точка доступа к БД (SSOT). SQLite на MVP, при переезде
// на PostgreSQL меняется только этот файл (драйвер и плейсхолдеры).
type Store struct {
	db   *sql.DB
	path string
}

const storageSchemaVersion = 5

const schema = `
CREATE TABLE IF NOT EXISTS schema_migrations (
	version    INTEGER PRIMARY KEY,
	name       TEXT NOT NULL DEFAULT '',
	applied_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS market_listings (
	ad_id             INTEGER PRIMARY KEY,
	user_id           INTEGER NOT NULL DEFAULT 0,
	title             TEXT NOT NULL DEFAULT '',
	price             REAL NOT NULL DEFAULT 0,
	currency          TEXT NOT NULL DEFAULT 'EUR',
	url               TEXT NOT NULL DEFAULT '',
	search_condition  TEXT NOT NULL DEFAULT '',
	exchange          INTEGER NOT NULL DEFAULT 0,
	kp_izlog          INTEGER NOT NULL DEFAULT 0,
	is_renewed        INTEGER NOT NULL DEFAULT 0,
	description_snip  TEXT NOT NULL DEFAULT '',
	description       TEXT NOT NULL DEFAULT '',
	seller            TEXT NOT NULL DEFAULT '',
	status            TEXT NOT NULL DEFAULT 'NEW',
	is_deal           INTEGER NOT NULL DEFAULT 0,
	need_check        INTEGER NOT NULL DEFAULT 0,
	model_found       INTEGER NOT NULL DEFAULT 0,
	estimated_profit  REAL NOT NULL DEFAULT 0,
	reason            TEXT NOT NULL DEFAULT '',
	specs             TEXT NOT NULL DEFAULT '',
	synced_to_sheets  INTEGER NOT NULL DEFAULT 0,
	created_at        INTEGER NOT NULL DEFAULT 0,
	process_state     TEXT NOT NULL DEFAULT '',
	attempt_count     INTEGER NOT NULL DEFAULT 0,
	next_attempt_at   INTEGER NOT NULL DEFAULT 0,
	lease_until       INTEGER NOT NULL DEFAULT 0,
	last_error        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_ml_status  ON market_listings(status);
CREATE INDEX IF NOT EXISTS idx_ml_synced  ON market_listings(synced_to_sheets);
CREATE INDEX IF NOT EXISTS idx_ml_created ON market_listings(created_at);

CREATE TABLE IF NOT EXISTS listing_observations (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	ad_id        INTEGER NOT NULL DEFAULT 0,
	observed_at  INTEGER NOT NULL DEFAULT 0,
	price        REAL NOT NULL DEFAULT 0,
	currency     TEXT NOT NULL DEFAULT 'EUR',
	title        TEXT NOT NULL DEFAULT '',
	url          TEXT NOT NULL DEFAULT '',
	fingerprint  TEXT NOT NULL DEFAULT '',
	source       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_lo_ad_time ON listing_observations(ad_id, observed_at);
CREATE INDEX IF NOT EXISTS idx_lo_fingerprint ON listing_observations(fingerprint);

CREATE TABLE IF NOT EXISTS listing_lifecycle (
	ad_id                INTEGER PRIMARY KEY,
	first_seen           INTEGER NOT NULL DEFAULT 0,
	last_seen            INTEGER NOT NULL DEFAULT 0,
	last_price           REAL NOT NULL DEFAULT 0,
	last_currency        TEXT NOT NULL DEFAULT '',
	last_fingerprint     TEXT NOT NULL DEFAULT '',
	seen_count           INTEGER NOT NULL DEFAULT 0,
	price_change_count   INTEGER NOT NULL DEFAULT 0,
	relist_count         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_ll_last_seen ON listing_lifecycle(last_seen);
CREATE INDEX IF NOT EXISTS idx_ll_fingerprint ON listing_lifecycle(last_fingerprint);

CREATE TABLE IF NOT EXISTS telegram_outbox (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	ad_id           INTEGER NOT NULL DEFAULT 0,
	kind            TEXT NOT NULL DEFAULT '',
	text            TEXT NOT NULL DEFAULT '',
	url             TEXT NOT NULL DEFAULT '',
	final_status    TEXT NOT NULL DEFAULT '',
	attempt_count   INTEGER NOT NULL DEFAULT 0,
	next_attempt_at INTEGER NOT NULL DEFAULT 0,
	lease_until     INTEGER NOT NULL DEFAULT 0,
	last_error      TEXT NOT NULL DEFAULT '',
	message_id      INTEGER NOT NULL DEFAULT 0,
	created_at      INTEGER NOT NULL DEFAULT 0,
	sent_at         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_tgo_due ON telegram_outbox(sent_at, next_attempt_at, lease_until);
`

func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("создание каталога БД: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: один писатель, без SQLITE_BUSY
	db.SetMaxIdleConns(1)
	if err := configureSQLite(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite setup: %w", err)
	}
	if err := applyStorageSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := checkSQLiteIntegrity(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, path: path}, nil
}

type schemaRunner interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
}

func applyStorageSchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin schema migration: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(schema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if err := migrateListingAudit(tx); err != nil {
		return fmt.Errorf("migrate listing audit: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		storageSchemaVersion, "storage-main-v5", time.Now().Unix(),
	); err != nil {
		return fmt.Errorf("record schema migration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema migration: %w", err)
	}
	return nil
}

// migrateListingAudit — колонки аудита вердиктов воронки (PLAN_v4 §4.4).
// ALTER TABLE без IF NOT EXISTS — проверяем pragma'ми.
func migrateListingAudit(db schemaRunner) error {
	cols := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(market_listings)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		cols[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, m := range []struct{ col, stmt string }{
		{"user_id", `ALTER TABLE market_listings ADD COLUMN user_id INTEGER NOT NULL DEFAULT 0`},
		{"search_condition", `ALTER TABLE market_listings ADD COLUMN search_condition TEXT NOT NULL DEFAULT ''`},
		{"exchange", `ALTER TABLE market_listings ADD COLUMN exchange INTEGER NOT NULL DEFAULT 0`},
		{"kp_izlog", `ALTER TABLE market_listings ADD COLUMN kp_izlog INTEGER NOT NULL DEFAULT 0`},
		{"is_renewed", `ALTER TABLE market_listings ADD COLUMN is_renewed INTEGER NOT NULL DEFAULT 0`},
		{"description_snip", `ALTER TABLE market_listings ADD COLUMN description_snip TEXT NOT NULL DEFAULT ''`},
		{"verdict_code", `ALTER TABLE market_listings ADD COLUMN verdict_code TEXT NOT NULL DEFAULT ''`},
		{"deviation", `ALTER TABLE market_listings ADD COLUMN deviation REAL NOT NULL DEFAULT 0`},
		{"group_n", `ALTER TABLE market_listings ADD COLUMN group_n INTEGER NOT NULL DEFAULT 0`},
		{"alternatives", `ALTER TABLE market_listings ADD COLUMN alternatives TEXT NOT NULL DEFAULT ''`},
		{"process_state", `ALTER TABLE market_listings ADD COLUMN process_state TEXT NOT NULL DEFAULT ''`},
		{"attempt_count", `ALTER TABLE market_listings ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 0`},
		{"next_attempt_at", `ALTER TABLE market_listings ADD COLUMN next_attempt_at INTEGER NOT NULL DEFAULT 0`},
		{"lease_until", `ALTER TABLE market_listings ADD COLUMN lease_until INTEGER NOT NULL DEFAULT 0`},
		{"last_error", `ALTER TABLE market_listings ADD COLUMN last_error TEXT NOT NULL DEFAULT ''`},
	} {
		if cols[m.col] {
			continue
		}
		if _, err := db.Exec(m.stmt); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`
UPDATE market_listings
SET process_state = 'DETAIL_PENDING', next_attempt_at = 0, lease_until = 0
WHERE process_state = '' AND status IN ('NEW', 'ERROR');
UPDATE market_listings
SET process_state = 'ALERT_PENDING', next_attempt_at = 0, lease_until = 0
WHERE process_state = '' AND status = 'ALERT_PENDING';
UPDATE market_listings
SET process_state = 'DONE'
WHERE process_state = '' AND status IN ('SCANNED','SKIPPED_SPAM','SKIPPED_BAN','ALERTED','NEED_CHECK','NO_DEAL');
UPDATE market_listings
SET status = 'NEW'
WHERE status IN ('DISCOVERED','DETAIL_PENDING','ENRICH_PENDING','EVALUATING','ALERT_PENDING')
   OR (status = 'ERROR' AND process_state IN ('DISCOVERED','DETAIL_PENDING','ENRICH_PENDING','EVALUATING','ALERT_PENDING'));
`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_ml_process_due ON market_listings(process_state, next_attempt_at, lease_until)`); err != nil {
		return err
	}
	return nil
}

func configureSQLite(db *sql.DB) error {
	for _, stmt := range []string{
		`PRAGMA busy_timeout=5000`,
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA foreign_keys=ON`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func checkSQLiteIntegrity(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("sqlite integrity_check: %w", err)
	}
	defer rows.Close()

	var problems []string
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return fmt.Errorf("sqlite integrity_check scan: %w", err)
		}
		if strings.TrimSpace(strings.ToLower(result)) != "ok" {
			problems = append(problems, result)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlite integrity_check rows: %w", err)
	}
	if len(problems) > 0 {
		return fmt.Errorf("sqlite integrity_check failed: %s", strings.Join(problems, "; "))
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Exists(ctx context.Context, adID int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM market_listings WHERE ad_id = ?`, adID).Scan(&n)
	return n > 0, err
}

// InsertListing пишет новый лот (INSERT OR IGNORE — повторная вставка не ошибка).
func (s *Store) InsertListing(ctx context.Context, l models.Listing) error {
	if l.CreatedAt.IsZero() {
		l.CreatedAt = time.Now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO market_listings (
			ad_id, user_id, title, price, currency, url, search_condition, exchange, kp_izlog, is_renewed, description_snip,
			status, created_at, process_state, next_attempt_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.AdID, l.UserID, l.Title, l.Price, models.NormalizeCurrency(l.Currency), l.URL,
		l.Condition, boolInt(l.Exchange), boolInt(l.KPIzlog), boolInt(l.IsRenewed), l.DescriptionSnip,
		string(l.Status), l.CreatedAt.Unix(), string(l.ProcessState), unixOrZero(l.NextAttemptAt)); err != nil {
		return err
	}
	if err := recordListingObservationTx(ctx, tx, l, "search", l.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetStatus(ctx context.Context, adID int64, st models.Status) error {
	_, err := s.db.ExecContext(ctx, `UPDATE market_listings SET status = ? WHERE ad_id = ?`, string(st), adID)
	return err
}

func (s *Store) UpsertDiscovered(ctx context.Context, l models.Listing) error {
	if l.CreatedAt.IsZero() {
		l.CreatedAt = time.Now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var status, state string
	err = tx.QueryRowContext(ctx, `SELECT status, process_state FROM market_listings WHERE ad_id = ?`, l.AdID).Scan(&status, &state)
	if err == sql.ErrNoRows {
		_, err = tx.ExecContext(ctx, `
INSERT INTO market_listings (
	ad_id, user_id, title, price, currency, url, search_condition, exchange, kp_izlog, is_renewed, description_snip,
	status, created_at, process_state, next_attempt_at, lease_until
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0)`,
			l.AdID, l.UserID, l.Title, l.Price, models.NormalizeCurrency(l.Currency), l.URL,
			l.Condition, boolInt(l.Exchange), boolInt(l.KPIzlog), boolInt(l.IsRenewed), l.DescriptionSnip,
			string(models.StatusNew), l.CreatedAt.Unix(), string(models.ProcessDetailPending))
		if err != nil {
			return err
		}
		if err := recordListingObservationTx(ctx, tx, l, "search", l.CreatedAt); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err != nil {
		return err
	}

	if listingClosed(status, state) {
		_, err = tx.ExecContext(ctx,
			`UPDATE market_listings
SET user_id = CASE WHEN ? != 0 THEN ? ELSE user_id END,
    title = ?, price = ?, currency = ?, url = ?,
    search_condition = CASE WHEN ? != '' THEN ? ELSE search_condition END,
    exchange = CASE WHEN ? != 0 THEN 1 ELSE exchange END,
    kp_izlog = CASE WHEN ? != 0 THEN 1 ELSE kp_izlog END,
    is_renewed = CASE WHEN ? != 0 THEN 1 ELSE is_renewed END,
    description_snip = CASE WHEN ? != '' THEN ? ELSE description_snip END
WHERE ad_id = ?`,
			l.UserID, l.UserID, l.Title, l.Price, models.NormalizeCurrency(l.Currency), l.URL,
			l.Condition, l.Condition, boolInt(l.Exchange), boolInt(l.KPIzlog), boolInt(l.IsRenewed),
			l.DescriptionSnip, l.DescriptionSnip, l.AdID)
	} else {
		nextState := state
		if nextState == "" || nextState == string(models.ProcessDiscovered) {
			nextState = string(models.ProcessDetailPending)
		}
		nextStatus := status
		if nextStatus == "" || nextStatus == string(models.StatusError) || activeProcessStatus(nextStatus) {
			nextStatus = string(models.StatusNew)
		}
		_, err = tx.ExecContext(ctx, `
UPDATE market_listings
SET user_id = CASE WHEN ? != 0 THEN ? ELSE user_id END,
    title = ?, price = ?, currency = ?, url = ?,
    search_condition = CASE WHEN ? != '' THEN ? ELSE search_condition END,
    exchange = CASE WHEN ? != 0 THEN 1 ELSE exchange END,
    kp_izlog = CASE WHEN ? != 0 THEN 1 ELSE kp_izlog END,
    is_renewed = CASE WHEN ? != 0 THEN 1 ELSE is_renewed END,
    description_snip = CASE WHEN ? != '' THEN ? ELSE description_snip END,
    status = ?, process_state = ?
WHERE ad_id = ?`,
			l.UserID, l.UserID, l.Title, l.Price, models.NormalizeCurrency(l.Currency), l.URL,
			l.Condition, l.Condition, boolInt(l.Exchange), boolInt(l.KPIzlog), boolInt(l.IsRenewed),
			l.DescriptionSnip, l.DescriptionSnip, nextStatus, nextState, l.AdID)
	}
	if err != nil {
		return err
	}
	if err := recordListingObservationTx(ctx, tx, l, "search", l.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func listingClosed(status, state string) bool {
	switch state {
	case string(models.ProcessDone), string(models.ProcessDead), string(models.ProcessAlertPending):
		return true
	}
	switch status {
	case string(models.StatusScanned), string(models.StatusSkippedSpam), string(models.StatusSkippedBan),
		string(models.StatusAlerted), string(models.StatusNeedCheck), string(models.StatusNoDeal):
		return true
	}
	return false
}

func activeProcessStatus(status string) bool {
	switch status {
	case string(models.ProcessDiscovered), string(models.ProcessDetailPending), string(models.ProcessEnrichPending),
		string(models.ProcessEvaluating), string(models.ProcessAlertPending):
		return true
	default:
		return false
	}
}

type ListingLifecycle struct {
	AdID             int64
	FirstSeen        time.Time
	LastSeen         time.Time
	LastPrice        float64
	LastCurrency     string
	LastFingerprint  string
	SeenCount        int
	PriceChangeCount int
	RelistCount      int
}

func (s *Store) ListingLifecycle(ctx context.Context, adID int64) (ListingLifecycle, error) {
	var lc ListingLifecycle
	var firstSeen, lastSeen int64
	err := s.db.QueryRowContext(ctx, `
SELECT ad_id, first_seen, last_seen, last_price, last_currency, last_fingerprint,
       seen_count, price_change_count, relist_count
FROM listing_lifecycle
WHERE ad_id = ?`, adID).Scan(
		&lc.AdID, &firstSeen, &lastSeen, &lc.LastPrice, &lc.LastCurrency, &lc.LastFingerprint,
		&lc.SeenCount, &lc.PriceChangeCount, &lc.RelistCount,
	)
	if err != nil {
		return lc, err
	}
	lc.FirstSeen = timeFromUnix(firstSeen)
	lc.LastSeen = timeFromUnix(lastSeen)
	return lc, nil
}

func (s *Store) ObservationCount(ctx context.Context, adID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM listing_observations WHERE ad_id = ?`, adID).Scan(&n)
	return n, err
}

func recordListingObservationTx(ctx context.Context, tx *sql.Tx, l models.Listing, source string, observedAt time.Time) error {
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	currency := models.NormalizeCurrency(l.Currency)
	fingerprint := listingFingerprint(l.Title)
	observedUnix := observedAt.Unix()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO listing_observations (ad_id, observed_at, price, currency, title, url, fingerprint, source)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		l.AdID, observedUnix, l.Price, currency, l.Title, l.URL, fingerprint, source); err != nil {
		return err
	}

	relistCount := 0
	if fingerprint != "" {
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM listing_lifecycle WHERE last_fingerprint = ? AND ad_id != ?`,
			fingerprint, l.AdID).Scan(&relistCount); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO listing_lifecycle (
	ad_id, first_seen, last_seen, last_price, last_currency, last_fingerprint,
	seen_count, price_change_count, relist_count
) VALUES (?, ?, ?, ?, ?, ?, 1, 0, ?)
ON CONFLICT(ad_id) DO UPDATE SET
	last_seen = CASE
		WHEN excluded.last_seen > listing_lifecycle.last_seen THEN excluded.last_seen
		ELSE listing_lifecycle.last_seen
	END,
	seen_count = listing_lifecycle.seen_count + 1,
	price_change_count = listing_lifecycle.price_change_count + CASE
		WHEN excluded.last_price > 0
		 AND listing_lifecycle.last_price > 0
		 AND (excluded.last_price != listing_lifecycle.last_price
		      OR excluded.last_currency != listing_lifecycle.last_currency)
		THEN 1 ELSE 0
	END,
	last_price = CASE
		WHEN excluded.last_price > 0 THEN excluded.last_price
		ELSE listing_lifecycle.last_price
	END,
	last_currency = CASE
		WHEN excluded.last_currency != '' THEN excluded.last_currency
		ELSE listing_lifecycle.last_currency
	END,
	last_fingerprint = CASE
		WHEN excluded.last_fingerprint != '' THEN excluded.last_fingerprint
		ELSE listing_lifecycle.last_fingerprint
	END,
	relist_count = CASE
		WHEN listing_lifecycle.relist_count > excluded.relist_count THEN listing_lifecycle.relist_count
		ELSE excluded.relist_count
	END`,
		l.AdID, observedUnix, observedUnix, l.Price, currency, fingerprint, relistCount)
	return err
}

func listingFingerprint(title string) string {
	normalized := normalizeFingerprintText(title)
	if normalized == "" {
		return ""
	}
	sum := sha1.Sum([]byte(normalized))
	return hex.EncodeToString(sum[:8])
}

func normalizeFingerprintText(s string) string {
	var b strings.Builder
	lastSpace := true
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func (s *Store) ClaimDueListings(ctx context.Context, states []models.ProcessState, limit int, lease time.Duration) ([]models.Listing, error) {
	if limit <= 0 || len(states) == 0 {
		return nil, nil
	}
	now := time.Now().Unix()
	leaseUntil := time.Now().Add(lease).Unix()
	args := make([]any, 0, len(states)+3)
	ph := make([]string, 0, len(states))
	for _, st := range states {
		ph = append(ph, "?")
		args = append(args, string(st))
	}
	args = append(args, now, now, limit)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
SELECT ad_id, user_id, title, price, currency, url, search_condition, exchange, kp_izlog, is_renewed, description_snip,
       description, seller, status, process_state,
       attempt_count, next_attempt_at, lease_until, last_error, created_at
FROM market_listings
WHERE process_state IN (`+strings.Join(ph, ",")+`)
  AND next_attempt_at <= ?
  AND (lease_until = 0 OR lease_until < ?)
ORDER BY created_at ASC, ad_id ASC
LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	var out []models.Listing
	for rows.Next() {
		var l models.Listing
		var status, state string
		var nextAt, leaseAt, createdAt int64
		var exchange, kpIzlog, isRenewed int
		if err := rows.Scan(&l.AdID, &l.UserID, &l.Title, &l.Price, &l.Currency, &l.URL,
			&l.Condition, &exchange, &kpIzlog, &isRenewed, &l.DescriptionSnip,
			&l.Description, &l.Seller, &status, &state, &l.AttemptCount,
			&nextAt, &leaseAt, &l.LastError, &createdAt); err != nil {
			rows.Close()
			return nil, err
		}
		l.Exchange = exchange != 0
		l.KPIzlog = kpIzlog != 0
		l.IsRenewed = isRenewed != 0
		l.Status = models.Status(status)
		l.ProcessState = models.ProcessState(state)
		l.NextAttemptAt = timeFromUnix(nextAt)
		l.LeaseUntil = time.Unix(leaseUntil, 0)
		l.CreatedAt = timeFromUnix(createdAt)
		out = append(out, l)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, l := range out {
		if _, err := tx.ExecContext(ctx,
			`UPDATE market_listings SET lease_until = ? WHERE ad_id = ?`,
			leaseUntil, l.AdID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) MarkProcessState(ctx context.Context, adID int64, state models.ProcessState) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE market_listings SET process_state = ?, lease_until = 0 WHERE ad_id = ?`,
		string(state), adID)
	return err
}

func (s *Store) RetryProcess(ctx context.Context, adID int64, state models.ProcessState, reason string) error {
	var attempts int
	if err := s.db.QueryRowContext(ctx, `SELECT attempt_count FROM market_listings WHERE ad_id = ?`, adID).Scan(&attempts); err != nil {
		return err
	}
	attempts++
	next := time.Now().Add(retryDelay(attempts)).Unix()
	_, err := s.db.ExecContext(ctx, `
UPDATE market_listings
SET status = ?, process_state = ?, attempt_count = ?, next_attempt_at = ?, lease_until = 0, last_error = ?
WHERE ad_id = ?`,
		string(models.StatusNew), string(state), attempts, next, truncateErr(reason), adID)
	return err
}

func (s *Store) CompleteProcess(ctx context.Context, adID int64, st models.Status) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE market_listings
SET status = ?, process_state = ?, lease_until = 0, last_error = ''
WHERE ad_id = ?`, string(st), string(models.ProcessDone), adID)
	return err
}

func (s *Store) MarkDead(ctx context.Context, adID int64, reason string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE market_listings
SET status = ?, process_state = ?, lease_until = 0, last_error = ?
WHERE ad_id = ?`, string(models.StatusError), string(models.ProcessDead), truncateErr(reason), adID)
	return err
}

// Delete удаляет лот — например, чтобы вернуть его в очередь после
// антибот-челленджа KP (иначе дедупликация по ad_id его больше не пропустит).
func (s *Store) Delete(ctx context.Context, adID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM market_listings WHERE ad_id = ?`, adID)
	return err
}

func (s *Store) UpdateDetails(ctx context.Context, adID int64, description, seller string, price float64, currency string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	currency = models.NormalizeCurrency(currency)
	if _, err := tx.ExecContext(ctx,
		`UPDATE market_listings SET description = ?, seller = ?, price = ?, currency = ? WHERE ad_id = ?`,
		description, seller, price, currency, adID); err != nil {
		return err
	}

	var l models.Listing
	var createdAt int64
	if err := tx.QueryRowContext(ctx,
		`SELECT ad_id, title, price, currency, url, created_at FROM market_listings WHERE ad_id = ?`,
		adID,
	).Scan(&l.AdID, &l.Title, &l.Price, &l.Currency, &l.URL, &createdAt); err != nil {
		return err
	}
	l.CreatedAt = time.Now()
	if err := recordListingObservationTx(ctx, tx, l, "detail", l.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

// FunnelVerdict — аудит детерминированного вердикта воронки (PLAN_v4 §4.4).
type FunnelVerdict struct {
	Code         string  // DIAMOND / DIAMOND_SUSPECT / FAIR / EXPENSIVE / CHECK / MANUAL / SHOP / JUNK / SANITY
	Deviation    float64 // цена/медиана − 1
	GroupN       int     // размер опорной группы
	Alternatives string  // JSON-массив альтернатив
	Reason       string  // пояснение из фактов (для NEED CHECK и аудита)
	Specs        string  // конфигурация одной строкой
}

// SaveFunnelVerdict пишет вердикт воронки + статус лота.
func (s *Store) SaveFunnelVerdict(ctx context.Context, adID int64, v FunnelVerdict, st models.Status) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE market_listings SET verdict_code = ?, deviation = ?, group_n = ?, alternatives = ?, reason = ?, specs = ?, status = ?, process_state = ?, lease_until = 0 WHERE ad_id = ?`,
		v.Code, v.Deviation, v.GroupN, v.Alternatives, v.Reason, v.Specs, string(st), processStateForStatus(st), adID)
	return err
}

func (s *Store) SaveFunnelAlertPending(ctx context.Context, adID int64, v FunnelVerdict, text, url string, finalStatus models.Status) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE market_listings SET verdict_code = ?, deviation = ?, group_n = ?, alternatives = ?, reason = ?, specs = ?, status = ?, process_state = ?, lease_until = 0 WHERE ad_id = ?`,
		v.Code, v.Deviation, v.GroupN, v.Alternatives, v.Reason, v.Specs,
		string(models.StatusNew), string(models.ProcessAlertPending), adID); err != nil {
		return err
	}
	if err := enqueueOutboxTx(ctx, tx, adID, "funnel", text, url, finalStatus); err != nil {
		return err
	}
	return tx.Commit()
}

type TelegramOutboxItem struct {
	ID          int64
	AdID        int64
	Kind        string
	Text        string
	URL         string
	FinalStatus models.Status
	Attempts    int
	CreatedAt   time.Time
}

func enqueueOutboxTx(ctx context.Context, tx *sql.Tx, adID int64, kind, text, url string, finalStatus models.Status) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM telegram_outbox WHERE ad_id = ? AND sent_at = 0`, adID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO telegram_outbox (ad_id, kind, text, url, final_status, next_attempt_at, created_at)
VALUES (?, ?, ?, ?, ?, 0, ?)`,
		adID, kind, text, url, string(finalStatus), time.Now().Unix())
	return err
}

func (s *Store) ClaimTelegramOutbox(ctx context.Context, limit int, lease time.Duration) ([]TelegramOutboxItem, error) {
	if limit <= 0 {
		return nil, nil
	}
	now := time.Now().Unix()
	leaseUntil := time.Now().Add(lease).Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
SELECT id, ad_id, kind, text, url, final_status, attempt_count, created_at
FROM telegram_outbox
WHERE sent_at = 0
  AND next_attempt_at <= ?
  AND (lease_until = 0 OR lease_until < ?)
ORDER BY created_at ASC, id ASC
LIMIT ?`, now, now, limit)
	if err != nil {
		return nil, err
	}
	var out []TelegramOutboxItem
	for rows.Next() {
		var it TelegramOutboxItem
		var finalStatus string
		var createdAt int64
		if err := rows.Scan(&it.ID, &it.AdID, &it.Kind, &it.Text, &it.URL,
			&finalStatus, &it.Attempts, &createdAt); err != nil {
			rows.Close()
			return nil, err
		}
		it.FinalStatus = models.Status(finalStatus)
		it.CreatedAt = timeFromUnix(createdAt)
		out = append(out, it)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, it := range out {
		if _, err := tx.ExecContext(ctx,
			`UPDATE telegram_outbox SET lease_until = ? WHERE id = ?`,
			leaseUntil, it.ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) MarkTelegramOutboxSent(ctx context.Context, it TelegramOutboxItem, messageID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `
UPDATE telegram_outbox
SET sent_at = ?, message_id = ?, lease_until = 0, last_error = ''
WHERE id = ?`, now, messageID, it.ID); err != nil {
		return err
	}
	if it.AdID != 0 && it.FinalStatus != "" {
		if _, err := tx.ExecContext(ctx, `
UPDATE market_listings
SET status = ?, process_state = ?, lease_until = 0, last_error = ''
WHERE ad_id = ?`, string(it.FinalStatus), string(models.ProcessDone), it.AdID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) RetryTelegramOutbox(ctx context.Context, id int64, reason string) error {
	var attempts int
	if err := s.db.QueryRowContext(ctx,
		`SELECT attempt_count FROM telegram_outbox WHERE id = ?`, id).Scan(&attempts); err != nil {
		return err
	}
	attempts++
	next := time.Now().Add(retryDelay(attempts)).Unix()
	_, err := s.db.ExecContext(ctx, `
UPDATE telegram_outbox
SET attempt_count = ?, next_attempt_at = ?, lease_until = 0, last_error = ?
WHERE id = ?`, attempts, next, truncateErr(reason), id)
	return err
}

func (s *Store) PendingTelegramOutbox(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM telegram_outbox WHERE sent_at = 0`).Scan(&n)
	return n, err
}

func (s *Store) LastTelegramDelivery(ctx context.Context) (time.Time, error) {
	var sent sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(sent_at) FROM telegram_outbox WHERE sent_at > 0`).Scan(&sent)
	if err != nil || !sent.Valid || sent.Int64 <= 0 {
		return time.Time{}, err
	}
	return time.Unix(sent.Int64, 0), nil
}

func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var version sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version)
	if err != nil || !version.Valid {
		return 0, err
	}
	return int(version.Int64), nil
}

func (s *Store) CountByProcessState(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT process_state, COUNT(*) FROM market_listings WHERE process_state != '' GROUP BY process_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		out[state] = n
	}
	return out, rows.Err()
}

// VerdictRow — строка отчёта по вердиктам воронки (пульт «Алмазы»).
type VerdictRow struct {
	AdID        int64
	Title       string
	Price       float64
	Currency    string
	URL         string
	VerdictCode string
	Deviation   float64
	CreatedAt   time.Time
}

// RecentVerdicts — вердикты по маске (LIKE) с since, свежие первыми.
func (s *Store) RecentVerdicts(ctx context.Context, codeLike string, since time.Time, limit int) ([]VerdictRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT ad_id, title, price, currency, url, verdict_code, deviation, created_at
		 FROM market_listings
		 WHERE verdict_code LIKE ? AND created_at >= ?
		 ORDER BY created_at DESC LIMIT ?`, codeLike, since.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VerdictRow
	for rows.Next() {
		var r VerdictRow
		var createdAt int64
		if err := rows.Scan(&r.AdID, &r.Title, &r.Price, &r.Currency, &r.URL,
			&r.VerdictCode, &r.Deviation, &createdAt); err != nil {
			return nil, err
		}
		r.CreatedAt = time.Unix(createdAt, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Unsynced возвращает лоты, ещё не выгруженные в CSV.
func (s *Store) Unsynced(ctx context.Context, limit int) ([]models.Listing, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT ad_id, title, price, currency, url, created_at FROM market_listings WHERE synced_to_sheets = 0 ORDER BY created_at ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.Listing
	for rows.Next() {
		var l models.Listing
		var createdAt int64
		if err := rows.Scan(&l.AdID, &l.Title, &l.Price, &l.Currency, &l.URL, &createdAt); err != nil {
			return nil, err
		}
		l.CreatedAt = time.Unix(createdAt, 0)
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) MarkSynced(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE market_listings SET synced_to_sheets = 1 WHERE ad_id IN (`+placeholders+`)`, args...)
	return err
}

// CountByStatusSince — количество лотов по статусам, появившихся с since
// (питает дневной дайджест).
func (s *Store) CountByStatusSince(ctx context.Context, since time.Time) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT status, COUNT(*) FROM market_listings WHERE created_at >= ? GROUP BY status`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// RecentAlerts — последние ALERTED-лоты с since (для дайджеста).
func (s *Store) RecentAlerts(ctx context.Context, since time.Time, limit int) ([]models.Listing, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT ad_id, title, price, currency, url, created_at FROM market_listings
		 WHERE status = 'ALERTED' AND created_at >= ?
		 ORDER BY created_at DESC LIMIT ?`, since.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Listing
	for rows.Next() {
		var l models.Listing
		var createdAt int64
		if err := rows.Scan(&l.AdID, &l.Title, &l.Price, &l.Currency, &l.URL, &createdAt); err != nil {
			return nil, err
		}
		l.CreatedAt = time.Unix(createdAt, 0)
		out = append(out, l)
	}
	return out, rows.Err()
}

type priceRow struct {
	Title string
	Price float64
}

// RecentPrices — цены EUR-лотов за последние days дней (питает кэш цен).
func (s *Store) RecentPrices(ctx context.Context, days, limit int) ([]priceRow, error) {
	since := time.Now().AddDate(0, 0, -days).Unix()
	rows, err := s.db.QueryContext(ctx,
		`SELECT title, price FROM market_listings
		 WHERE price > 0 AND currency = 'EUR' AND created_at >= ?
		 ORDER BY created_at DESC LIMIT ?`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []priceRow
	for rows.Next() {
		var r priceRow
		if err := rows.Scan(&r.Title, &r.Price); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM market_listings`).Scan(&n)
	return n, err
}

func processStateForStatus(st models.Status) string {
	switch st {
	case models.StatusError:
		return string(models.ProcessDead)
	default:
		return string(models.ProcessDone)
	}
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Duration(1<<uint(attempt-1)) * time.Minute
}

func truncateErr(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= 1000 {
		return s
	}
	return string(r[:1000])
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func timeFromUnix(v int64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	return time.Unix(v, 0)
}
