// funnel-demo — прогнать воронку L0–L5 по конкретному лоту ВРУЧНУЮ и
// увидеть ВЕСЬ след: каждый шаг L0–L5, каждый запрос к Gemini и ответ.
// Ничего не пишет в kp_bot.db; трассировка дублируется в data/funnel_trace.log.
//
//	go run ./tools/funnel-demo -ad 194306999      # конкретный лот по ad_id
//	go run ./tools/funnel-demo -random            # первый свежий лот из поиска
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"kpbot/collector"
	"kpbot/config"
	"kpbot/funnel"
	"kpbot/models"
	"kpbot/vision"
)

func main() {
	var (
		adID   = flag.Int64("ad", 0, "ad_id лота (из ссылки на объявление)")
		random = flag.Bool("random", false, "взять первый попавшийся свежий лот из поиска")
	)
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	log := slog.Default()

	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	kp := collector.NewClient(collector.WithSharedCooldown(
		cfg.KPCooldownPath, cfg.KPRateCooldown, cfg.KPChallengeCooldown,
	))

	var ad models.SearchAd
	switch {
	case *adID > 0:
		ad = models.SearchAd{AdID: *adID}
	case *random:
		ads, err := kp.Search(ctx)
		if err != nil || len(ads) == 0 {
			fmt.Println("поиск не вернул лотов:", err)
			os.Exit(1)
		}
		ad = ads[0]
		fmt.Printf("Взят свежий лот из поиска: ad_id=%d %q\n\n", ad.AdID, ad.Name)
	default:
		flag.Usage()
		os.Exit(1)
	}

	fmt.Printf("Скачиваю детали /eds/%d ...\n", ad.AdID)
	detail, err := kp.FetchDetail(ctx, ad.AdID)
	if err != nil {
		fmt.Println("детали:", err)
		os.Exit(1)
	}
	// если брали по id без поиска — дополняем поля из деталей
	if ad.Name == "" {
		ad.Name = detail.Name
		ad.Price = detail.Price
		ad.Currency = detail.Currency
		ad.AdURL = detail.AdURL
	}

	fmt.Println("Собираю рыночную модель (research.db + hw.db)...")
	fnl := funnel.NewFunnel()
	fnl.RefreshMarket(ctx, cfg, log)

	gem := vision.NewGeminiClient(cfg.GeminiAPIKey, cfg.GeminiTextModel).
		SetLimits(cfg.GeminiConcurrency, cfg.GeminiDailyLimit).
		SetDailyBudgetUSD(cfg.GeminiDailyBudgetUSD)
	cfg.FunnelTrace = true // трассировка всегда — ради этого и запускаем

	fmt.Println("\n────────── ПРОГОН ВОРОНКИ ──────────")
	out := funnel.Run(ctx, fnl, cfg, gem, log, ad, detail)

	fmt.Println("\n────────── РЕЗУЛЬТАТ ──────────")
	fmt.Printf("Вердикт: %s (статус %s)\n", out.Code, out.Status)
	if out.Audit.Specs != "" {
		fmt.Printf("Конфигурация: %s\n", out.Audit.Specs)
	}
	if out.Audit.Reason != "" {
		fmt.Printf("Служебно: %s\n", out.Audit.Reason)
	}
	if out.AlertText != "" {
		fmt.Println("\nТекст алерта, который ушёл бы в Telegram:")
		fmt.Println(stripTags(out.AlertText))
	}
	fmt.Println("\nПолный след шагов — в data/funnel_trace.log (последний блок).")
}

func stripTags(s string) string {
	out := []rune{}
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			out = append(out, r)
		}
	}
	return string(out)
}
