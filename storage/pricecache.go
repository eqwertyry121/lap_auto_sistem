package storage

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"

	"kpbot/models"
)

// PriceCache — in-memory срез рыночных цен из БД (SSOT). Обновляется
// фоновым воркером раз в PRICE_CACHE_REFRESH_MIN. Даёт Gemini рыночную
// сводку по похожим лотам.
type PriceCache struct {
	mu      sync.RWMutex
	store   *Store
	days    int
	entries []priceEntry
}

type priceEntry struct {
	tokens map[string]struct{}
	price  float64
}

func NewPriceCache(store *Store, days int) *PriceCache {
	return &PriceCache{store: store, days: days}
}

var tokenRe = regexp.MustCompile(`[a-z0-9]+`)

// Слова, не несущие информации о модели/железе.
var stopWords = map[string]struct{}{
	"laptop": {}, "laptopovi": {}, "laptopa": {}, "prodajem": {}, "prodaja": {},
	"prodam": {}, "polovan": {}, "novo": {}, "zamena": {}, "ocuvan": {}, "ocuvani": {},
	"super": {}, "top": {}, "hit": {}, "sa": {}, "za": {}, "je": {}, "su": {}, "ili": {},
	"the": {}, "and": {}, "kom": {}, "din": {}, "eur": {}, "rsd": {}, "rs": {},
}

func tokenize(title string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, tok := range tokenRe.FindAllString(strings.ToLower(title), -1) {
		if len(tok) < 2 {
			continue
		}
		if _, skip := stopWords[tok]; skip {
			continue
		}
		out[tok] = struct{}{}
	}
	return out
}

func (c *PriceCache) Refresh(ctx context.Context) {
	rows, err := c.store.RecentPrices(ctx, c.days, 5000)
	if err != nil {
		slog.Error("price cache: обновление не удалось", "err", err)
		return
	}
	entries := make([]priceEntry, 0, len(rows))
	for _, r := range rows {
		tokens := tokenize(r.Title)
		if len(tokens) == 0 {
			continue
		}
		entries = append(entries, priceEntry{tokens: tokens, price: r.Price})
	}
	c.mu.Lock()
	c.entries = entries
	c.mu.Unlock()
	slog.Info("price cache обновлён", "записей", len(entries))
}

// HintFor — рыночная сводка по лотам, похожим на title (≥2 общих токенов).
// Пустая строка, если данных недостаточно.
func (c *PriceCache) HintFor(title string) string {
	query := tokenize(title)
	if len(query) == 0 {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()

	var prices []float64
	for _, e := range c.entries {
		if sharedCount(query, e.tokens) >= 2 {
			prices = append(prices, e.price)
		}
	}
	if len(prices) < 3 {
		return ""
	}
	sort.Float64s(prices)
	n := len(prices)
	return fmt.Sprintf("похожие лоты на рынке (n=%d): медиана %s, диапазон %s–%s",
		n,
		models.FormatPrice(prices[n/2], "EUR"),
		models.FormatPrice(prices[0], "EUR"),
		models.FormatPrice(prices[n-1], "EUR"))
}

func sharedCount(a, b map[string]struct{}) int {
	small, big := a, b
	if len(small) > len(big) {
		small, big = big, small
	}
	n := 0
	for k := range small {
		if _, ok := big[k]; ok {
			n++
		}
	}
	return n
}
