// ResearchCoverage — покрытие датасета рынка (research.db) для дневного
// дайджеста бота. Только чтение: владелец базы — процесс research.
package storage

import (
	"context"
	"database/sql"

	_ "modernc.org/sqlite"
)

// ResearchCoverage возвращает общее число лотов и число лотов с деталями.
func ResearchCoverage(ctx context.Context, path string) (total, detailed int, err error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return 0, 0, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// research.exe может писать в момент чтения — ждём освобождения, не падая.
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		return 0, 0, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM research_ads`).Scan(&total); err != nil {
		return 0, 0, err
	}
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM research_ads WHERE fetch_status='OK'`).Scan(&detailed); err != nil {
		return 0, 0, err
	}
	return total, detailed, nil
}
