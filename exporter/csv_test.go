package exporter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kpbot/models"
)

func TestCSVAppendRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "market_history.csv")
	exp, err := NewCSV(path)
	if err != nil {
		t.Fatalf("NewCSV: %v", err)
	}

	rows := []models.Listing{{
		AdID:      1,
		Title:     `Laptop "Lenovo", i5-1135G7`,
		Price:     449.5,
		Currency:  "EUR",
		URL:       "https://www.kupujemprodajem.com/1",
		CreatedAt: time.Unix(1720000000, 0),
	}}

	ctx := context.Background()
	if err := exp.AppendRows(ctx, rows); err != nil {
		t.Fatalf("AppendRows: %v", err)
	}
	// вторая пачка — дозапись без повторного заголовка
	if err := exp.AppendRows(ctx, rows); err != nil {
		t.Fatalf("AppendRows (вторая пачка): %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("чтение файла: %v", err)
	}
	text := string(data)

	if !strings.HasPrefix(text, "\ufeff") {
		t.Error("в начале файла нет UTF-8 BOM — Excel не увидит кириллицу")
	}
	if got := strings.Count(text, "Дата,Заголовок,Цена,Валюта,Ссылка"); got != 1 {
		t.Errorf("заголовок встретился %d раз, хочу ровно 1", got)
	}
	// кавычки и запятые в заголовке экранируются по RFC 4180
	if got := strings.Count(text, `"Laptop ""Lenovo"", i5-1135G7"`); got != 2 {
		t.Errorf("экранированный заголовок встретился %d раз, хочу 2", got)
	}
	if got := strings.Count(text, "449.5"); got != 2 {
		t.Errorf("цена встретилась %d раз, хочу 2", got)
	}
	// заголовок + 2 строки данных
	if got := len(strings.Split(strings.TrimSpace(strings.TrimPrefix(text, "\ufeff")), "\n")); got != 3 {
		t.Errorf("строк в файле %d, хочу 3", got)
	}
}

func TestCSVAppendEmptyBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "market_history.csv")
	exp, err := NewCSV(path)
	if err != nil {
		t.Fatalf("NewCSV: %v", err)
	}
	if err := exp.AppendRows(context.Background(), nil); err != nil {
		t.Fatalf("AppendRows(nil): %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("чтение файла: %v", err)
	}
	if got := len(strings.Split(strings.TrimSpace(strings.TrimPrefix(string(data), "\ufeff")), "\n")); got != 1 {
		t.Errorf("после пустой пачки строк %d, хочу 1 (только заголовок)", got)
	}
}
