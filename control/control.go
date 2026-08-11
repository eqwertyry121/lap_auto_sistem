// Package control — пульт управления ботом из Telegram: три кнопки —
// Старт, Стоп, Статус. Стоп приостанавливает поллинг и воронку (процесс
// остаётся жив, пульт продолжает отвечать), Старт возобновляет работу.
// Команды принимаются только из чата TELEGRAM_CHAT_ID.
package control

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"kpbot/notifier"
	"kpbot/pricing"
)

// MarketProvider — доступ к рыночной модели воронки (для статуса).
type MarketProvider interface {
	MarketOnly() *pricing.Market
}

type RuntimeHealthProvider interface {
	LastSearchOK() time.Time
	LastDetailOK() time.Time
	LastGeminiOK() time.Time
	LastTelegramOK() time.Time
	LastBackupOK() time.Time
	GeminiCallsToday() int
	GeminiDailyLimit() int
	GeminiCircuitUntil() time.Time
	SchemaVersion() int
	BuildVersion() string
}

// Panel — состояние пульта.
type Panel struct {
	tg      *notifier.Telegram
	token   string
	chatID  int64
	root    string // корень проекта (heartbeat/watchdog в data/)
	running *atomic.Bool
	started time.Time
	chCount *atomic.Int64
	market  MarketProvider
	health  RuntimeHealthProvider
}

// New создаёт пульт. chatID не распознан → пульт отключён (nil).
func New(tg *notifier.Telegram, token, chatIDStr, root string, running *atomic.Bool,
	started time.Time, chCount *atomic.Int64, market MarketProvider, health RuntimeHealthProvider) *Panel {
	chatID, err := strconv.ParseInt(strings.TrimSpace(chatIDStr), 10, 64)
	if err != nil || chatID == 0 {
		return nil
	}
	return &Panel{
		tg: tg, token: token, chatID: chatID, root: root,
		running: running, started: started, chCount: chCount, market: market, health: health,
	}
}

// ---------- long-polling ----------

type tgUpdate struct {
	UpdateID      int64       `json:"update_id"`
	Message       *tgMessage  `json:"message"`
	CallbackQuery *tgCallback `json:"callback_query"`
}

type tgMessage struct {
	Chat struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	Text string `json:"text"`
}

type tgCallback struct {
	ID      string `json:"id"`
	Data    string `json:"data"`
	Message struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
}

// Run — цикл пульта до отмены ctx.
func (p *Panel) Run(ctx context.Context, log *slog.Logger) {
	log.Info("пульт управления включён", "chat_id", p.chatID)
	client := &http.Client{Timeout: 60 * time.Second}
	var offset int64
	for ctx.Err() == nil {
		updates, next, err := p.fetchUpdates(ctx, client, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn("пульт: getUpdates", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(15 * time.Second):
			}
			continue
		}
		offset = next
		for i := range updates {
			p.handle(ctx, &updates[i], log)
		}
	}
}

