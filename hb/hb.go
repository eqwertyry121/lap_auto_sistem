// Package hb — heartbeat-файлы процессов: единственный критерий живости
// для watchdog (PLAN_v4, Фаза 0). Наличие процесса в списке ОС ничего не
// доказывает (дедлок выглядит «живым»); свежий heartbeat-файл — доказывает.
package hb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// Heartbeat — периодическая запись файла живости.
type Heartbeat struct {
	path  string
	state atomic.Value // string
}

// New создаёт heartbeat, пишущий в path. state — начальное состояние.
func New(path, state string) *Heartbeat {
	h := &Heartbeat{path: path}
	h.state.Store(state)
	return h
}

// SetState обновляет текущее состояние (polling / challenge_pause / …).
func (h *Heartbeat) SetState(state string) { h.state.Store(state) }

// State — текущее состояние.
func (h *Heartbeat) State() string { return h.state.Load().(string) }

// Run пишет файл сразу и далее раз в interval, пока ctx не отменён.
// Запускать в отдельной горутине: она живёт независимо от основного цикла,
// поэтому heartbeat остаётся свежим даже во время длинных пауз (антибот).
func (h *Heartbeat) Run(ctx context.Context, interval time.Duration) {
	h.write()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.write()
		}
	}
}

func (h *Heartbeat) write() {
	if dir := filepath.Dir(h.path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	// Формат: unix-время + состояние. Watchdog проверяет свежесть по mtime
	// файла, содержимое — диагностика для человека.
	content := fmt.Sprintf("%d %s\n", time.Now().Unix(), h.State())
	_ = os.WriteFile(h.path, []byte(content), 0o644)
}
