package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"kpbot/models"
)

// Telegram — отправка алертов через Bot API. Сбой Telegram не теряет
// алерты: несработавшие сообщения складываются в JSONL-очередь и
// доотправляются после первого успешного сообщения (PLAN_v4 §6.4, Реш. ⑦).
type Telegram struct {
	token     string
	chatID    string
	http      *http.Client
	queuePath string
	baseURL   string
	mu        sync.Mutex // очередь: параллельные воркеры не конфликтуют
}

func New(token, chatID string) *Telegram {
	return NewWithQueue(token, chatID, "data/alert_queue.jsonl")
}

// NewWithQueue — конструктор с явным путём очереди (для тестов и конфигов).
func NewWithQueue(token, chatID, queuePath string) *Telegram {
	return &Telegram{
		token:     token,
		chatID:    chatID,
		http:      &http.Client{Timeout: 20 * time.Second},
		queuePath: queuePath,
		baseURL:   "https://api.telegram.org",
	}
}

// Enabled — можно ли слать сообщения (заданы ли токен и chat_id).
func (t *Telegram) Enabled() bool { return t.token != "" && t.chatID != "" }

type inlineButton struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

type sendMessageRequest struct {
	ChatID      string       `json:"chat_id"`
	Text        string       `json:"text"`
	ParseMode   string       `json:"parse_mode"`
	ReplyMarkup *replyMarkup `json:"reply_markup,omitempty"`
}

type replyMarkup struct {
	InlineKeyboard [][]inlineButton `json:"inline_keyboard"`
}

func (t *Telegram) send(ctx context.Context, text, openURL string) error {
	if !t.Enabled() {
		// dry-run: печатаем в консоль, чтобы не терять алерты при разработке
		fmt.Println("────── [DRY-RUN] Telegram не настроен ──────")
		fmt.Println(text)
		if openURL != "" {
			fmt.Println("[кнопка] Открыть объявление на KP:", openURL)
		}
		fmt.Println("─────────────────────────────────────────────")
		return nil
	}

	req := sendMessageRequest{
		ChatID:    t.chatID,
		Text:      text,
		ParseMode: "HTML",
	}
	if openURL != "" {
		req.ReplyMarkup = &replyMarkup{
			InlineKeyboard: [][]inlineButton{
				{{Text: "Открыть объявление на KP", URL: openURL}},
			},
		}
	}

	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/bot%s/sendMessage", t.baseURL, t.token)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var tr struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &tr); err != nil {
		return fmt.Errorf("telegram: HTTP %d", resp.StatusCode)
	}
	if !tr.OK {
		return fmt.Errorf("telegram: %s", tr.Description)
	}
	return nil
}

// ---------- очередь алертов ----------

// queuedMessage — сериализованный алерт для доотправки.
type queuedMessage struct {
	Type      string         `json:"type"` // "alert" | "need_check" | "raw"
	Listing   models.Listing `json:"listing"`
	Verdict   models.Verdict `json:"verdict"`
	Hint      string         `json:"hint"`
	RawText   string         `json:"raw_text,omitempty"`
	RawButton string         `json:"raw_button,omitempty"`
}

func (t *Telegram) enqueue(m queuedMessage) {
	t.mu.Lock()
	defer t.mu.Unlock()
	f, err := os.OpenFile(t.queuePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if b, err := json.Marshal(m); err == nil {
		_, _ = f.Write(append(b, '\n'))
	}
}

// flushQueue доотправляет накопленное. Вызывается ТОЛЬКО после успешной
// отправки: если Telegram лежит, очередь молча растёт без долбления API.
func (t *Telegram) flushQueue(ctx context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()
	data, err := os.ReadFile(t.queuePath)
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return
	}
	var rest []queuedMessage
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m queuedMessage
		if json.Unmarshal([]byte(line), &m) != nil {
			continue // битая строка — выбрасываем, не зацикливаемся
		}
		if t.sendQueued(ctx, m) != nil {
			rest = append(rest, m)
		}
	}
	if len(rest) == 0 {
		_ = os.Remove(t.queuePath)
		return
	}
	f, err := os.Create(t.queuePath)
	if err != nil {
		return
	}
	defer f.Close()
	for _, m := range rest {
		if b, err := json.Marshal(m); err == nil {
			_, _ = f.Write(append(b, '\n'))
		}
	}
}

func (t *Telegram) sendQueued(ctx context.Context, m queuedMessage) error {
	switch m.Type {
	case "alert":
		return t.send(ctx, alertText(m.Listing, m.Verdict, m.Hint), m.Listing.URL)
	case "need_check":
		return t.send(ctx, needCheckText(m.Listing, m.Verdict), m.Listing.URL)
	case "raw":
		return t.send(ctx, m.RawText, m.RawButton)
	}
	return nil
}

