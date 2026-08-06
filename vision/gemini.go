package vision

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// GeminiClient — минимальный REST-клиент Gemini (generateContent), без SDK.
type GeminiClient struct {
	apiKey string
	model  string
	http   *http.Client
}

func NewGeminiClient(apiKey, model string) *GeminiClient {
	if model == "" {
		model = "gemini-flash-latest"
	}
	return &GeminiClient{
		apiKey: apiKey,
		model:  model,
		http:   &http.Client{Timeout: 90 * time.Second},
	}
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
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("gemini: marshal: %w", err)
	}

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", g.model)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", g.apiKey)

	resp, err := g.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini: запрос: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("gemini: чтение ответа: %w", err)
	}

	var gr response
	if err := json.Unmarshal(raw, &gr); err != nil {
		return nil, fmt.Errorf("gemini: decode (HTTP %d): %w", resp.StatusCode, err)
	}
	if gr.Error != nil {
		return nil, fmt.Errorf("gemini: API error %d: %s", gr.Error.Code, gr.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini: HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	return &gr, nil
}

// textOf собирает текст ответа из parts кандидата.
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
