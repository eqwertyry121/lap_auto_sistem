package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const researchSchema = `
CREATE TABLE IF NOT EXISTS research_ads (
	ad_id         INTEGER PRIMARY KEY,
	title         TEXT NOT NULL DEFAULT '',
	price         REAL NOT NULL DEFAULT 0,
	currency      TEXT NOT NULL DEFAULT 'EUR',
	url           TEXT NOT NULL DEFAULT '',
	posted        TEXT NOT NULL DEFAULT '',
	description   TEXT NOT NULL DEFAULT '',
	seller        TEXT NOT NULL DEFAULT '',
	condition     TEXT NOT NULL DEFAULT '',
	kind          TEXT NOT NULL DEFAULT 'UNKNOWN',
	is_trader     INTEGER NOT NULL DEFAULT 0,
	kp_izlog      INTEGER NOT NULL DEFAULT 0,
	attrs_json    TEXT NOT NULL DEFAULT '',
	fetch_status  TEXT NOT NULL DEFAULT '',
	fetched_at    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_ra_kind   ON research_ads(kind);
CREATE INDEX IF NOT EXISTS idx_ra_status ON research_ads(fetch_status);

CREATE TABLE IF NOT EXISTS research_specs (
	ad_id      INTEGER PRIMARY KEY,
	laptop_model TEXT NOT NULL DEFAULT '',
	cpu_model  TEXT NOT NULL DEFAULT '',
	cpu_score  REAL NOT NULL DEFAULT 0,
	ram_gb     INTEGER NOT NULL DEFAULT 0,
	ssd_gb     INTEGER NOT NULL DEFAULT 0,
	gpu_model  TEXT NOT NULL DEFAULT '',
	gpu_score  REAL NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL DEFAULT 0,
	source     TEXT NOT NULL DEFAULT 'regex'
);

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
	target_type TEXT NOT NULL,            -- 'seller' | 'ad'
	target_id   INTEGER NOT NULL,
	label       TEXT NOT NULL,            -- SHOP/PRIVATE/JUNK/CLEAN
	note        TEXT NOT NULL DEFAULT '',
	created     INTEGER NOT NULL DEFAULT 0,
	UNIQUE(target_type, target_id)
);
`

// researchStore — отдельная SQLite-база датасета (не трогает SSOT бота).
type researchStore struct {
	db *sql.DB
}

type researchRow struct {
	AdID        int64
	Title       string
	Price       float64
	Currency    string
	URL         string
	Posted      string
	Description string
	Seller      string
	Condition   string // поле KP: "new" / "used"
	Kind        string // NEW / USED / BROKEN / UNKNOWN (condition + текст)
	IsTrader    bool   // KP пометил продавца торговцем
	KPIzlog     bool   // у продавца витрина (KP Izlog)
	AttrsJSON   string // все атрибуты — для последующего анализа
	FetchStatus string // OK / NOT_FOUND / ERROR
	FetchedAt   time.Time

	// Бесплатные сигналы уровня поиска (PLAN_v4, Решение ②).
	UserID    int64
	ViewCount int64
	IsRenewed bool
	Snippet   string
}

func openResearch(path string) (*researchStore, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// Два писателя возможны (бот начнёт upsert'ить search-строки с Фазы 4Г,
	// плюс разовые проходы -search-refresh): ждём блокировку, а не падаем.
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(researchSchema); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateResearch(db); err != nil {
		db.Close()
		return nil, err
	}
	return &researchStore{db: db}, nil
}

