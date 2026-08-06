package storage

import (
	"strings"
	"testing"
)

func TestTokenize(t *testing.T) {
	tokens := tokenize("Lenovo ThinkPad T490 i5-8265U 16GB SSD laptop polovan")
	for _, want := range []string{"lenovo", "thinkpad", "t490", "i5", "8265u", "16gb", "ssd"} {
		if _, ok := tokens[want]; !ok {
			t.Errorf("ожидал токен %q в %v", want, tokens)
		}
	}
	// стоп-слова не должны попадать
	for _, stop := range []string{"laptop", "polovan"} {
		if _, ok := tokens[stop]; ok {
			t.Errorf("стоп-слово %q не должно быть токеном", stop)
		}
	}
}

func TestSharedCount(t *testing.T) {
	a := map[string]struct{}{"lenovo": {}, "thinkpad": {}, "t490": {}}
	b := map[string]struct{}{"lenovo": {}, "thinkpad": {}, "x1": {}}
	if got := sharedCount(a, b); got != 2 {
		t.Fatalf("sharedCount = %d, хочу 2", got)
	}
}

func newCacheWith(entries []priceEntry) *PriceCache {
	return &PriceCache{entries: entries}
}

func mkEntry(price float64, title string) priceEntry {
	return priceEntry{tokens: tokenize(title), price: price}
}

func TestHintForEnoughData(t *testing.T) {
	c := newCacheWith([]priceEntry{
		mkEntry(200, "Lenovo ThinkPad T490 i5 8GB"),
		mkEntry(250, "Lenovo ThinkPad T490 i7 16GB"),
		mkEntry(300, "Lenovo ThinkPad T490 i5 16GB"),
		mkEntry(1000, "Apple MacBook Pro 16 M2"), // не похож
	})
	hint := c.HintFor("Lenovo ThinkPad T490 i5 8GB SSD")
	if hint == "" {
		t.Fatal("ожидал непустую сводку для похожих лотов")
	}
	if !strings.Contains(hint, "медиана") {
		t.Errorf("сводка должна содержать медиану: %q", hint)
	}
	t.Logf("hint: %s", hint)
}

func TestHintForTooFew(t *testing.T) {
	c := newCacheWith([]priceEntry{
		mkEntry(200, "Lenovo ThinkPad T490 i5 8GB"),
		mkEntry(250, "Lenovo ThinkPad T490 i7 16GB"),
	})
	hint := c.HintFor("Lenovo ThinkPad T490 i5 8GB SSD")
	if hint != "" {
		t.Fatalf("при <3 похожих жду пустую строку, получил %q", hint)
	}
}
