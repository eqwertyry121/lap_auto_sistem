package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"kpbot/models"
)

// Store — единая точка доступа к БД (SSOT). SQLite на MVP, при переезде
// на PostgreSQL меняется только этот файл (драйвер и плейсхолдеры).
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS market_listings (
	ad_id             INTEGER PRIMARY KEY,
	title             TEXT NOT NULL DEFAULT '',
	price             REAL NOT NULL DEFAULT 0,
	currency          TEXT NOT NULL DEFAULT 'EUR',
	url               TEXT NOT NULL DEFAULT '',
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
	created_at        INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_ml_status  ON market_listings(status);
CREATE INDEX IF NOT EXISTS idx_ml_synced  ON market_listings(synced_to_sheets);
CREATE INDEX IF NOT EXISTS idx_ml_created ON market_listings(created_at);
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
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("применение схемы: %w", err)
	}
	if err := migrateListingAudit(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("миграция аудита: %w", err)
	}
	return &Store{db: db}, nil
}

// migrateListingAudit — колонки аудита вердиктов воронки (PLAN_v4 §4.4).
// ALTER TABLE без IF NOT EXISTS — проверяем pragma'ми.
func migrateListingAudit(db *sql.DB) error {
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
		{"verdict_code", `ALTER TABLE market_listings ADD COLUMN verdict_code TEXT NOT NULL DEFAULT ''`},
		{"deviation", `ALTER TABLE market_listings ADD COLUMN deviation REAL NOT NULL DEFAULT 0`},
		{"group_n", `ALTER TABLE market_listings ADD COLUMN group_n INTEGER NOT NULL DEFAULT 0`},
		{"alternatives", `ALTER TABLE market_listings ADD COLUMN alternatives TEXT NOT NULL DEFAULT ''`},
	} {
		if cols[m.col] {
			continue
		}
		if _, err := db.Exec(m.stmt); err != nil {
			return err
		}
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
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO market_listings (ad_id, title, price, currency, url, status, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		l.AdID, l.Title, l.Price, l.Currency, l.URL, string(l.Status), l.CreatedAt.Unix())
	return err
}

func (s *Store) SetStatus(ctx context.Context, adID int64, st models.Status) error {
	_, err := s.db.ExecContext(ctx, `UPDATE market_listings SET status = ? WHERE ad_id = ?`, string(st), adID)
	return err
}

// Delete удаляет лот — например, чтобы вернуть его в очередь после
// антибот-челленджа KP (иначе дедупликация по ad_id его больше не пропустит).
func (s *Store) Delete(ctx context.Context, adID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM market_listings WHERE ad_id = ?`, adID)
	return err
}

func (s *Store) UpdateDetails(ctx context.Context, adID int64, description, seller string, price float64, currency string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE market_listings SET description = ?, seller = ?, price = ?, currency = ? WHERE ad_id = ?`,
		description, seller, price, currency, adID)
	return err
}

// SaveVerdict пишет вердикт Gemini (старый путь оценки).
func (s *Store) SaveVerdict(ctx context.Context, adID int64, v models.Verdict, st models.Status) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE market_listings SET is_deal = ?, need_check = ?, model_found = ?, estimated_profit = ?, reason = ?, specs = ?, status = ? WHERE ad_id = ?`,
		boolInt(v.IsDeal), boolInt(v.NeedCheck), boolInt(v.ModelFound), v.EstimatedProfit, v.Reason, v.Specs, string(st), adID)
	return err
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
		`UPDATE market_listings SET verdict_code = ?, deviation = ?, group_n = ?, alternatives = ?, reason = ?, specs = ?, status = ? WHERE ad_id = ?`,
		v.Code, v.Deviation, v.GroupN, v.Alternatives, v.Reason, v.Specs, string(st), adID)
	return err
}

// SaveFunnelAudit — только аудит, статус не трогается (теневой режим).
func (s *Store) SaveFunnelAudit(ctx context.Context, adID int64, v FunnelVerdict) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE market_listings SET verdict_code = ?, deviation = ?, group_n = ?, alternatives = ?, reason = ?, specs = ? WHERE ad_id = ?`,
		v.Code, v.Deviation, v.GroupN, v.Alternatives, v.Reason, v.Specs, adID)
	return err
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

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
