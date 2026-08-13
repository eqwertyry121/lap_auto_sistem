package runlock

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	defaultRefreshInterval = time.Minute
	defaultLockStaleAfter  = 10 * time.Minute
	defaultHeartbeatStale  = 3 * time.Minute
)

type Options struct {
	HeartbeatPath       string
	RefreshInterval     time.Duration
	LockStaleAfter      time.Duration
	HeartbeatStaleAfter time.Duration
}

func Acquire(path string, opts Options) (func(), error) {
	opts = opts.withDefaults()
	release, err := create(path, opts.RefreshInterval)
	if err == nil {
		return release, nil
	}
	if !os.IsExist(err) {
		return nil, err
	}
	stale, staleErr := canBreak(path, opts.HeartbeatPath, time.Now(), opts.LockStaleAfter, opts.HeartbeatStaleAfter)
	if staleErr != nil {
		return nil, staleErr
	}
	if !stale {
		return nil, err
	}
	if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
		return nil, removeErr
	}
	return create(path, opts.RefreshInterval)
}

func (o Options) withDefaults() Options {
	if o.RefreshInterval <= 0 {
		o.RefreshInterval = defaultRefreshInterval
	}
	if o.LockStaleAfter <= 0 {
		o.LockStaleAfter = defaultLockStaleAfter
	}
	if o.HeartbeatStaleAfter <= 0 {
		o.HeartbeatStaleAfter = defaultHeartbeatStale
	}
	return o
}

func create(path string, refreshInterval time.Duration) (func(), error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(f, "%d\n%s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(refreshInterval)
		defer t.Stop()
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				_ = touch(path)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			<-done
			_ = os.Remove(path)
		})
	}, nil
}

func canBreak(lockPath, heartbeatPath string, now time.Time, lockStaleAfter, heartbeatStaleAfter time.Duration) (bool, error) {
	lockInfo, err := os.Stat(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	if now.Sub(lockInfo.ModTime()) < lockStaleAfter {
		return false, nil
	}
	if heartbeatPath == "" {
		return true, nil
	}
	heartbeatInfo, err := os.Stat(heartbeatPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	return now.Sub(heartbeatInfo.ModTime()) >= heartbeatStaleAfter, nil
}

func touch(path string) error {
	now := time.Now()
	return os.Chtimes(path, now, now)
}
