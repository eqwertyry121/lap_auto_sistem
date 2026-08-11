package notifier

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// fakeTG — тестовый Telegram-сервер: ok переключается атомарно.
func fakeTG(t *testing.T, ok *atomic.Bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if ok.Load() {
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":77}}`))
		} else {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"description":"flood"}`))
		}
	}))
}

func TestSendRawDirectReturnsMessageID(t *testing.T) {
	var ok atomic.Bool
	ok.Store(true)
	srv := fakeTG(t, &ok)
	defer srv.Close()
	tg := newTestTG(t, srv)
	id, err := tg.SendRawDirect(context.Background(), "<b>test</b>", "https://kp/x")
	if err != nil {
		t.Fatalf("send raw direct: %v", err)
	}
	if id != 77 {
		t.Fatalf("message id = %d, want 77", id)
	}
}

func newTestTG(t *testing.T, srv *httptest.Server) *Telegram {
	t.Helper()
	return &Telegram{
		token:   "test-token",
		chatID:  "42",
		http:    srv.Client(),
		baseURL: srv.URL,
	}
}

func TestSendRawDirectReturnsError(t *testing.T) {
	var ok atomic.Bool
	srv := fakeTG(t, &ok)
	defer srv.Close()
	tg := newTestTG(t, srv)

	ok.Store(false)
	if _, err := tg.SendRawDirect(context.Background(), "<b>test</b>", "https://kp/x"); err == nil {
		t.Fatal("ждал ошибку отправки")
	}
}

func TestDryRunSendRawDirectDoesNotFail(t *testing.T) {
	tg := &Telegram{}
	if _, err := tg.SendRawDirect(context.Background(), "<b>test</b>", "https://kp/x"); err != nil {
		t.Fatalf("dry-run не должен падать: %v", err)
	}
}