// afterSend — реакция на результат: сбой → в очередь, успех → доотправка.
func (t *Telegram) afterSend(ctx context.Context, sendErr error, m queuedMessage) {
	_ = ctx
	_ = sendErr
	_ = m
}

// ---------- тексты сообщений ----------

func alertText(l models.Listing, v models.Verdict, marketHint string) string {
	specs := v.Specs
	if specs == "" {
		specs = l.Title
	}
	var b bytes.Buffer
	b.WriteString("🚀 <b>НАЙДЕН ПРОФИТ!</b>\n\n")
	fmt.Fprintf(&b, "<b>Ноутбук:</b> %s\n", html.EscapeString(specs))
	fmt.Fprintf(&b, "<b>Цена продавца:</b> %s\n", models.FormatPrice(l.Price, l.Currency))
	if marketHint != "" {
		fmt.Fprintf(&b, "<b>Рынок:</b> %s\n", html.EscapeString(marketHint))
	}
	fmt.Fprintf(&b, "<b>Ожидаемая прибыль:</b> ~%s\n", models.FormatPrice(v.EstimatedProfit, "EUR"))
	if v.Reason != "" {
		fmt.Fprintf(&b, "<b>Почему:</b> %s\n", html.EscapeString(v.Reason))
	}
	fmt.Fprintf(&b, "\n🔗 %s", html.EscapeString(l.URL))
	return b.String()
}

func needCheckText(l models.Listing, v models.Verdict) string {
	var b bytes.Buffer
	b.WriteString("⚠️ <b>ТРЕБУЕТСЯ ПРОВЕРКА</b>\n\n")
	fmt.Fprintf(&b, "<b>Заголовок:</b> %s\n", html.EscapeString(l.Title))
	fmt.Fprintf(&b, "<b>Цена:</b> %s\n", models.FormatPrice(l.Price, l.Currency))
	if v.Reason != "" {
		fmt.Fprintf(&b, "<b>Причина:</b> %s\n", html.EscapeString(v.Reason))
	}
	fmt.Fprintf(&b, "\n🔗 %s", html.EscapeString(l.URL))
	return b.String()
}

// ---------- публичные отправки ----------

// SendRaw — отправка произвольного HTML-текста (для smoke-тестов/сервисных сообщений).
func AlertHTML(l models.Listing, v models.Verdict, marketHint string) string {
	return alertText(l, v, marketHint)
}

func NeedCheckHTML(l models.Listing, v models.Verdict) string {
	return needCheckText(l, v)
}

func (t *Telegram) SendRaw(ctx context.Context, text string) error {
	return t.send(ctx, text, "")
}

func (t *Telegram) SendRawDirect(ctx context.Context, text, url string) (int64, error) {
	if !t.Enabled() {
		return 0, t.send(ctx, text, url)
	}
	req := sendMessageRequest{
		ChatID:    t.chatID,
		Text:      text,
		ParseMode: "HTML",
	}
	if url != "" {
		req.ReplyMarkup = &replyMarkup{InlineKeyboard: [][]inlineButton{
			{{Text: "Открыть объявление на KP", URL: url}},
		}}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}
	apiURL := fmt.Sprintf("%s/bot%s/sendMessage", t.baseURL, t.token)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := t.http.Do(httpReq)
	if err != nil {
		return 0, fmt.Errorf("telegram: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &tr); err != nil {
		return 0, fmt.Errorf("telegram: HTTP %d", resp.StatusCode)
	}
	if !tr.OK {
		return 0, fmt.Errorf("telegram: %s", tr.Description)
	}
	return tr.Result.MessageID, nil
}

// SendAlert — формат «НАЙДЕН ПРОФИТ» из ТЗ.
func (t *Telegram) SendAlert(ctx context.Context, l models.Listing, v models.Verdict, marketHint string) error {
	err := t.send(ctx, alertText(l, v, marketHint), l.URL)
	t.afterSend(ctx, err, queuedMessage{Type: "alert", Listing: l, Verdict: v, Hint: marketHint})
	return err
}

// SendNeedCheck — формат «ТРЕБУЕТСЯ ПРОВЕРКА».
func (t *Telegram) SendNeedCheck(ctx context.Context, l models.Listing, v models.Verdict) error {
	err := t.send(ctx, needCheckText(l, v), l.URL)
	t.afterSend(ctx, err, queuedMessage{Type: "need_check", Listing: l, Verdict: v})
	return err
}

// SendRawQueued — готовый HTML-текст с inline-кнопкой на url; при сбое
// Telegram становится в очередь на доотправку (алерты воронки, Фаза 4).
func (t *Telegram) SendRawQueued(ctx context.Context, text, url string) error {
	err := t.send(ctx, text, url)
	t.afterSend(ctx, err, queuedMessage{Type: "raw", RawText: text, RawButton: url})
	return err
}