// migrateResearch — добавляет колонки, которых нет (идемпотентно):
// схема расширялась по ходу проекта (Фаза 2), ALTER TABLE в SQLite не
// поддерживает IF NOT EXISTS.
func migrateResearch(db *sql.DB) error {
	cols := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(research_ads)`)
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
		{"user_id", `ALTER TABLE research_ads ADD COLUMN user_id INTEGER NOT NULL DEFAULT 0`},
		{"view_count", `ALTER TABLE research_ads ADD COLUMN view_count INTEGER NOT NULL DEFAULT 0`},
		{"is_renewed", `ALTER TABLE research_ads ADD COLUMN is_renewed INTEGER NOT NULL DEFAULT 0`},
		{"snippet", `ALTER TABLE research_ads ADD COLUMN snippet TEXT NOT NULL DEFAULT ''`},
	} {
		if cols[m.col] {
			continue
		}
		if _, err := db.Exec(m.stmt); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_ra_user ON research_ads(user_id)`); err != nil {
		return err
	}

	// research_specs.source — кто распознал железо (regex/gemini-text/gemini-photo-all).
	specCols := map[string]bool{}
	rows2, err := db.Query(`PRAGMA table_info(research_specs)`)
	if err != nil {
		return err
	}
	for rows2.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows2.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows2.Close()
			return err
		}
		specCols[name] = true
	}
	rows2.Close()
	if err := rows2.Err(); err != nil {
		return err
	}
	if !specCols["laptop_model"] {
		if _, err := db.Exec(`ALTER TABLE research_specs ADD COLUMN laptop_model TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if !specCols["source"] {
		if _, err := db.Exec(`ALTER TABLE research_specs ADD COLUMN source TEXT NOT NULL DEFAULT 'regex'`); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`
DELETE FROM research_specs
WHERE source='regex'
  AND ad_id IN (
	SELECT ad_id FROM research_ads
	WHERE fetch_status!='OK' OR description=''
  )`); err != nil {
		return err
	}
	return nil
}

func (s *researchStore) close() error { return s.db.Close() }

// healEmptyOK — самозалечивание датасета: строки, помеченные OK при фактически
// пустом ответе /eds/ (мягкая форма антибота KP: HTTP 200, но info без
// описания и продавца), возвращаются в очередь докачки. Без этого они
// никогда не ретраятся — строка считается финальной.
func (s *researchStore) healEmptyOK(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE research_ads SET fetch_status='SEARCH'
		 WHERE fetch_status='OK' AND description='' AND seller=''`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// status — fetch_status строки в датасете; пустая строка, если лота нет.
func (s *researchStore) status(ctx context.Context, adID int64) (string, error) {
	var st string
	err := s.db.QueryRowContext(ctx,
		`SELECT fetch_status FROM research_ads WHERE ad_id = ?`, adID).Scan(&st)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return st, err
}

func (s *researchStore) upsert(ctx context.Context, r researchRow) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO research_ads (ad_id, title, price, currency, url, posted, description, seller, condition, kind, is_trader, kp_izlog, attrs_json, fetch_status, fetched_at, user_id, view_count, is_renewed, snippet)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(ad_id) DO UPDATE SET
	title=excluded.title, price=excluded.price, currency=excluded.currency, url=excluded.url,
	posted=excluded.posted, description=excluded.description, seller=excluded.seller,
	condition=excluded.condition, kind=excluded.kind, is_trader=excluded.is_trader,
	kp_izlog=excluded.kp_izlog, attrs_json=excluded.attrs_json,
	fetch_status=excluded.fetch_status, fetched_at=excluded.fetched_at,
	user_id=excluded.user_id, view_count=excluded.view_count,
	is_renewed=excluded.is_renewed, snippet=excluded.snippet`,
		r.AdID, r.Title, r.Price, r.Currency, r.URL, r.Posted, r.Description, r.Seller,
		r.Condition, r.Kind, boolInt(r.IsTrader), boolInt(r.KPIzlog), r.AttrsJSON,
		r.FetchStatus, r.FetchedAt.Unix(), r.UserID, r.ViewCount, boolInt(r.IsRenewed), r.Snippet)
	return err
}

// observeSeller — наблюдение продавца в реестре (Фаза 2): каждый встреченный
// лот увеличивает счётчик, флаги/метаданные «защёлкиваются» в максимум.
func (s *researchStore) observeSeller(ctx context.Context, userID int64,
	trader, kpizlog bool, reviews int, userCreated string) error {
	if userID == 0 {
		return nil
	}
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sellers (user_id, first_seen, last_seen, ads_seen, trader_seen, kpizlog_seen, reviews, user_created)
VALUES (?,?,?,?,?,?,?,?)
ON CONFLICT(user_id) DO UPDATE SET
	last_seen=excluded.last_seen,
	ads_seen=sellers.ads_seen+1,
	trader_seen=CASE WHEN excluded.trader_seen=1 THEN 1 ELSE sellers.trader_seen END,
	kpizlog_seen=CASE WHEN excluded.kpizlog_seen=1 THEN 1 ELSE sellers.kpizlog_seen END,
	reviews=CASE WHEN excluded.reviews>sellers.reviews THEN excluded.reviews ELSE sellers.reviews END,
	user_created=CASE WHEN excluded.user_created!='' THEN excluded.user_created ELSE sellers.user_created END`,
		userID, now, now, 1, boolInt(trader), boolInt(kpizlog), reviews, userCreated)
	return err
}

