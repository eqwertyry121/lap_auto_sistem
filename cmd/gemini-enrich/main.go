// gemini-enrich — Фаза 5 (PLAN_v4): добивка конфигураций через Gemini,
// ступень L3-текст. Берёт самые дорогие лоты с нераспознанным CPU (у них
// выше шанс «алмаза»), спрашивает у Gemini точные характеристики по
// описанию+атрибутам и пишет результат обратно в research_specs
// (source='gemini-text'). Рынок учится; повторные вызовы по уже
// обработанным лотам запрещены (идемпотентность).
//
//	go run ./cmd/gemini-enrich             # бюджет 100 лотов за запуск
//	go run ./cmd/gemini-enrich -budget 25
//
// Фото-ступень (L3-фото) использует существующий vision-путь живого бота;
// сюда она подключится после стабилизации текстовой.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"kpbot/config"
	"kpbot/hw"
	"kpbot/pricing"
	"kpbot/specs"
	"kpbot/vision"
)

type geminiSpecs = specs.GeminiSpecs

var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

func main() {
	var (
		dbPath = flag.String("db", "data/research.db", "SQLite-файл датасета")
		hwPath = flag.String("hw", "data/hw.db", "SQLite-файл эталона железа")
		budget = flag.Int("budget", 100, "сколько лотов обработать за запуск")
	)
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	log := slog.Default()

	cfg := config.Load()
	if cfg.GeminiAPIKey == "" {
		log.Error("GEMINI_API_KEY не задан — обогащение невозможно")
		os.Exit(1)
	}

	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		log.Error("датасет", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		log.Error("busy_timeout", "err", err)
		os.Exit(1)
	}

	cpus, err := hw.LoadCPUs(ctx, *hwPath)
	if err != nil {
		log.Error("эталон CPU", "err", err)
		os.Exit(1)
	}
	gpus, err := hw.LoadGPUs(ctx, *hwPath)
	if err != nil {
		log.Error("эталон GPU", "err", err)
		os.Exit(1)
	}

	rate, rateSrc := pricing.ResolveRate(ctx, *dbPath)
	log.Info("курс для фильтра цен", "rsd_eur", rate, "источник", rateSrc)

	cands := selectCandidates(ctx, db, *budget, rate)
	if len(cands) == 0 {
		log.Info("кандидатов нет — все дорогие лоты уже распознаны")
		return
	}
	log.Info("кандидаты выбраны", "лотов", len(cands), "бюджет", *budget)

	gem := vision.NewGeminiClient(cfg.GeminiAPIKey, cfg.GeminiTextModel).
		SetLimits(cfg.GeminiConcurrency, cfg.GeminiDailyLimit).
		SetDailyBudgetUSD(cfg.GeminiDailyBudgetUSD)
	var recognized, attempts int
	for _, c := range cands {
		if ctx.Err() != nil {
			break
		}
		attempts++
		sp, err := askGemini(ctx, gem, c)
		if err != nil {
			log.Error("gemini", "ad_id", c.AdID, "err", err)
			// ошибку НЕ записываем в specs: лот останется кандидатом на следующий запуск
			continue
		}
		if err := writeBack(ctx, db, c.AdID, sp, cpus, gpus); err != nil {
			log.Error("запись результата", "ad_id", c.AdID, "err", err)
			continue
		}
		if cpu := strings.TrimSpace(sp.CPU); cpu != "" {
			recognized++
			log.Info("распознано", "ad_id", c.AdID, "cpu", cpu, "цена", fmt.Sprintf("%.0f€", c.PriceEUR))
		} else {
			log.Info("gemini не определил — лот помечен обработанным", "ad_id", c.AdID)
		}
		time.Sleep(time.Second) // вежливость к API
	}
	log.Info("обогащение завершено", "обработано", attempts, "распознано", recognized)
}

type candidate struct {
	AdID      int64
	Title     string
	Desc      string
	AttrsJSON string
	PriceEUR  float64
}

// selectCandidates — самые дорогие лоты с деталями, без распознанного CPU
// и без попытки Gemini в прошлом; цена 10–5000€ (план §5.1). Сортировка —
// по НОРМАЛИЗОВАННОЙ EUR-цене: сырые RSD-номиналы (×117 к EUR) иначе
// искажают очередь «сначала самое дорогое».
func selectCandidates(ctx context.Context, db *sql.DB, budget int, rate float64) []candidate {
	rows, err := db.QueryContext(ctx, `
SELECT a.ad_id, a.title, a.description, a.attrs_json, a.price, a.currency
FROM research_ads a
LEFT JOIN research_specs s ON s.ad_id = a.ad_id
WHERE a.fetch_status='OK' AND a.description != ''
	AND COALESCE(s.cpu_score,0) = 0
	AND COALESCE(s.source,'') NOT LIKE '%gemini%'
	AND COALESCE(s.source,'') NOT LIKE '%model-catalog%'
ORDER BY CASE upper(a.currency)
	WHEN 'EUR' THEN a.price
	WHEN 'RSD' THEN a.price/?
	WHEN 'DIN' THEN a.price/?
	WHEN 'BAM' THEN a.price/1.95583
	WHEN 'KM' THEN a.price/1.95583
	WHEN 'USD' THEN a.price*0.92
	ELSE 0 END DESC`, rate, rate)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []candidate
	for rows.Next() && len(out) < budget {
		var c candidate
		var price float64
		var currency string
		if err := rows.Scan(&c.AdID, &c.Title, &c.Desc, &c.AttrsJSON, &price, &currency); err != nil {
			return out
		}
		c.PriceEUR = pricing.ToEUR(price, currency, rate)
		if c.PriceEUR < 10 || c.PriceEUR > 5000 {
			continue
		}
		out = append(out, c)
	}
	return out
}

