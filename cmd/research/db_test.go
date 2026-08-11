package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrateResearchAddsLaptopModel(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "research.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`
CREATE TABLE research_ads (
	ad_id INTEGER PRIMARY KEY,
	fetch_status TEXT NOT NULL DEFAULT 'OK',
	description TEXT NOT NULL DEFAULT 'desc'
);
CREATE TABLE research_specs (
	ad_id INTEGER PRIMARY KEY,
	cpu_model TEXT NOT NULL DEFAULT '',
	cpu_score REAL NOT NULL DEFAULT 0,
	ram_gb INTEGER NOT NULL DEFAULT 0,
	ssd_gb INTEGER NOT NULL DEFAULT 0,
	gpu_model TEXT NOT NULL DEFAULT '',
	gpu_score REAL NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL DEFAULT 0
);
INSERT INTO research_ads (ad_id, fetch_status, description) VALUES (1, 'OK', 'desc');
INSERT INTO research_specs (ad_id, cpu_model) VALUES (1, 'i7-10850H');
`); err != nil {
		t.Fatal(err)
	}

	if err := migrateResearch(db); err != nil {
		t.Fatal(err)
	}
	if !testColumnExists(t, db, "research_specs", "laptop_model") {
		t.Fatal("research_specs.laptop_model was not added")
	}
	if !testColumnExists(t, db, "research_specs", "source") {
		t.Fatal("research_specs.source was not added")
	}

	var laptopModel, source string
	if err := db.QueryRow(`SELECT laptop_model, source FROM research_specs WHERE ad_id=1`).
		Scan(&laptopModel, &source); err != nil {
		t.Fatal(err)
	}
	if laptopModel != "" || source != "regex" {
		t.Fatalf("migrated defaults = laptop_model=%q source=%q", laptopModel, source)
	}
}

func testColumnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     string
			notNull int
			def     sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &def, &pk); err != nil {
			t.Fatal(err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return false
}
