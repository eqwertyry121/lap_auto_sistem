package vision

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

var (
	geminiMaxAttempts    = 3
	geminiRetryBaseDelay = 750 * time.Millisecond
	geminiRetryMaxDelay  = 5 * time.Second
	geminiCooldownDelay  = 2 * time.Minute
)

// GeminiClient — минимальный REST-клиент Gemini (generateContent), без SDK.
type GeminiClient struct {
	apiKey  string
	model   string
	http    *http.Client
	limiter *geminiLimiter
	circuit *geminiCircuit
	stats   *geminiStats
}

type geminiLimiter struct {
	sem        chan struct{}
	dailyLimit int
	mu         sync.Mutex
	day        string
	calls      int
}

type geminiCircuit struct {
	mu     sync.Mutex
	until  time.Time
	reason string
}

type geminiStats struct {
	lastOKUnix atomic.Int64
}

func NewGeminiClient(apiKey, model string) *GeminiClient {
	if model == "" {
		model = "gemini-2.5-flash-lite"
	}
	return &GeminiClient{
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: 90 * time.Second},
		circuit: &geminiCircuit{},
		stats:   &geminiStats{},
	}
}

func (g *GeminiClient) Model() string { return g.model }

func (g *GeminiClient) LastSuccess() time.Time {
	if g == nil || g.stats == nil {
		return time.Time{}
	}
	unix := g.stats.lastOKUnix.Load()
	if unix <= 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}

func (g *GeminiClient) SetLimits(concurrency, dailyLimit int) *GeminiClient {
	if g == nil {
		return nil
	}
	if concurrency <= 0 && dailyLimit <= 0 {
		g.limiter = nil
		return g
	}
	lim := &geminiLimiter{dailyLimit: dailyLimit}
	if concurrency > 0 {
		lim.sem = make(chan struct{}, concurrency)
	}
	g.limiter = lim
	return g
}

// WithModel возвращает лёгкую копию клиента с другой моделью и тем же
// HTTP-клиентом. Это позволяет дешёвым L3-ступеням использовать Flash-Lite,
// не размножая настройки ключа/таймаута.
func (g *GeminiClient) WithModel(model string) *GeminiClient {
	if g == nil || model == "" || model == g.model {
		return g
	}
	cp := *g
	cp.model = model
	return &cp
}

// Part — текстовая или inline-изображение часть запроса.
type Part struct {
	Text       string      `json:"text,omitempty"`
	InlineData *InlineData `json:"inline_data,omitempty"`
}

type InlineData struct {
	MIMEType string `json:"mime_type"`
	Data     string `json:"data"` // base64
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []Part `json:"parts"`
}

type generationConfig struct {
	ResponseMIMEType string  `json:"responseMimeType,omitempty"`
	Temperature      float64 `json:"temperature,omitempty"`
}

// toolSet — инструменты модели. Google Search (grounding) даёт запросу
// реальный доступ в интернет: модель сама ищет и цитирует источники.
type toolSet struct {
	GoogleSearch *struct{} `json:"google_search,omitempty"`
}