func (p *Panel) fetchUpdates(ctx context.Context, client *http.Client, offset int64) ([]tgUpdate, int64, error) {
	q := url.Values{}
	q.Set("timeout", "30")
	q.Set("allowed_updates", `["message","callback_query"]`)
	if offset > 0 {
		q.Set("offset", strconv.FormatInt(offset, 10))
	}
	u := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?%s", p.token, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, offset, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, offset, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, offset, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateCtl(string(raw), 160))
	}
	var envelope struct {
		OK     bool       `json:"ok"`
		Result []tgUpdate `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, offset, err
	}
	next := offset
	for _, u := range envelope.Result {
		if u.UpdateID >= next {
			next = u.UpdateID + 1
		}
	}
	return envelope.Result, next, nil
}

func (p *Panel) answerCallback(cbID string) {
	u := fmt.Sprintf("https://api.telegram.org/bot%s/answerCallbackQuery?callback_query_id=%s",
		p.token, url.QueryEscape(cbID))
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(u)
	if err == nil {
		resp.Body.Close()
	}
}

// ---------- диспетчер ----------

func (p *Panel) handle(ctx context.Context, u *tgUpdate, log *slog.Logger) {
	switch {
	case u.CallbackQuery != nil:
		cb := u.CallbackQuery
		p.answerCallback(cb.ID)
		if cb.Message.Chat.ID != p.chatID {
			return
		}
		p.dispatch(ctx, cb.Data, log)
	case u.Message != nil:
		if u.Message.Chat.ID != p.chatID {
			return
		}
		p.onText(ctx, strings.TrimSpace(u.Message.Text))
	}
}

func (p *Panel) onText(ctx context.Context, text string) {
	switch text {
	case "/start", "/panel", "/help", "":
		p.sendPanel(ctx)
	default:
		p.send(ctx, "Нажмите /panel — откроется пульт с кнопками.")
	}
}

// ---------- кнопки ----------

const panelCaption = `🎛 ПУЛЬТ — KP Laptop Bot

▶️ Старт — возобновить поллинг и воронку: бот снова ищет лоты и шлёт алерты
⏹ Стоп — приостановить поллинг: бот молчит, но пульт продолжает отвечать
🩺 Статус — режим, аптайм, heartbeat, челленджи, рыночная модель`

func (p *Panel) sendPanel(ctx context.Context) {
	keyboard := map[string]any{
		"inline_keyboard": [][]map[string]string{
			{
				{"text": "▶️ Старт", "callback_data": "start"},
				{"text": "⏹ Стоп", "callback_data": "stop"},
			},
			{{"text": "🩺 Статус", "callback_data": "status"}},
		},
	}
	p.sendWithKeyboard(ctx, panelCaption, keyboard)
}

func (p *Panel) dispatch(ctx context.Context, data string, log *slog.Logger) {
	switch data {
	case "panel", "help":
		p.sendPanel(ctx)
	case "start":
		if !p.running.Load() {
			p.send(ctx, "▶️ Бот уже работает.")
			return
		}
		p.running.Store(false)
		log.Info("пульт: СТАРТ — поллинг возобновлён")
		p.send(ctx, "▶️ Запущено: поллинг и воронка снова в работе. Алерты возобновятся со следующего цикла.")
	case "stop":
		if p.running.Load() {
			p.send(ctx, "⏹ Бот уже остановлен.")
			return
		}
		p.running.Store(true)
		log.Warn("пульт: СТОП — поллинг приостановлен")
		p.send(ctx, "⏹ Остановлено: поллинг и алерты на паузе. Пульт отвечает, watchdog следит. Нажмите ▶️ Старт для возобновления.")
	case "status":
		p.status(ctx)
	default:
		p.send(ctx, "Неизвестная команда: "+data)
	}
}

// ---------- статус ----------

func (p *Panel) status(ctx context.Context) {
	var b strings.Builder
	mode := "▶️ работает"
	if p.running.Load() {
		mode = "⏹ остановлен пультом"
	}
	fmt.Fprintf(&b, "🩺 СТАТУС\n\nРежим: %s\nАптайм: %s\n", mode, time.Since(p.started).Round(time.Minute))
	if p.chCount != nil {
		fmt.Fprintf(&b, "Челленджей KP с запуска: %d\n", p.chCount.Load())
	}
	if p.health != nil {
		now := time.Now()
		fmt.Fprintf(&b, "\nHealth:\n")
		fmt.Fprintf(&b, "build: %s\n", nonEmptyCtl(p.health.BuildVersion(), "dev"))
		fmt.Fprintf(&b, "schema_version: %d\n", p.health.SchemaVersion())
		fmt.Fprintf(&b, "search_ok: %s\n", runtimeAge(now, p.health.LastSearchOK()))
		fmt.Fprintf(&b, "detail_ok: %s\n", runtimeAge(now, p.health.LastDetailOK()))
		fmt.Fprintf(&b, "gemini_ok: %s\n", runtimeAge(now, p.health.LastGeminiOK()))
		fmt.Fprintf(&b, "gemini_calls: %s\n", geminiCallsStatus(p.health.GeminiCallsToday(), p.health.GeminiDailyLimit()))
		if until := p.health.GeminiCircuitUntil(); until.After(now) {
			fmt.Fprintf(&b, "gemini_circuit: %s\n", runtimeRemaining(now, until))
		}
		fmt.Fprintf(&b, "telegram_ok: %s\n", runtimeAge(now, p.health.LastTelegramOK()))
		fmt.Fprintf(&b, "backup_ok: %s\n", runtimeAge(now, p.health.LastBackupOK()))
	}
	for _, name := range []string{"kpbot", "research"} {
		path := filepath.Join(p.root, "data", name+".heartbeat")
		if data, err := os.ReadFile(path); err == nil {
			age := time.Since(fileMTime(path)).Round(time.Second)
			fmt.Fprintf(&b, "heartbeat %s: %s (возраст %s)\n", name, strings.TrimSpace(string(data)), age)
		} else {
			fmt.Fprintf(&b, "heartbeat %s: файла нет\n", name)
		}
	}
	if tail, err := tailLines(filepath.Join(p.root, "data", "watchdog.log"), 3); err == nil && tail != "" {
		fmt.Fprintf(&b, "\nwatchdog (последние записи):\n%s\n", tail)
	}
	if m := p.market.MarketOnly(); m != nil {
		fmt.Fprintf(&b, "\nРыночная модель: лотов %d, пул %d, курс %.2f (%s)\n",
			len(m.Lots()), len(m.Pool()), m.RsdEurRate(), m.RateSource())
	} else {
		b.WriteString("\nРыночная модель ещё не загружена.\n")
	}
	p.send(ctx, strings.TrimRight(b.String(), "\n"))
}

// ---------- отправка ----------

func (p *Panel) send(ctx context.Context, text string) {
	for _, chunk := range splitChunks(text, 4000) {
		if err := p.tg.SendRaw(ctx, chunk); err != nil {
			slog.Error("пульт: отправка", "err", err)
		}
	}
}

func (p *Panel) sendWithKeyboard(ctx context.Context, text string, keyboard map[string]any) {
	payload := map[string]any{
		"chat_id":      strconv.FormatInt(p.chatID, 10),
		"text":         text,
		"reply_markup": keyboard,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	u := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", p.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(body)))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	c := &http.Client{Timeout: 20 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		slog.Error("пульт: панель", "err", err)
		return
	}
	resp.Body.Close()
}

// ---------- утилиты ----------

func splitChunks(text string, limit int) []string {
	if len(text) <= limit {
		return []string{text}
	}
	var out []string
	lines := strings.Split(text, "\n")
	var cur strings.Builder
	for _, ln := range lines {
		if cur.Len()+len(ln)+1 > limit && cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
		cur.WriteString(ln)
		cur.WriteString("\n")
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func truncateCtl(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func runtimeAge(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	if t.After(now) {
		return "0s"
	}
	return now.Sub(t).Round(time.Minute).String()
}

func runtimeRemaining(now, until time.Time) string {
	if !until.After(now) {
		return "inactive"
	}
	return until.Sub(now).Round(time.Minute).String()
}

func geminiCallsStatus(callsToday, dailyLimit int) string {
	if dailyLimit > 0 {
		return fmt.Sprintf("%d/%d", callsToday, dailyLimit)
	}
	return fmt.Sprintf("%d/unlimited", callsToday)
}

func nonEmptyCtl(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func fileMTime(path string) time.Time {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

func tailLines(path string, n int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}
