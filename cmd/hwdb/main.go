// hwdb — наполнение/обновление эталонной базы «железа» из рейтингов
// chaynikam.info.
//
// Источник: https://www.chaynikam.info/cpu_table.html — одна страница с полным
// рейтингом CPU (балл быстродействия, частоты, ядра/потоки). Данные лежат в
// data/hw.db (hw_cpu) и используются движком «цена/качество» для лотов KP:
// балл — это измеримая мощность, на которую делится цена.
//
// Запуск:
//
//	go run ./cmd/hwdb                          # скачать рейтинг и обновить БД
//	go run ./cmd/hwdb -src tools/cpu_table.html  # из уже скачанного файла
package main

import (
	"context"
	"flag"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultCPUURL = "https://www.chaynikam.info/cpu_table.html"
	defaultGPUURL = "https://www.chaynikam.info/gpu_specif.html"
)

func main() {
	var (
		dbPath = flag.String("db", "data/hw.db", "SQLite-файл эталонной базы железа")
		src    = flag.String("src", defaultCPUURL, "URL или локальный файл с рейтингом CPU")
		gpuSrc = flag.String("gpu", defaultGPUURL, "URL или локальный файл с рейтингом GPU")
	)
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	log := slog.Default()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	body, err := readSource(ctx, *src)
	if err != nil {
		log.Error("не удалось получить рейтинг", "src", *src, "err", err)
		os.Exit(1)
	}

	cpus := parseCPURating(body)
	if len(cpus) < 1000 {
		log.Error("подозрительно мало распознано CPU — страница поменялась?", "rows", len(cpus))
		os.Exit(1)
	}

	store, err := openHW(*dbPath)
	if err != nil {
		log.Error("не удалось открыть базу железа", "err", err)
		os.Exit(1)
	}
	defer store.close()

	if err := store.replaceCPUs(ctx, cpus); err != nil {
		log.Error("запись рейтинга в БД", "err", err)
		os.Exit(1)
	}
	total, _ := store.countCPUs(ctx)
	log.Info("эталонная база CPU обновлена", "db", *dbPath, "cpu", total)

	// Самопроверка по заведомо известным моделям.
	for _, probe := range []string{"Intel Core i5-1135G7", "AMD Ryzen 5 4600H", "Intel Core i7-8550U"} {
		c, err := store.findByName(ctx, probe)
		if err != nil {
			log.Warn("эталон не найден", "name", probe)
			continue
		}
		log.Info("проверка", "name", c.Name, "rank", c.Rank, "score", c.Score,
			"ядра_потоки", fmt.Sprintf("%d/%d", c.Cores, c.Threads))
	}

	fmt.Printf("\nТоп-5 CPU по быстродействию:\n")
	for _, c := range cpus[:min(5, len(cpus))] {
		fmt.Printf("  %4d. %-45s %10.2f\n", c.Rank, c.Name, c.Score)
	}

	// --- GPU ---
	gpuBody, err := readSource(ctx, *gpuSrc)
	if err != nil {
		log.Error("не удалось получить рейтинг GPU", "src", *gpuSrc, "err", err)
		os.Exit(1)
	}
	gpus := parseGPURating(gpuBody)
	if len(gpus) < 100 {
		log.Error("подозрительно мало распознано GPU — страница поменялась?", "rows", len(gpus))
		os.Exit(1)
	}
	if err := store.replaceGPUs(ctx, gpus); err != nil {
		log.Error("запись рейтинга GPU в БД", "err", err)
		os.Exit(1)
	}
	gpuTotal, _ := store.countGPUs(ctx)
	log.Info("эталонная база GPU обновлена", "gpu", gpuTotal)

	fmt.Printf("\nТоп-5 GPU по быстродействию:\n")
	for _, g := range gpus[:min(5, len(gpus))] {
		fmt.Printf("  %4d. %-45s %10.0f\n", g.Rank, g.Name, g.Score)
	}
}

