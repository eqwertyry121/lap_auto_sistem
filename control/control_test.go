package control

import (
	"context"
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
