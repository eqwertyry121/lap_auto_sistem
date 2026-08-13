package control

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kpbot/notifier"
)

func TestPanelPauseResumeUsePausedFlag(t *testing.T) {
	var paused atomic.Bool
	p := &Panel{paused: &paused}

	if p.isPaused() {
		t.Fatal("new panel state is paused, want running")
	}
	if !p.pause() {
		t.Fatal("pause returned false, want true")
	}
	if !paused.Load() || !p.isPaused() {
		t.Fatal("pause did not set paused flag")
	}
	if p.pause() {
		t.Fatal("second pause returned true, want false")
	}
	if !p.resume() {
		t.Fatal("resume returned false, want true")
	}
	if paused.Load() || p.isPaused() {
		t.Fatal("resume did not clear paused flag")
	}
	if p.resume() {
		t.Fatal("second resume returned true, want false")
	}
}

func TestPanelStatusToleratesMissingMarketProvider(t *testing.T) {
	var paused atomic.Bool
	p := &Panel{
		tg:      notifier.New("", ""),
		root:    t.TempDir(),
		paused:  &paused,
		started: time.Now(),
	}

	p.status(context.Background())
}

func TestChallengeRatePerHour(t *testing.T) {
	if got := challengeRatePerHour(6, 2*time.Hour); got != 3 {
		t.Fatalf("challengeRatePerHour = %.2f, want 3.00", got)
	}
	if got := challengeRatePerHour(1, 0); got != 0 {
		t.Fatalf("zero uptime challengeRatePerHour = %.2f, want 0", got)
	}
	if got := challengeRatePerHour(0, time.Hour); got != 0 {
		t.Fatalf("zero challenges challengeRatePerHour = %.2f, want 0", got)
	}
}

func TestTelegramCtlResponseError(t *testing.T) {
	if err := telegramCtlResponseError("sendMessage", 200, []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("ok response returned error: %v", err)
	}

	err := telegramCtlResponseError("sendMessage", 200, []byte(`{"ok":false,"description":"chat not found"}`))
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("ok=false error = %v, want description", err)
	}

	err = telegramCtlResponseError("sendMessage", 502, []byte(`bad gateway`))
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("bad body error = %v, want HTTP status", err)
	}
}
