package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Курс RSD→EUR (PLAN_v4 §6.3, Фаза 6). Источник — open.er-api.com
// (бесплатно, без ключа, RSD поддерживается; frankfurter.app RSD не отдаёт).
// Недоступен — fallback-константа.

// FallbackRsdEur — исторический курс, проверенный на данных проекта.
const FallbackRsdEur = 117.2

const erAPIURL = "https://open.er-api.com/v6/latest/EUR"

// FetchRsdEur — сколько динаров за 1 евро. Возвращает курс и источник
// («er-api» или «fallback») — для отчётности в дайджесте.
func FetchRsdEur(ctx context.Context, url string) (float64, string) {
	if url == "" {
		url = erAPIURL
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return FallbackRsdEur, "fallback"
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return FallbackRsdEur, "fallback"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return FallbackRsdEur, "fallback"
	}
	var payload struct {
		Rates map[string]float64 `json:"rates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return FallbackRsdEur, "fallback"
	}
	rsd, ok := payload.Rates["RSD"]
	// Здравый смысл: курс динара к евро живёт в диапазоне 100–140.
	if !ok || rsd < 100 || rsd > 140 {
		return FallbackRsdEur, "fallback"
	}
	return rsd, fmt.Sprintf("er-api (%s)", time.Now().Format("2006-01-02"))
}