type request struct {
	Contents          []content         `json:"contents"`
	SystemInstruction *content          `json:"systemInstruction,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
	Tools             []toolSet         `json:"tools,omitempty"`
}

// groundingMetadata — веб-источники, на которые опёрся ответ с поиском.
type groundingMetadata struct {
	GroundingChunks []struct {
		Web struct {
			URI   string `json:"uri"`
			Title string `json:"title"`
		} `json:"web"`
	} `json:"groundingChunks"`
}

type response struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason      string             `json:"finishReason"`
		GroundingMetadata *groundingMetadata `json:"groundingMetadata,omitempty"`
	} `json:"candidates"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Generate выполняет запрос: системный промпт + части (текст/изображения).
// Возвращает сырой текстовый ответ модели.
func (g *GeminiClient) Generate(ctx context.Context, system string, parts []Part) (string, error) {
	if g.apiKey == "" {
		return "", fmt.Errorf("gemini: GEMINI_API_KEY не задан")
	}

	req := request{
		Contents: []content{{Role: "user", Parts: parts}},
		GenerationConfig: &generationConfig{
			ResponseMIMEType: "application/json",
			Temperature:      0.1,
		},
	}
	if system != "" {
		req.SystemInstruction = &content{Parts: []Part{{Text: system}}}
	}

	gr, err := g.call(ctx, req)
	if err != nil {
		return "", err
	}
	return textOf(gr)
}

// SearchSource — веб-источник из grounding-метаданных (прозрачность
// интернет-ресерча: в алерте видно, на чём основана оценка).
type SearchSource struct {
	URL   string
	Title string
}

// GenerateWithSearch — запрос с включённым поиском Google (grounding):
// модель реально идёт в интернет и опирается на найденное.
// ВАЖНО: google_search несовместим со structured output
// (responseMimeType=application/json) — JSON запрашивается самим промптом
// и парсится терпимо вызывающим кодом.
func (g *GeminiClient) GenerateWithSearch(ctx context.Context, system string, parts []Part) (string, []SearchSource, error) {
	if g.apiKey == "" {
		return "", nil, fmt.Errorf("gemini: GEMINI_API_KEY не задан")
	}

	req := request{
		Contents:         []content{{Role: "user", Parts: parts}},
		GenerationConfig: &generationConfig{Temperature: 0.1},
		Tools:            []toolSet{{GoogleSearch: &struct{}{}}},
	}
	if system != "" {
		req.SystemInstruction = &content{Parts: []Part{{Text: system}}}
	}

	gr, err := g.call(ctx, req)
	if err != nil {
		return "", nil, err
	}
	out, err := textOf(gr)
	if err != nil {
		return "", nil, err
	}

	var sources []SearchSource
	if len(gr.Candidates) > 0 && gr.Candidates[0].GroundingMetadata != nil {
		for _, ch := range gr.Candidates[0].GroundingMetadata.GroundingChunks {
			if ch.Web.URI != "" {
				sources = append(sources, SearchSource{URL: ch.Web.URI, Title: ch.Web.Title})
			}
		}
	}
	return out, sources, nil
}

// call выполняет HTTP-запрос generateContent и разбирает конверт ответа.
func (g *GeminiClient) call(ctx context.Context, req request) (*response, error) {
	if err := g.circuitErr(); err != nil {
		return nil, err
	}
	release, err := g.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("gemini: marshal: %w", err)
	}

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", g.model)
	raw, statusCode, err := g.postGenerate(ctx, url, body)
	if err != nil {
		return nil, err
	}
	g.recordCircuit(statusCode)

	var gr response
	if err := json.Unmarshal(raw, &gr); err != nil {
		return nil, fmt.Errorf("gemini: decode (HTTP %d): %w", statusCode, err)
	}
	if gr.Error != nil {
		return nil, fmt.Errorf("gemini: API error %d: %s", gr.Error.Code, gr.Error.Message)
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini: HTTP %d: %s", statusCode, truncate(string(raw), 300))
	}
	if g.stats != nil {
		g.stats.lastOKUnix.Store(time.Now().Unix())
	}
	return &gr, nil
}

func (g *GeminiClient) postGenerate(ctx context.Context, url string, body []byte) ([]byte, int, error) {
	attempts := geminiMaxAttempts
	if attempts <= 0 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, 0, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("x-goog-api-key", g.apiKey)

		resp, err := g.http.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("gemini: запрос: %w", err)
			if ctx.Err() != nil || attempt == attempts {
				return nil, 0, lastErr
			}
			if err := sleepContext(ctx, geminiRetryDelay(attempt, 0)); err != nil {
				return nil, 0, err
			}
			continue
		}

		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		closeErr := resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("gemini: чтение ответа: %w", readErr)
			if attempt == attempts {
				return nil, resp.StatusCode, lastErr
			}
			if err := sleepContext(ctx, geminiRetryDelay(attempt, retryAfter)); err != nil {
				return nil, 0, err
			}
			continue
		}
		if closeErr != nil {
			lastErr = fmt.Errorf("gemini: закрытие ответа: %w", closeErr)
			if attempt == attempts {
				return nil, resp.StatusCode, lastErr
			}
			if err := sleepContext(ctx, geminiRetryDelay(attempt, retryAfter)); err != nil {
				return nil, 0, err
			}
			continue
		}
		if !geminiRetryableStatus(resp.StatusCode) || attempt == attempts {
			return raw, resp.StatusCode, nil
		}
		if err := sleepContext(ctx, geminiRetryDelay(attempt, retryAfter)); err != nil {
			return nil, 0, err
		}
	}
	return nil, 0, lastErr
}

func geminiRetryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func geminiRetryDelay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 && retryAfter <= geminiRetryMaxDelay {
		return retryAfter
	}
	delay := geminiRetryBaseDelay << max(attempt-1, 0)
	if delay > geminiRetryMaxDelay {
		return geminiRetryMaxDelay
	}
	return delay
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(v); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(v); err == nil {
		return time.Until(when)
	}
	return 0
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (g *GeminiClient) circuitErr() error {
	if g == nil || g.circuit == nil {
		return nil
	}
	g.circuit.mu.Lock()
	defer g.circuit.mu.Unlock()
	if g.circuit.until.IsZero() || time.Now().After(g.circuit.until) {
		return nil
	}
	return fmt.Errorf("gemini: cooldown until %s after %s", g.circuit.until.Format(time.RFC3339), g.circuit.reason)
}

func (g *GeminiClient) recordCircuit(status int) {
	if g == nil || g.circuit == nil {
		return
	}
	g.circuit.mu.Lock()
	defer g.circuit.mu.Unlock()
	if status == http.StatusOK {
		g.circuit.until = time.Time{}
		g.circuit.reason = ""
		return
	}
	if !geminiRetryableStatus(status) {
		return
	}
	until := time.Now().Add(geminiCooldownDelay)
	if until.After(g.circuit.until) {
		g.circuit.until = until
		g.circuit.reason = fmt.Sprintf("HTTP %d", status)
	}
}

// textOf собирает текст ответа из parts кандидата.
func (g *GeminiClient) acquire(ctx context.Context) (func(), error) {
	if g == nil || g.limiter == nil {
		return func() {}, nil
	}
	lim := g.limiter
	acquired := false
	if lim.sem != nil {
		select {
		case lim.sem <- struct{}{}:
			acquired = true
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	release := func() {
		if acquired {
			<-lim.sem
		}
	}
	if err := lim.reserveDaily(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func (l *geminiLimiter) reserveDaily() error {
	if l.dailyLimit <= 0 {
		return nil
	}
	day := time.Now().Format("2006-01-02")
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.day != day {
		l.day = day
		l.calls = 0
	}
	if l.calls >= l.dailyLimit {
		return fmt.Errorf("gemini: daily limit reached (%d)", l.dailyLimit)
	}
	l.calls++
	return nil
}

func textOf(gr *response) (string, error) {
	if len(gr.Candidates) == 0 {
		return "", fmt.Errorf("gemini: пустой candidates")
	}
	var out string
	for _, p := range gr.Candidates[0].Content.Parts {
		out += p.Text
	}
	if out == "" {
		return "", fmt.Errorf("gemini: пустой текст ответа")
	}
	return out, nil
}

// ImageToPart скачивает изображение и пакует его как inline_data.
func (g *GeminiClient) ImageToPart(ctx context.Context, url string) (*Part, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36")
	req.Header.Set("accept", "image/avif,image/webp,image/*,*/*;q=0.8")
	req.Header.Set("referer", "https://www.kupujemprodajem.com/")

	resp, err := g.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("скачивание фото: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("скачивание фото: HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 15<<20))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("пустое изображение")
	}

	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = "image/webp"
	}
	// отрезаем параметры вида "image/webp; charset=..."
	if i := indexByte(mime, ';'); i >= 0 {
		mime = mime[:i]
	}

	return &Part{InlineData: &InlineData{
		MIMEType: mime,
		Data:     base64.StdEncoding.EncodeToString(data),
	}}, nil
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
