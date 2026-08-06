// market-scan — сбор всей рыночной базы ноутбуков KupujemProdajem.
//
// Проход по всем страницам Search API (порядок: свежие → старые), каждое
// объявление пишется в market_listings со статусом SCANNED. Это фундамент
// для кэша цен и для экспорта рыночной истории в CSV.
//
// Запуск:
//
//	go run ./cmd/market-scan                     // полный обход до конца выдачи
//	go run ./cmd/market-scan -max-pages 10       // только первые 10 страниц (проба)
//	go run ./cmd/market-scan -delay-ms 2500      // медленнее, чтобы не злить KP
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"kpbot/collector"
	"kpbot/models"
	"kpbot/storage"
)

func main() {
	var (
		dbPath       = flag.String("db", envOr("DB_PATH", "data/kp_bot.db"), "путь к SQLite-базе")
		maxPages     = flag.Int("max-pages", 0, "максимум страниц (0 — до конца выдачи)")
		delayMS      = flag.Int("delay-ms", 1500, "пауза между страницами, мс")
		stopOnStaleN = flag.Int("stale-pages", 5, "остановиться после N страниц подряд без новых лотов (0 — не останавливаться)")
	)
	flag.Parse()

	_ = godotenv.Load()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	log := slog.Default()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := storage.Open(*dbPath)
	if err != nil {
		log.Error("не удалось открыть базу", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	client := collector.NewClient()

	log.Info("сканирование рынка ноутбуков KP",
		"db", *dbPath, "max_pages", *maxPages, "delay_ms", *delayMS)

	started := time.Now()
	var (
		page       = 1
		seen       int
		inserted   int
		staleRun   int // страницы подряд без новых лотов
		rateLimits int
		totalPages int // заявленное KP число страниц выдачи
	)

	for {
		if ctx.Err() != nil {
			log.Warn("остановлено пользователем")
			break
		}
		if *maxPages > 0 && page > *maxPages {
			log.Info("достигнут лимит страниц", "max_pages", *maxPages)
			break
		}

		res, err := client.SearchPageFull(ctx, page)
		if err != nil {
			if errors.Is(err, collector.ErrRateLimited) {
				rateLimits++
				if rateLimits > 3 {
					log.Error("KP трижды вернул 429 подряд — останавливаюсь", "page", page)
					break
				}
				backoff := time.Duration(30*rateLimits) * time.Second
				log.Warn("429 Too Many Requests — откат назад", "page", page, "backoff", backoff)
				if !sleepCtx(ctx, backoff) {
					break
				}
				continue // повторяем ту же страницу
			}
			if ctx.Err() != nil {
				break
			}
			log.Error("ошибка запроса страницы", "page", page, "err", err)
			// одна повторная попытка после паузы, затем выход
			if !sleepCtx(ctx, 5*time.Second) {
				break
			}
			if res2, err2 := client.SearchPageFull(ctx, page); err2 == nil {
				res = res2
			} else {
				break
			}
		} else {
			rateLimits = 0
		}

		ads := res.Ads
		if len(ads) == 0 {
			log.Info("выдача закончилась", "page", page)
			break
		}

		// Запоминаем заявленное KP число страниц, чтобы не уйти в бесконечный
		// «хвост» (за пределом KP повторяет последние объявления).
		if totalPages == 0 && res.Pages > 0 {
			totalPages = res.Pages
			log.Info("KP заявил предел выдачи", "pages", totalPages, "total_ads", res.Total)
		}

		newOnPage := 0
		for _, ad := range ads {
			seen++
			exists, err := store.Exists(ctx, ad.AdID)
			if err != nil {
				log.Error("проверка дубля", "ad_id", ad.AdID, "err", err)
				continue
			}
			if exists {
				continue
			}
			cur := models.NormalizeCurrency(ad.Currency)
			l := models.Listing{
				AdID:      ad.AdID,
				Title:     ad.Name,
				Price:     float64(ad.Price),
				Currency:  cur,
				URL:       ad.URL(),
				Status:    models.StatusScanned,
				CreatedAt: time.Now(),
			}
			if err := store.InsertListing(ctx, l); err != nil {
				log.Error("запись лота", "ad_id", ad.AdID, "err", err)
				continue
			}
			newOnPage++
			inserted++
		}

		log.Info("страница обработана",
			"page", page, "ads", len(ads), "new", newOnPage, "total_new", inserted)

		// Страницы без новинок считаем «хвостом» только если в этом запуске
		// уже что-то собрано — иначе заранее заполненный префикс (стр. 1..N)
		// остановил бы сбор в самом начале.
		if newOnPage == 0 && inserted > 0 {
			staleRun++
			if *stopOnStaleN > 0 && staleRun >= *stopOnStaleN {
				log.Info("хвост выдачи: одни дубли — остановка",
					"stale_pages", staleRun)
				break
			}
		} else {
			staleRun = 0
		}

		// KP сам сообщает, что дальше страниц нет.
		if res.HasReachedLimit || res.HasReachedMax {
			log.Info("KP: достигнут предел выдачи", "page", page)
			break
		}

		// Лимит по заявленному KP числу страниц (защита от бесконечного хвоста).
		if totalPages > 0 && page >= totalPages {
			log.Info("достигнут заявленный KP лимит страниц", "page", page, "total_pages", totalPages)
			break
		}

		page++
		if !sleepCtx(ctx, time.Duration(*delayMS)*time.Millisecond) {
			break
		}
	}

	total, _ := store.Count(ctx)
	log.Info("сканирование завершено",
		"страниц", page, "просмотрено", seen, "добавлено", inserted,
		"всего_в_базе", total, "длительность", time.Since(started).Round(time.Second))
	fmt.Printf("\nИтог: просмотрено %d объявлений, добавлено %d, всего в базе %d.\n", seen, inserted, total)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