func askGemini(ctx context.Context, gem *vision.GeminiClient, c candidate) (geminiSpecs, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Заголовок: %s\n", c.Title)
	if model := specs.ExtractLaptopModel(c.Title + " " + stripHTML(c.Desc)); model != "" {
		fmt.Fprintf(&b, "Модель из текста: %s\n", model)
	}
	fmt.Fprintf(&b, "Описание: %s\n", truncateStr(stripHTML(c.Desc), 2500))
	if c.AttrsJSON != "" && c.AttrsJSON != "[]" {
		fmt.Fprintf(&b, "Атрибуты объявления: %s\n", truncateStr(c.AttrsJSON, 500))
	}
	out, err := gem.Generate(ctx, specs.GeminiSpecsPrompt, []vision.Part{{Text: b.String()}})
	if err != nil {
		return geminiSpecs{}, err
	}
	return parseSpecsJSON(out)
}

// parseSpecsJSON — делегирование общему парсеру specs (SSOT).
func parseSpecsJSON(raw string) (geminiSpecs, error) {
	return specs.ParseGeminiSpecs(raw)
}

// writeBack — результат в research_specs (source='gemini-text'); пустой
// результат тоже пишется — повторный вызов по лоту запрещён.
func writeBack(ctx context.Context, db *sql.DB, adID int64, sp geminiSpecs,
	cpus map[string]hw.CPU, gpus map[string]hw.GPU) error {
	if err := ensureResearchSpecsLaptopModelColumn(ctx, db); err != nil {
		return err
	}
	cpuModel, cpuScore := strings.TrimSpace(sp.CPU), 0.0
	if cpuModel != "" {
		if c, ok := cpus[hw.Key(cpuModel)]; ok {
			cpuModel, cpuScore = c.Name, c.Score
		}
	}
	gpuModel, gpuScore := strings.TrimSpace(sp.GPU), 0.0
	if gpuModel != "" {
		if g, ok := gpus[hw.Key(gpuModel)]; ok {
			gpuModel, gpuScore = g.Name, g.Score
		}
	}
	_, err := db.ExecContext(ctx, `
INSERT INTO research_specs (ad_id, laptop_model, cpu_model, cpu_score, ram_gb, ssd_gb, gpu_model, gpu_score, updated_at, source)
VALUES (?,?,?,?,?,?,?,?,?, 'gemini-text')
ON CONFLICT(ad_id) DO UPDATE SET
	laptop_model=CASE WHEN excluded.laptop_model != '' THEN excluded.laptop_model ELSE research_specs.laptop_model END,
	cpu_model=CASE WHEN excluded.cpu_model != '' THEN excluded.cpu_model ELSE research_specs.cpu_model END,
	cpu_score=CASE WHEN excluded.cpu_model != '' THEN excluded.cpu_score ELSE research_specs.cpu_score END,
	ram_gb=CASE WHEN excluded.ram_gb > 0 THEN excluded.ram_gb ELSE research_specs.ram_gb END,
	ssd_gb=CASE WHEN excluded.ssd_gb > 0 THEN excluded.ssd_gb ELSE research_specs.ssd_gb END,
	gpu_model=CASE WHEN excluded.gpu_model != '' THEN excluded.gpu_model ELSE research_specs.gpu_model END,
	gpu_score=CASE WHEN excluded.gpu_model != '' THEN excluded.gpu_score ELSE research_specs.gpu_score END,
	updated_at=excluded.updated_at, source=excluded.source`,
		adID, strings.TrimSpace(sp.LaptopModel), cpuModel, cpuScore, sp.RAMGB, sp.SSDGB, gpuModel, gpuScore, time.Now().Unix())
	return err
}

func ensureResearchSpecsLaptopModelColumn(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(research_specs)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	seenTable := false
	for rows.Next() {
		seenTable = true
		var (
			cid     int
			name    string
			typ     string
			notNull int
			def     sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &def, &pk); err != nil {
			return err
		}
		if strings.EqualFold(name, "laptop_model") {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !seenTable {
		return fmt.Errorf("research_specs table is missing")
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE research_specs ADD COLUMN laptop_model TEXT NOT NULL DEFAULT ''`); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return nil
		}
		return err
	}
	return nil
}

func stripHTML(s string) string {
	s = htmlTagRe.ReplaceAllString(s, " ")
	for _, pair := range [][2]string{{"&nbsp;", " "}, {"&amp;", "&"}, {"&quot;", "\""}, {"&#39;", "'"}, {"&lt;", "<"}, {"&gt;", ">"}} {
		s = strings.ReplaceAll(s, pair[0], pair[1])
	}
	return strings.Join(strings.Fields(s), " ")
}

func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
