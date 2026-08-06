// Package hw — доступ к эталонной базе «железа» (data/hw.db, источник —
// рейтинги chaynikam.info). Используется обогащением датасета (cmd/research
// -enrich) и движком цена/качество (cmd/analyze).
package hw

import (
	"context"
	"database/sql"
	"strings"

	_ "modernc.org/sqlite"
)

type CPU struct {
	Name  string
	Score float64
}

type GPU struct {
	Name  string
	Score float64
}

// Key нормализует имя железа для сопоставления:
// «Intel Core i5-1135G7» → «i5-1135g7», «AMD Ryzen 5 4600H» → «ryzen 5 4600h».
func Key(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	for {
		trimmed := s
		for _, p := range []string{"intel ", "amd ", "nvidia ", "geforce ", "radeon ", "core "} {
			trimmed = strings.TrimPrefix(trimmed, p)
		}
		if trimmed == s {
			return s
		}
		s = trimmed
	}
}

// LoadCPUs — lookup по нормализованному имени CPU.
func LoadCPUs(ctx context.Context, path string) (map[string]CPU, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `SELECT name, score FROM hw_cpu`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]CPU, 5000)
	for rows.Next() {
		var name string
		var score float64
		if err := rows.Scan(&name, &score); err != nil {
			return nil, err
		}
		out[Key(name)] = CPU{Name: name, Score: score}
	}
	return out, rows.Err()
}

// LoadGPUs — lookup по нормализованному имени GPU.
func LoadGPUs(ctx context.Context, path string) (map[string]GPU, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `SELECT name, score FROM hw_gpu`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]GPU, 1000)
	for rows.Next() {
		var name string
		var score float64
		if err := rows.Scan(&name, &score); err != nil {
			return nil, err
		}
		out[Key(name)] = GPU{Name: name, Score: score}
	}
	return out, rows.Err()
}
