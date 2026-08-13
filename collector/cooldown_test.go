package collector

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSharedCooldownErrMatchesReason(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kp_cooldown")
	t.Setenv("KP_COOLDOWN_PATH", path)

	writeSharedCooldown("challenge", time.Hour)
	err := sharedCooldownErr()
	if !errors.Is(err, ErrChallenge) {
		t.Fatalf("challenge cooldown err = %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeSharedCooldown("rate", time.Hour)
	err = sharedCooldownErr()
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("rate cooldown err = %v", err)
	}
}

func TestSharedCooldownExpiredIsRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kp_cooldown")
	t.Setenv("KP_COOLDOWN_PATH", path)
	oldUntil := time.Now().Add(-time.Minute).Unix()
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d rate\n", oldUntil)), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := sharedCooldownErr(); err != nil {
		t.Fatalf("expired cooldown must not block: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expired cooldown file must be removed, stat err=%v", err)
	}
}

func TestSharedCooldownKeepsLongerUntil(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kp_cooldown")
	t.Setenv("KP_COOLDOWN_PATH", path)

	writeSharedCooldown("challenge", time.Hour)
	before, reason, ok := readSharedCooldown(path)
	if !ok {
		t.Fatal("cooldown not written")
	}
	writeSharedCooldown("rate", time.Minute)
	after, afterReason, ok := readSharedCooldown(path)
	if !ok {
		t.Fatal("cooldown disappeared")
	}
	if !after.Equal(before) || afterReason != reason {
		t.Fatalf("shorter cooldown overwrote longer one: before=%s/%s after=%s/%s", before, reason, after, afterReason)
	}
}

func TestClientSharedCooldownUsesConfiguredPath(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "env_cooldown")
	configPath := filepath.Join(dir, "configured_cooldown")
	t.Setenv("KP_COOLDOWN_PATH", envPath)

	client := NewClient(WithSharedCooldown(configPath, 2*time.Second, 3*time.Minute))
	client.recordSharedCooldown(ErrRateLimited)

	until, reason, ok := readSharedCooldown(configPath)
	if !ok {
		t.Fatal("configured cooldown file was not written")
	}
	if reason != "rate" {
		t.Fatalf("reason = %q, want rate", reason)
	}
	left := time.Until(until)
	if left < time.Second || left > 3*time.Second {
		t.Fatalf("rate cooldown duration = %s, want about 2s", left)
	}
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatalf("env cooldown path must not be used, stat err=%v", err)
	}
}
