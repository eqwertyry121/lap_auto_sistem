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
	stats := g.Stats()
	if stats.CallsToday != 1 || stats.DailyLimit != 10 || stats.LastSuccess.IsZero() {
		t.Fatalf("Stats = %+v, want one call, limit 10 and last success", stats)
	}
}

func TestGeminiRecordsTokenUsageAndCost(t *testing.T) {
	g := NewGeminiClient("key", "gemini-2.5-flash-lite").SetLimits(1, 10)
	g.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return geminiResponse(http.StatusOK, `{
			"candidates":[{"content":{"parts":[{"text":"{\"ok\":true}"}]}}],
			"usageMetadata":{
				"promptTokenCount":1000,
				"candidatesTokenCount":100,
				"thoughtsTokenCount":50,
				"totalTokenCount":1150
			}
		}`), nil
	})}

	if _, err := g.Generate(context.Background(), "", []Part{{Text: "test"}}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	stats := g.Stats()
	if stats.PromptTokensToday != 1000 || stats.OutputTokensToday != 150 || stats.TotalTokensToday != 1150 {
		t.Fatalf("token stats = %+v", stats)
	}
	if stats.EstimatedCostUSD < 0.000159 || stats.EstimatedCostUSD > 0.000161 {
		t.Fatalf("EstimatedCostUSD = %.9f, want about 0.000160", stats.EstimatedCostUSD)
	}
}

func TestGeminiPriceAliasesAreEstimated(t *testing.T) {
	cases := []struct {
		model string
		want  geminiPrice
	}{
		{"gemini-flash-lite-latest", geminiPrice{inputUSDPerMTok: 0.10, outputUSDPerMTok: 0.40}},
		{"models/gemini-flash-latest", geminiPrice{inputUSDPerMTok: 0.30, outputUSDPerMTok: 2.50}},
		{"gemini-pro-latest", geminiPrice{inputUSDPerMTok: 1.25, outputUSDPerMTok: 10.00}},
	}
	for _, c := range cases {
		t.Run(c.model, func(t *testing.T) {
			got, ok := geminiPriceForModel(c.model)
			if !ok {
				t.Fatalf("geminiPriceForModel(%q) not recognized", c.model)
			}
			if got != c.want {
				t.Fatalf("geminiPriceForModel(%q) = %+v, want %+v", c.model, got, c.want)
			}
		})
	}
}

func TestGeminiDailyBudgetBlocksAfterSpend(t *testing.T) {
	var calls atomic.Int32
	g := NewGeminiClient("key", "gemini-2.5-flash-lite").
		SetLimits(1, 10).
		SetDailyBudgetUSD(0.000001)
	g.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return geminiResponse(http.StatusOK, `{
			"candidates":[{"content":{"parts":[{"text":"{\"ok\":true}"}]}}],
			"usageMetadata":{"promptTokenCount":1000,"candidatesTokenCount":100,"totalTokenCount":1100}
		}`), nil
	})}

	if _, err := g.Generate(context.Background(), "", []Part{{Text: "first"}}); err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	if _, err := g.Generate(context.Background(), "", []Part{{Text: "second"}}); err == nil {
		t.Fatal("second Generate must fail on daily cost budget")
	}
	if calls.Load() != 1 {
		t.Fatalf("transport calls = %d, want 1", calls.Load())
	}
	if stats := g.Stats(); stats.DailyBudgetUSD != 0.000001 {
		t.Fatalf("DailyBudgetUSD = %.9f", stats.DailyBudgetUSD)
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
