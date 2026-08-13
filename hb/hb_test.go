package hb

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitForFile(t *testing.T, path string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("heartbeat-файл %s не появился за %s", path, d)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHeartbeatWritesAndUpdatesState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.heartbeat")
	h := New(path, "starting")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.Run(ctx, 10*time.Millisecond)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Run did not stop after context cancellation")
		}
	})

	waitForFile(t, path, 2*time.Second)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "starting") {
		t.Fatalf("в файле нет начального состояния: %q", data)
	}

	h.SetState("challenge_pause")
	// Окно с запасом: параллельные тесты могут голодать по CPU, а запись
	// heartbeat — по 10-мс тикеру.
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, _ = os.ReadFile(path)
		if strings.Contains(string(data), "challenge_pause") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("состояние не обновилось: %q", data)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHeartbeatStopsOnCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.heartbeat")
	h := New(path, "starting")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.Run(ctx, 5*time.Millisecond)
		close(done)
	}()

	waitForFile(t, path, 2*time.Second)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run не завершился после отмены ctx")
	}
}
