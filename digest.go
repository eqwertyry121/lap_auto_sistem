// digest — дневной отчёт о состоянии системы в Telegram (PLAN_v4, Фаза 0).
// Молчание системы = инцидент: нет дайджеста — значит бот мёртв или молчит.
package main

import (
	"fmt"
	"strings"
	"time"

	"kpbot/models"
)

// parseDigestTime разбирает «HH:MM» в часы и минуты.
func parseDigestTime(s string) (int, int, error) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return 0, 0, fmt.Errorf("неверный DIGEST_AT %q (нужно HH:MM)", s)
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("DIGEST_AT %q вне диапазона", s)
	}
	return h, m, nil
}

// nextDigestTime — ближайший будущий момент HH:MM (сегодня или завтра).
func nextDigestTime(now time.Time, h, m int) time.Time {
	target := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if !target.After(now) {
		target = target.AddDate(0, 0, 1)
	}
	return target
}

type runtimeHealth struct {
	Now                time.Time
	LastSearchOK       time.Time
	LastDetailOK       time.Time
	LastGeminiOK       time.Time
	LastTelegramOK     time.Time
	LastBackupOK       time.Time
	GeminiCallsToday   int
	GeminiDailyLimit   int
	GeminiCircuitUntil time.Time
}

// digestText собирает HTML-текст дайджеста. challenges — счётчик с момента
// запуска (за 24ч не обнуляется — честнее писать как есть).
func digestText(uptime time.Duration, byStatus map[string]int, challenges int64,
	total, detailed int, alerts []models.Listing, processStates map[string]int, pendingOutbox int,
	health runtimeHealth) string {
	var b strings.Builder
	b.WriteString("📊 <b>Дайджест KP-бота</b>\n\n")
	fmt.Fprintf(&b, "<b>Аптайм:</b> %s\n", uptime.Round(time.Minute))

	total24 := 0
	for _, n := range byStatus {
		total24 += n
	}
	fmt.Fprintf(&b, "<b>Лоты за 24ч:</b> %d\n", total24)
	for _, st := range []string{"ALERTED", "NEED_CHECK", "NO_DEAL", "SKIPPED_SPAM", "SKIPPED_BAN", "NEW", "ERROR"} {
		if n := byStatus[st]; n > 0 {
			fmt.Fprintf(&b, "  · %s: %d\n", st, n)
		}
	}

	fmt.Fprintf(&b, "<b>Челленджи KP (с запуска):</b> %d\n", challenges)

	if total > 0 {
		fmt.Fprintf(&b, "<b>Покрытие рынка:</b> %d/%d деталей (%.1f%%)\n",
			detailed, total, 100*float64(detailed)/float64(total))
	}

	now := health.Now
	if now.IsZero() {
		now = time.Now()
	}
	b.WriteString("<b>Health:</b>\n")
	fmt.Fprintf(&b, "  - search_ok: %s\n", healthAge(now, health.LastSearchOK))
	fmt.Fprintf(&b, "  - detail_ok: %s\n", healthAge(now, health.LastDetailOK))
	fmt.Fprintf(&b, "  - gemini_ok: %s\n", healthAge(now, health.LastGeminiOK))
	fmt.Fprintf(&b, "  - gemini_calls: %s\n", geminiCallsLine(health.GeminiCallsToday, health.GeminiDailyLimit))
	if health.GeminiCircuitUntil.After(now) {
		fmt.Fprintf(&b, "  - gemini_circuit: %s\n", healthRemaining(now, health.GeminiCircuitUntil))
	}
	fmt.Fprintf(&b, "  - telegram_ok: %s\n", healthAge(now, health.LastTelegramOK))
	fmt.Fprintf(&b, "  - backup_ok: %s\n", healthAge(now, health.LastBackupOK))

	if pendingOutbox > 0 || hasQueueBacklog(processStates) {
		b.WriteString("<b>Очереди:</b>\n")
		for _, st := range []string{"DETAIL_PENDING", "EVALUATING", "ALERT_PENDING", "DEAD"} {
			if n := processStates[st]; n > 0 {
				fmt.Fprintf(&b, "  · %s: %d\n", st, n)
			}
		}
		if pendingOutbox > 0 {
			fmt.Fprintf(&b, "  · telegram_outbox: %d\n", pendingOutbox)
		}
	}

	if len(alerts) > 0 {
		b.WriteString("<b>Последние алерты:</b>\n")
		for _, a := range alerts {
			fmt.Fprintf(&b, "  · %s — %s\n",
				models.FormatPrice(a.Price, a.Currency), truncateLine(a.Title, 60))
		}
	}
	return b.String()
}

func hasQueueBacklog(processStates map[string]int) bool {
	for _, st := range []string{"DETAIL_PENDING", "EVALUATING", "ALERT_PENDING", "DEAD"} {
		if processStates[st] > 0 {
			return true
		}
	}
	return false
}

func healthAge(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	if t.After(now) {
		return "0s"
	}
	return now.Sub(t).Round(time.Minute).String()
}

func healthRemaining(now, until time.Time) string {
	if !until.After(now) {
		return "inactive"
	}
	return until.Sub(now).Round(time.Minute).String()
}

func geminiCallsLine(callsToday, dailyLimit int) string {
	if dailyLimit > 0 {
		return fmt.Sprintf("%d/%d", callsToday, dailyLimit)
	}
	return fmt.Sprintf("%d/unlimited", callsToday)
}

func truncateLine(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
