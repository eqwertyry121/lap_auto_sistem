package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// Регрессия сортировки: очередь обязана идти по EUR-цене, а не по сырой
// (RSD-номиналы в ~117 раз больше и раньше ломали порядок «сначала дорогое»).
func TestSelectCandidatesOrdersByEUR(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	schema := `
CREATE TABLE research_ads (
	ad_id INTEGER PRIMARY KEY,
	title TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	attrs_json TEXT NOT NULL DEFAULT '',
	price REAL NOT NULL DEFAULT 0,
	currency TEXT NOT NULL DEFAULT 'EUR',
	fetch_status TEXT NOT NULL DEFAULT 'OK'
);
CREATE TABLE research_specs (
	ad_id INTEGER PRIMARY KEY,
	cpu_score REAL NOT NULL DEFAULT 0,
	source TEXT NOT NULL DEFAULT 'regex'
);`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		id       int64
		price    float64
		currency string
	}{
		{1, 3000, "EUR"},    // €3000
		{2, 468800, "RSD"},  // €4000 — должен быть ПЕРВЫМ
		{3, 6000, "EUR"},    // €6000 — выше потолка 5000, отфильтрован
		{4, 590000, "RSD"},  // ~€5034 — выше потолка, отфильтрован
		{5, 5, "EUR"},       // €5 — ниже порога 10, отфильтрован
		{6, 999999, "RSD"},  // распознан CPU — исключён
		{7, 999998, "RSD"},  // уже обработан Gemini — исключён
	}
	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO research_ads (ad_id, title, description, price, currency) VALUES (?,?,?,?,?)`,
			r.id, "t", "desc", r.price, r.currency); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO research_specs (ad_id, cpu_score) VALUES (6, 100)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO research_specs (ad_id, cpu_score, source) VALUES (7, 0, 'gemini-text')`); err != nil {
		t.Fatal(err)
	}

	cands := selectCandidates(context.Background(), db, 10, 117.2)
	if len(cands) != 2 {
		t.Fatalf("кандидатов %d, жду 2: %+v", len(cands), cands)
	}
	if cands[0].AdID != 2 {
		t.Errorf("первым должен быть RSD-лот €4000 (ad_id=2), got ad_id=%d", cands[0].AdID)
	}
	if cands[1].AdID != 1 {
		t.Errorf("вторым должен быть EUR-лот €3000 (ad_id=1), got ad_id=%d", cands[1].AdID)
	}
}
