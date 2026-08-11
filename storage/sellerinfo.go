package storage

import (
	"context"
	"database/sql"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SellerInfo — сведения о продавце из research.db для фильтра L1
// (правила М3/М6). Открывает базу на каждое обращение: лотов немного,
// а два писателя (бот и research) уже разведены busy_timeout'ом.
type SellerInfo struct {
	Found          bool
	AdsCount       int    // лотов продавца в датасете (COUNT по user_id)
	RecentAdsCount int    // лотов продавца, недавно виденных в поиске/research
	Label          string // ручная разметка seller из labels/sellers.label: SHOP или PRIVATE
	TraderSeen     bool   // KP хоть раз пометил торговцем
	KPIzlogSeen    bool   // хоть раз была витрина
	Reviews        int    // отзывов (максимум из виденного)
	UserCreated    string // дата регистрации аккаунта «2006-01-02 15:04:05»
}

// AgeDays — возраст аккаунта в днях; -1 если неизвестен.
func (s SellerInfo) AgeDays() int {
	if s.UserCreated == "" {
		return -1
	}
	t, err := time.Parse("2006-01-02 15:04:05", s.UserCreated)
	if err != nil {
		return -1
	}
	return int(time.Since(t).Hours() / 24)
}

// LoadSellerInfo — продавец по user_id (0 → пустой результат).
func LoadSellerInfo(ctx context.Context, dbPath string, userID int64) (SellerInfo, error) {
	if userID == 0 {
		return SellerInfo{}, nil
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return SellerInfo{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		return SellerInfo{}, err
	}

	var info SellerInfo
	var trader, izlog int
	err = db.QueryRowContext(ctx, `
SELECT ads_seen, trader_seen, kpizlog_seen, reviews, user_created
FROM sellers WHERE user_id = ?`, userID).
		Scan(&info.AdsCount, &trader, &izlog, &info.Reviews, &info.UserCreated)
	switch err {
	case nil:
		info.Found = true
		info.TraderSeen = trader == 1
		info.KPIzlogSeen = izlog == 1
	case sql.ErrNoRows:
	default:
		return SellerInfo{}, err
	}
	if label, err := lookupManualLabel(ctx, db, "seller", userID); err != nil {
		return SellerInfo{}, err
	} else if label != "" {
		info.Label = label
	}
	if info.Label == "" {
		var label string
		err := db.QueryRowContext(ctx,
			`SELECT COALESCE(label,'') FROM sellers WHERE user_id = ?`, userID).Scan(&label)
		switch {
		case err == nil:
			info.Label = label
		case err == sql.ErrNoRows || missingOptionalLabelSchema(err):
		default:
			return SellerInfo{}, err
		}
	}

	// АдcSeen в реестре считает НАБЛЮДЕНИЯ (один лот может наблюдаться
	// дважды); для М3 берём точное число лотов в датасете.
	var ads int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM research_ads WHERE user_id = ?`, userID).Scan(&ads); err != nil {
		return SellerInfo{}, err
	}
	if ads > info.AdsCount {
		info.AdsCount = ads
	}
	recentSince := time.Now().Add(-30 * 24 * time.Hour).Unix()
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM research_ads WHERE user_id = ? AND fetched_at >= ?`,
		userID, recentSince).Scan(&info.RecentAdsCount); err != nil {
		return SellerInfo{}, err
	}
	return info, nil
}

// LoadLabel returns a manual label from research.db labels table.
// Missing labels table is treated as an empty label so a fresh DB can boot.
func LoadLabel(ctx context.Context, dbPath, targetType string, targetID int64) (string, error) {
	if targetID == 0 || strings.TrimSpace(targetType) == "" {
		return "", nil
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return "", err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		return "", err
	}
	return lookupManualLabel(ctx, db, targetType, targetID)
}

func lookupManualLabel(ctx context.Context, db *sql.DB, targetType string, targetID int64) (string, error) {
	var label string
	err := db.QueryRowContext(ctx, `
SELECT COALESCE(label,'')
FROM labels
WHERE target_type = ? AND target_id = ?
LIMIT 1`, strings.ToLower(strings.TrimSpace(targetType)), targetID).Scan(&label)
	switch {
	case err == nil:
		return strings.ToUpper(strings.TrimSpace(label)), nil
	case err == sql.ErrNoRows || missingOptionalLabelSchema(err):
		return "", nil
	default:
		return "", err
	}
}

func missingOptionalLabelSchema(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such table: labels") || strings.Contains(msg, "no such column: label")
}

// BumpFunnelStat — +1 к счётчику слоя воронки за день (PLAN_v4 §4.3).
// Живёт в research.db: бот и инструменты видят одну картину.
func BumpFunnelStat(ctx context.Context, dbPath, layer string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS funnel_stats (
	day    TEXT,
	layer  TEXT,
	count  INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (day, layer)
)`); err != nil {
		return err
	}
	day := time.Now().Format("2006-01-02")
	_, err = db.ExecContext(ctx, `
INSERT INTO funnel_stats (day, layer, count) VALUES (?,?,1)
ON CONFLICT(day, layer) DO UPDATE SET count = count + 1`, day, layer)
	return err
}
