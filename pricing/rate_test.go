package pricing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchRsdEur(t *testing.T) {
	// Нормальный ответ frankfurter: 1 EUR = 117.23 RSD.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"amount":1.0,"base":"EUR","date":"2026-08-04","rates":{"RSD":117.23}}`))
	}))
	defer ok.Close()

	rate, src := FetchRsdEur(context.Background(), ok.URL)
	if rate != 117.23 || src == "fallback" {
		t.Errorf("rate=%.2f src=%s, жду 117.23 не-fallback", rate, src)
	}

	// Внезапный курс за пределами здравого смысла → fallback.
	weird := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"rates":{"RSD":5}}`))
	}))
	defer weird.Close()
	rate, src = FetchRsdEur(context.Background(), weird.URL)
	if rate != FallbackRsdEur || src != "fallback" {
		t.Errorf("дикий курс: rate=%.2f src=%s, жду fallback", rate, src)
	}

	// Недоступный сервер → fallback.
	rate, src = FetchRsdEur(context.Background(), "http://127.0.0.1:1/none")
	if rate != FallbackRsdEur || src != "fallback" {
		t.Errorf("недоступный API: rate=%.2f src=%s, жду fallback", rate, src)
	}
}
