package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const hwSchema = `
CREATE TABLE IF NOT EXISTS hw_cpu (
	slug      TEXT PRIMARY KEY,
	name      TEXT NOT NULL,
	rank      INTEGER NOT NULL DEFAULT 0,
	score     REAL NOT NULL DEFAULT 0,
	freq_mhz  INTEGER NOT NULL DEFAULT 0,
	boost_mhz INTEGER NOT NULL DEFAULT 0,
	cores     INTEGER NOT NULL DEFAULT 0,
	threads   INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS hw_gpu (
	slug      TEXT PRIMARY KEY,
	name      TEXT NOT NULL,
	rank      INTEGER NOT NULL DEFAULT 0,
	score     REAL NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL DEFAULT 0
);
`

// hwStore — эталонная база «железа» (рейтинги chaynikam.info).
type hwStore struct {
	db *sql.DB
}

type hwCPU struct {
	Slug     string // стабильный id страницы, напр. Core_i5-1135G7
	Name     string // Intel Core i5-1135G7
	Rank     int
	Score    float64 // «общее быстродействие» chaynikam.info
	FreqMHz  int
	BoostMHz int
	Cores    int
	Threads  int
}

func openHW(path string) (*hwStore, error) {
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
	if _, err := db.Exec(hwSchema); err != nil {
		db.Close()
		return nil, err
	}
	return &hwStore{db: db}, nil
}

func (s *hwStore) close() error { return s.db.Close() }

// replaceCPUs атомарно перезатирает весь рейтинг CPU (полное обновление).
func (s *hwStore) replaceCPUs(ctx context.Context, cpus []hwCPU) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM hw_cpu`); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO hw_cpu (slug, name, rank, score, freq_mhz, boost_mhz, cores, threads, updated_at)
VALUES (?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now().Unix()
	for _, c := range cpus {
		if _, err := stmt.ExecContext(ctx, c.Slug, c.Name, c.Rank, c.Score,
			c.FreqMHz, c.BoostMHz, c.Cores, c.Threads, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type hwGPU struct {
	Slug  string
	Name  string
	Rank  int
	Score float64
}

// replaceGPUs атомарно перезатирает весь рейтинг GPU (полное обновление).
func (s *hwStore) replaceGPUs(ctx context.Context, gpus []hwGPU) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM hw_gpu`); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO hw_gpu (slug, name, rank, score, updated_at) VALUES (?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now().Unix()
	for _, g := range gpus {
		if _, err := stmt.ExecContext(ctx, g.Slug, g.Name, g.Rank, g.Score, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *hwStore) countGPUs(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM hw_gpu`).Scan(&n)
	return n, err
}

func (s *hwStore) countCPUs(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM hw_cpu`).Scan(&n)
	return n, err
}

func (s *hwStore) findByName(ctx context.Context, name string) (*hwCPU, error) {
	var c hwCPU
	err := s.db.QueryRowContext(ctx,
		`SELECT slug, name, rank, score, freq_mhz, boost_mhz, cores, threads
		 FROM hw_cpu WHERE name = ?`, name).
		Scan(&c.Slug, &c.Name, &c.Rank, &c.Score, &c.FreqMHz, &c.BoostMHz, &c.Cores, &c.Threads)
	if err != nil {
		return nil, err
	}
	return &c, nil
}
