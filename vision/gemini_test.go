package vision

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func withFastGeminiRetry(t *testing.T) {
	t.Helper()
	oldAttempts := geminiMaxAttempts
	oldBase := geminiRetryBaseDelay
	oldMax := geminiRetryMaxDelay
	oldCooldown := geminiCooldownDelay
	geminiMaxAttempts = 3
	geminiRetryBaseDelay = time.Millisecond
	geminiRetryMaxDelay = time.Millisecond
	geminiCooldownDelay = time.Minute
	t.Cleanup(func() {
		geminiMaxAttempts = oldAttempts
		geminiRetryBaseDelay = oldBase
		geminiRetryMaxDelay = oldMax
		geminiCooldownDelay = oldCooldown
	})
}

func geminiResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestGeminiRetriesRetryableHTTP(t *testing.T) {
	withFastGeminiRetry(t)
	var calls atomic.Int32
	g := NewGeminiClient("key", "model").SetLimits(1, 10)
	g.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		n := calls.Add(1)
		if n < 3 {
			return geminiResponse(http.StatusTooManyRequests, `{"error":{"code":429,"message":"rate"}}`), nil
		}
		return geminiResponse(http.StatusOK, `{"candidates":[{"content":{"parts":[{"text":"{\"ok\":true}"}]}}]}`), nil
	})}

	out, err := g.Generate(context.Background(), "", []Part{{Text: "test"}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if out != `{"ok":true}` {
		t.Fatalf("out = %q", out)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
	if g.limiter.calls != 1 {
		t.Fatalf("daily calls = %d, want 1", g.limiter.calls)
	}
	if g.LastSuccess().IsZero() {
		t.Fatal("LastSuccess was not recorded")
	}
}

func TestGeminiCooldownSkipsCallAndDailyReserve(t *testing.T) {
	withFastGeminiRetry(t)
	geminiMaxAttempts = 1
	geminiCooldownDelay = time.Hour

	var calls atomic.Int32
	g := NewGeminiClient("key", "model").SetLimits(1, 10)
	g.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return geminiResponse(http.StatusTooManyRequests, `{"error":{"code":429,"message":"rate"}}`), nil
	})}

	if _, err := g.Generate(context.Background(), "", []Part{{Text: "first"}}); err == nil {
		t.Fatal("first Generate must fail")
	}
	if _, err := g.Generate(context.Background(), "", []Part{{Text: "second"}}); err == nil {
		t.Fatal("second Generate must fail on cooldown")
	}
	if calls.Load() != 1 {
		t.Fatalf("transport calls = %d, want 1", calls.Load())
	}
	if g.limiter.calls != 1 {
		t.Fatalf("daily calls = %d, want 1", g.limiter.calls)
	}
}