// readSource — локальный файл, если существует; иначе скачивание по URL.
func readSource(ctx context.Context, src string) ([]byte, error) {
	if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
		return os.ReadFile(src)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36")
	req.Header.Set("accept", "text/html,application/xhtml+xml")
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

var (
	rowStartRe = regexp.MustCompile(`<tr oncontextmenu="tdcont\('(\d+)'`)
	slugRe     = regexp.MustCompile(`<a href="([^"]+)\.html"`)
	nameRe     = regexp.MustCompile(`<noscript><a href="[^"]+\.html"[^>]*></noscript>([^<]+)<noscript>`)
	scoreRe    = regexp.MustCompile(`<span class="spandiag"[^>]*>([^<]+)</span>`)
	freqRe     = regexp.MustCompile(`<td class="cpus4">([^<]*)</td>`)
	boostRe    = regexp.MustCompile(`<td class="cpus5">([^<]*)</td>`)
	coresRe    = regexp.MustCompile(`<td class="cpus6">([^<]*)</td>`)
	numRe      = regexp.MustCompile(`\d+`)
)

// parseCPURating разбирает одну большую HTML-таблицу рейтинга на строки.
func parseCPURating(body []byte) []hwCPU {
	text := string(body)
	starts := rowStartRe.FindAllStringSubmatchIndex(text, -1)

	out := make([]hwCPU, 0, len(starts))
	for i, loc := range starts {
		end := len(text)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		chunk := text[loc[0]:end]

		rank, _ := strconv.Atoi(chunk[loc[2]-loc[0] : loc[3]-loc[0]])
		c := hwCPU{Rank: rank}

		if m := slugRe.FindStringSubmatch(chunk); m != nil {
			c.Slug = m[1]
		}
		if m := nameRe.FindStringSubmatch(chunk); m != nil {
			c.Name = strings.TrimSpace(html.UnescapeString(m[1]))
		}
		if m := scoreRe.FindStringSubmatch(chunk); m != nil {
			c.Score = parseFloat(strings.ReplaceAll(m[1], ",", "."))
		}
		if m := freqRe.FindStringSubmatch(chunk); m != nil {
			c.FreqMHz = parseMHz(m[1])
		}
		if m := boostRe.FindStringSubmatch(chunk); m != nil {
			c.BoostMHz = parseMHz(m[1])
		}
		if m := coresRe.FindStringSubmatch(chunk); m != nil {
			c.Cores, c.Threads = parseCores(m[1])
		}

		if c.Slug == "" || c.Name == "" || c.Score == 0 {
			continue // битая строка
		}
		out = append(out, c)
	}
	return out
}

var (
	gpuNameRe  = regexp.MustCompile(`<td class="gpu2"><a href="([^"]+)\.html">([^<]+)</a></td>`)
	gpuScoreRe = regexp.MustCompile(`<div class="gpuperf"[^>]*>([0-9]+)</div>`)
	gpuRankRe  = regexp.MustCompile(`<td class="gpu">(\d+)</td>`)
)

// parseGPURating разбирает рейтинг видеокарт: якорь — строка с именем,
// ранг в <td class="gpu"> перед ней, балл в <div class="gpuperf"> после.
func parseGPURating(body []byte) []hwGPU {
	text := string(body)
	anchors := gpuNameRe.FindAllStringSubmatchIndex(text, -1)

	out := make([]hwGPU, 0, len(anchors))
	for _, a := range anchors {
		g := hwGPU{
			Slug: text[a[2]:a[3]],
			Name: strings.TrimSpace(html.UnescapeString(text[a[4]:a[5]])),
		}
		if ms := gpuRankRe.FindAllStringSubmatch(text[:a[0]], -1); len(ms) > 0 {
			g.Rank, _ = strconv.Atoi(ms[len(ms)-1][1])
		}
		after := text[a[1]:min(a[1]+400, len(text))]
		if m := gpuScoreRe.FindStringSubmatch(after); m != nil {
			g.Score = parseFloat(m[1])
		}
		if g.Slug == "" || g.Name == "" || g.Score == 0 {
			continue
		}
		out = append(out, g)
	}
	return out
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

// parseMHz достаёт число из «2400 MHz»; «нет» → 0.
func parseMHz(s string) int {
	if m := numRe.FindString(s); m != "" {
		n, _ := strconv.Atoi(m)
		return n
	}
	return 0
}

// parseCores — «4 / 8» → (4, 8).
func parseCores(s string) (int, int) {
	nums := numRe.FindAllString(s, -1)
	if len(nums) < 2 {
		return 0, 0
	}
	cores, _ := strconv.Atoi(nums[0])
	threads, _ := strconv.Atoi(nums[1])
	return cores, threads
}
