package notifier

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"kpbot/models"
)

// fakeTG — тестовый Telegram-сервер: ok переключается атомарно.
func fakeTG(t *testing.T, ok *atomic.Bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if ok.Load() {
			_, _ = w.Write([]byte(`{"ok":true}`))
		} else {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"description":"flood"}`))
		}
	}))
}

func newTestTG(t *testing.T, srv *httptest.Server) *Telegram {
	t.Helper()
	return &Telegram{
		token:     "test-token",
		chatID:    "42",
		http:      srv.Client(),
		queuePath: filepath.Join(t.TempDir(), "queue.jsonl"),
		baseURL:   srv.URL,
	}
}

func TestAlertQueueAndFlush(t *testing.T) {
	var ok atomic.Bool
	srv := fakeTG(t, &ok)
	defer srv.Close()
	tg := newTestTG(t, srv)
	ctx := context.Background()

	l := models.Listing{AdID: 1, Title: "Laptop", Price: 240, Currency: "EUR", URL: "https://kp/x"}
	v := models.Verdict{IsDeal: true, EstimatedProfit: 60, Reason: "test", Specs: "i5"}

	// 1) Telegram «лежит» → алерт становится в очередь.
	ok.Store(false)
	if err := tg.SendAlert(ctx, l, v, "hint"); err == nil {
		t.Fatal("ждал ошибку отправки")
	}
	data, err := os.ReadFile(tg.queuePath)
	if err != nil || !strings.Contains(string(data), `"type":"alert"`) {
		t.Fatalf("очередь не создана: %v / %q", err, data)
	}

	// 2) Telegram ожил → новый алерт проходит, очередь доотправляется и чистится.
	ok.Store(true)
	if err := tg.SendNeedCheck(ctx, l, v); err != nil {
		t.Fatalf("повторная отправка: %v", err)
	}
	if _, err := os.Stat(tg.queuePath); !os.IsNotExist(err) {
		t.Fatalf("очередь не рассосалась после успешной отправки: err=%v", err)
	}
}

func TestDryRunDoesNotQueue(t *testing.T) {
	tg := &Telegram{queuePath: filepath.Join(t.TempDir(), "queue.jsonl")}
	l := models.Listing{AdID: 1, Title: "Laptop", Price: 1, Currency: "EUR"}
	if err := tg.SendAlert(context.Background(), l, models.Verdict{}, ""); err != nil {
		t.Fatalf("dry-run не должен падать: %v", err)
	}
	if _, err := os.Stat(tg.queuePath); !os.IsNotExist(err) {
		t.Fatal("dry-run не должен создавать очередь")
	}
}
