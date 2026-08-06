package exporter

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"kpbot/models"
)

// CSVExporter — асинхронная выгрузка рыночной базы в CSV-файл (замена
// Google Sheets). Тот же паттерн, что в ТЗ: никаких записей при парсинге —
// батч дописывается раз в EXPORT_INTERVAL_MIN одним вызовом AppendRows.
type CSVExporter struct {
	path string
}

// csvHeader — колонки выгрузки (соответствуют бывшей вкладке Market_History).
var csvHeader = []string{"Дата", "Заголовок", "Цена", "Валюта", "Ссылка"}

// NewCSV создаёт экспортёр: файл открывается в режиме дозаписи, для нового
// (пустого) файла пишутся UTF-8 BOM (чтобы Excel корректно видел кириллицу)
// и заголовок.
func NewCSV(path string) (*CSVExporter, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("csv: каталог: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("csv: открытие файла: %w", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("csv: stat: %w", err)
	}
	if fi.Size() == 0 {
		if _, err := f.WriteString("\ufeff"); err != nil {
			return nil, fmt.Errorf("csv: запись BOM: %w", err)
		}
		w := csv.NewWriter(f)
		if err := w.Write(csvHeader); err != nil {
			return nil, fmt.Errorf("csv: заголовок: %w", err)
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return nil, fmt.Errorf("csv: заголовок: %w", err)
		}
	}
	return &CSVExporter{path: path}, nil
}

// AppendRows дописывает пачку строк в конец файла.
// Колонки: Дата, Заголовок, Цена, Валюта, Ссылка.
func (e *CSVExporter) AppendRows(_ context.Context, rows []models.Listing) error {
	if len(rows) == 0 {
		return nil
	}
	f, err := os.OpenFile(e.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("csv: открытие файла: %w", err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	for _, l := range rows {
		rec := []string{
			l.CreatedAt.Format("2006-01-02 15:04:05"),
			l.Title,
			strconv.FormatFloat(l.Price, 'f', -1, 64),
			l.Currency,
			l.URL,
		}
		if err := w.Write(rec); err != nil {
			return fmt.Errorf("csv: запись: %w", err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return fmt.Errorf("csv: flush: %w", err)
	}
	return nil
}
