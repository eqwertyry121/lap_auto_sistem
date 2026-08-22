package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Telegram — тонкий клиент Bot API. Надёжность алертов обеспечивает
// DB outbox в storage: этот пакет только отправляет конкретное сообщение.
type Telegram struct {
	token   string
	chatID  string
	http    *http.Client
	baseURL string
}

func New(token, chatID string) *Telegram {
	return &Telegram{
		token:   token,
		chatID:  chatID,
		http:    &http.Client{Timeout: 20 * time.Second},
		baseURL: "https://api.telegram.org",
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
		return fmt.Errorf("telegram: %w", sanitizedTelegramError(err))
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("telegram: %w", sanitizedTelegramError(err))
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

// ---------- публичные отправки ----------

// SendRaw — отправка произвольного HTML-текста (для smoke-тестов/сервисных сообщений).
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
		return 0, fmt.Errorf("telegram: %w", sanitizedTelegramError(err))
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := t.http.Do(httpReq)
	if err != nil {
		return 0, fmt.Errorf("telegram: %w", sanitizedTelegramError(err))
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

func sanitizedTelegramError(err error) error {
	if uerr, ok := err.(*url.Error); ok {
		return uerr.Err
	}
	return err
}
