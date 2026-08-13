//go:build live

package collector

import (
	"context"
	"testing"
	"time"

	"github.com/joho/godotenv"
)

// Проверка реального KP API. Запуск: go test -tags live ./collector -run TestLive -v
func TestLive(t *testing.T) {
	_ = godotenv.Load()
	c := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ads, err := c.Search(ctx)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(ads) == 0 {
		t.Fatal("пустой список объявлений")
	}
	t.Logf("объявлений на странице: %d", len(ads))
	for i, a := range ads {
		if i >= 5 {
			break
		}
		t.Logf("  id=%d %q price=%.2f %s loc=%q url=%s", a.AdID, a.Name, a.Price, a.Currency, a.LocationName, a.URL())
	}

	// Ищем объявление с фото и берём его детали
	var target = ads[0]
	d, err := c.FetchDetail(ctx, target.AdID)
	if err != nil {
		t.Fatalf("fetch detail: %v", err)
	}
	t.Logf("detail id=%d %q: описание %d симв., фото %d, атрибутов %d, продавец %q",
		d.AdID, d.Name, len(d.Description), len(d.Photos), len(d.Attributes), d.Seller())
	if len(d.Photos) > 0 {
		t.Logf("  первое фото (big): %s", d.Photos[0].BestURL())
	}
	for i, a := range d.Attributes {
		if i >= 5 {
			break
		}
		t.Logf("  attr: %s = %s", a.Name, a.Value)
	}
}
