package config

import (
	"testing"
	"time"
)

func TestQueueConfig_DefaultRetryTiers(t *testing.T) {
	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	want := []time.Duration{5 * time.Second, 30 * time.Second, 5 * time.Minute, time.Hour}
	if len(cfg.Queue.RetryTiers) != len(want) {
		t.Fatalf("default queue.retry_tiers = %v, want %v", cfg.Queue.RetryTiers, want)
	}
	for i, d := range want {
		if cfg.Queue.RetryTiers[i] != d {
			t.Fatalf("default queue.retry_tiers = %v, want %v", cfg.Queue.RetryTiers, want)
		}
	}
}

func TestQueueConfig_RetryTiersEnvOverride(t *testing.T) {
	t.Setenv("QUEUE_RETRY_TIERS", "1s,10s")
	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	got := cfg.Queue.RetryTiers
	if len(got) != 2 || got[0] != time.Second || got[1] != 10*time.Second {
		t.Fatalf("QUEUE_RETRY_TIERS override: got %v", got)
	}
}

func TestQueueConfig_DefaultMaxAttempts(t *testing.T) {
	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Queue == nil {
		t.Fatal("expected [queue] section to be present")
	}
	if cfg.Queue.MaxAttempts != 50 {
		t.Fatalf("default queue.max_attempts = %d, want 50", cfg.Queue.MaxAttempts)
	}
}

func TestQueueConfig_EnvOverride(t *testing.T) {
	t.Setenv("QUEUE_MAX_ATTEMPTS", "9")
	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Queue == nil || cfg.Queue.MaxAttempts != 9 {
		t.Fatalf("QUEUE_MAX_ATTEMPTS override: got %+v, want max_attempts=9", cfg.Queue)
	}
}

func TestQueueConfig_DefaultProxyFailureCap(t *testing.T) {
	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Queue.ProxyFailureCap != 20 {
		t.Fatalf("default queue.proxy_failure_cap = %d, want 20", cfg.Queue.ProxyFailureCap)
	}
}

func TestQueueConfig_ProxyFailureCapEnvOverrideAndValidation(t *testing.T) {
	t.Setenv("QUEUE_PROXY_FAILURE_CAP", "7")
	cfg, err := NewConfig()
	if err != nil || cfg.Queue.ProxyFailureCap != 7 {
		t.Fatalf("QUEUE_PROXY_FAILURE_CAP=7: queue %+v, err %v", cfg.Queue, err)
	}
	for _, val := range []string{"0", "-1"} {
		t.Setenv("QUEUE_PROXY_FAILURE_CAP", val)
		if _, err := NewConfig(); err == nil {
			t.Fatalf("accepted QUEUE_PROXY_FAILURE_CAP=%q", val)
		}
	}
}
