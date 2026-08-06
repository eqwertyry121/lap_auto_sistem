// smoke — быстрая проверка живых ключей без запуска полного бота:
//   1) Gemini отвечает на тестовый запрос (валидность GEMINI_API_KEY и модели);
//   2) Telegram шлёт сообщение (если заданы токен и chat_id).
package main

import (
	"context"
	"fmt"
	"time"

	"kpbot/collector"
	"kpbot/config"
	"kpbot/notifier"
	"kpbot/vision"
)

func main() {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	fmt.Println("=== SMOKE-ТЕСТ ===")

	// 1. Gemini: текст + vision
	if cfg.GeminiAPIKey == "" {
		fmt.Println("[GEMINI] GEMINI_API_KEY не задан — пропускаю")
	} else {
		g := vision.NewGeminiClient(cfg.GeminiAPIKey, cfg.GeminiModel)

		out, err := g.Generate(ctx, "Ты тест. Ответь строго JSON.", []vision.Part{{Text: `Верни ровно один JSON-объект: {"ok":true}`}})
		if err != nil {
			fmt.Println("[GEMINI-TEXT] ОШИБКА:", err)
		} else {
			fmt.Printf("[GEMINI-TEXT] OK, модель=%s, ответ: %s\n", cfg.GeminiModel, truncate(out, 120))
		}

		// Vision: берём одно реальное фото ноутбука с KP и просим Gemini его описать.
		if photoURL := samplePhoto(ctx); photoURL != "" {
			img, err := g.ImageToPart(ctx, photoURL)
			if err != nil {
				fmt.Println("[GEMINI-VISION] не удалось скачать фото:", err)
			} else {
				vOut, err := g.Generate(ctx, "Опиши ноутбук на фото одним предложением. Ответь строго JSON.",
					[]vision.Part{{Text: `Верни JSON: {"what":"..."}`}, *img})
				if err != nil {
					fmt.Println("[GEMINI-VISION] ОШИБКА:", err)
				} else {
					fmt.Printf("[GEMINI-VISION] OK, ответ: %s\n", truncate(vOut, 160))
				}
			}
		} else {
			fmt.Println("[GEMINI-VISION] не нашёл фото на KP — пропускаю")
		}
	}

	// 2. Telegram
	tg := notifier.New(cfg.TelegramToken, cfg.TelegramChatID)
	if !tg.Enabled() {
		fmt.Println("[TELEGRAM] токен или CHAT_ID не заданы — пропускаю отправку")
	} else {
		msg := "✅ <b>Smoke-тест KP Laptop Bot</b>\nЕсли вы это видите, Telegram подключён."
		if err := tg.SendRaw(ctx, msg); err != nil {
			fmt.Println("[TELEGRAM] ОШИБКА:", err)
		} else {
			fmt.Println("[TELEGRAM] OK — тестовое сообщение отправлено")
		}
	}

	fmt.Println("=== ЗАВЕРШЕНО ===")
}

// samplePhoto — одно фото ноутбука с KP (первое попавшееся объявление с фото).
func samplePhoto(ctx context.Context) string {
	kp := collector.NewClient()
	ads, err := kp.Search(ctx)
	if err != nil || len(ads) == 0 {
		return ""
	}
	for _, ad := range ads[:min(len(ads), 5)] {
		d, err := kp.FetchDetail(ctx, ad.AdID)
		if err != nil || len(d.Photos) == 0 {
			continue
		}
		if u := d.Photos[0].BestURL(); u != "" {
			return u
		}
	}
	return ""
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
