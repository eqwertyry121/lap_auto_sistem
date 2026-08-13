package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireSingletonRejectsFreshLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "kpbot.lock")
	heartbeatPath := filepath.Join(dir, "kpbot.heartbeat")

	release, err := acquireSingleton(lockPath, heartbeatPath)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	secondRelease, err := acquireSingleton(lockPath, heartbeatPath)
	if err == nil {
		secondRelease()
		t.Fatal("second acquire succeeded with fresh lock")
	}
	if !os.IsExist(err) {
		t.Fatalf("second acquire error = %v, want exists", err)
	}
}

func TestAcquireSingletonBreaksStaleLockWithoutFreshHeartbeat(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "kpbot.lock")
	heartbeatPath := filepath.Join(dir, "kpbot.heartbeat")
	writeOldFile(t, lockPath, singletonLockStaleAfter+time.Minute)

	release, err := acquireSingleton(lockPath, heartbeatPath)
	if err != nil {
		t.Fatalf("acquire stale lock: %v", err)
	}
	defer release()

	body, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if string(body) == "old\n" {
		t.Fatal("stale lock content was not replaced")
	}
}

func TestAcquireSingletonKeepsStaleLockWithFreshHeartbeat(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "kpbot.lock")
	heartbeatPath := filepath.Join(dir, "kpbot.heartbeat")
	writeOldFile(t, lockPath, singletonLockStaleAfter+time.Minute)
	if err := os.WriteFile(heartbeatPath, []byte("fresh\n"), 0o644); err != nil {
		t.Fatalf("write heartbeat: %v", err)
	}

	release, err := acquireSingleton(lockPath, heartbeatPath)
	if err == nil {
		release()
		t.Fatal("acquire succeeded despite fresh heartbeat")
	}
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("acquire error = %v, want exists", err)
	}
}

func TestReleaseSingletonRemovesLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "kpbot.lock")
	heartbeatPath := filepath.Join(dir, "kpbot.heartbeat")

	release, err := acquireSingleton(lockPath, heartbeatPath)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	release()

	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock stat after release = %v, want not exist", err)
	}
}

func writeOldFile(t *testing.T, path string, age time.Duration) {
	t.Helper()
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write old file: %v", err)
	}
	old := time.Now().Add(-age)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("age old file: %v", err)
	}
}