// countSellerAds — сколько лотов продавца в датасете (правило М3).
func (s *researchStore) countSellerAds(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM research_ads WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}

// upsertLabel — ручная разметка (золотая выборка).
func (s *researchStore) upsertLabel(ctx context.Context, targetType string, targetID int64, label, note string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO labels (target_type, target_id, label, note, created)
VALUES (?,?,?,?,?)
ON CONFLICT(target_type, target_id) DO UPDATE SET
	label=excluded.label, note=excluded.note, created=excluded.created`,
		targetType, targetID, label, note, time.Now().Unix())
	return err
}

// refreshSearch — частичное обновление search-полей уже собранных строк
// (бэкфилл user_id и др. после Фазы 2): детали, продавец и статусы
// записи НЕ затираются.
func (s *researchStore) refreshSearch(ctx context.Context, r researchRow) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE research_ads SET
	price=?, currency=?, posted=?, condition=?, kp_izlog=?,
	user_id=?, view_count=?, is_renewed=?, snippet=?, fetched_at=?
WHERE ad_id=?`,
		r.Price, r.Currency, r.Posted, r.Condition, boolInt(r.KPIzlog),
		r.UserID, r.ViewCount, boolInt(r.IsRenewed), r.Snippet, r.FetchedAt.Unix(), r.AdID)
	return err
}

func (s *researchStore) count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM research_ads WHERE fetch_status = 'OK'`).Scan(&n)
	return n, err
}

func (s *researchStore) countByKind(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT kind, COUNT(*) FROM research_ads WHERE fetch_status = 'OK' GROUP BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		out[kind] = n
	}
	return out, rows.Err()
}

func (s *researchStore) all(ctx context.Context) ([]researchRow, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT ad_id, title, price, currency, url, posted, description, seller, condition, kind,
	is_trader, kp_izlog, fetch_status, fetched_at
FROM research_ads WHERE fetch_status IN ('OK','SEARCH') ORDER BY fetched_at ASC, ad_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []researchRow
	for rows.Next() {
		var r researchRow
		var fetchedAt int64
		var isTrader, kpIzlog int
		if err := rows.Scan(&r.AdID, &r.Title, &r.Price, &r.Currency, &r.URL, &r.Posted,
			&r.Description, &r.Seller, &r.Condition, &r.Kind,
			&isTrader, &kpIzlog, &r.FetchStatus, &fetchedAt); err != nil {
			return nil, err
		}
		r.IsTrader = isTrader != 0
		r.KPIzlog = kpIzlog != 0
		r.FetchedAt = time.Unix(fetchedAt, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

type specsRow struct {
	AdID        int64
	LaptopModel string
	CPUModel    string
	CPUScore    float64
	RAMGB       int
	SSDGB       int
	GPUModel    string
	GPUScore    float64
}

func (s *researchStore) upsertSpecs(ctx context.Context, r specsRow) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO research_specs (ad_id, laptop_model, cpu_model, cpu_score, ram_gb, ssd_gb, gpu_model, gpu_score, updated_at)
VALUES (?,?,?,?,?,?,?,?,?)
ON CONFLICT(ad_id) DO UPDATE SET
	laptop_model=excluded.laptop_model,
	cpu_model=excluded.cpu_model, cpu_score=excluded.cpu_score, ram_gb=excluded.ram_gb,
	ssd_gb=excluded.ssd_gb, gpu_model=excluded.gpu_model, gpu_score=excluded.gpu_score,
	updated_at=excluded.updated_at`,
		r.AdID, r.LaptopModel, r.CPUModel, r.CPUScore, r.RAMGB, r.SSDGB, r.GPUModel, r.GPUScore, time.Now().Unix())
	return err
}
