// checkenv — что реально видит бот в .env (длина значений, без вывода секретов).
package main

import (
	"fmt"

	"kpbot/config"
)

func main() {
	cfg := config.Load()
	fmt.Println("Что видит бот в конфигурации (длины значений):")
	fmt.Printf("  GEMINI_API_KEY               len=%d\n", len(cfg.GeminiAPIKey))
	fmt.Printf("  GEMINI_MODEL                 %q\n", cfg.GeminiModel)
	fmt.Printf("  TELEGRAM_BOT_TOKEN           len=%d\n", len(cfg.TelegramToken))
	fmt.Printf("  TELEGRAM_CHAT_ID             len=%d\n", len(cfg.TelegramChatID))
	fmt.Printf("  CSV_PATH                     %q\n", cfg.CSVPath)
	fmt.Printf("  DB_PATH                      %q\n", cfg.DBPath)

	fmt.Println()
	if cfg.GeminiAPIKey == "" {
		fmt.Println("❌ GEMINI_API_KEY пуст")
	}
	if cfg.TelegramToken == "" || cfg.TelegramChatID == "" {
		fmt.Println("❌ Telegram не будет слать: нужен и TELEGRAM_BOT_TOKEN, и TELEGRAM_CHAT_ID")
	} else {
		fmt.Println("✅ Telegram готов к отправке")
	}
}
