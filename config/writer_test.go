package config

import (
	"testing"
	"time"
)

func TestWriterConfig_Defaults(t *testing.T) {
	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Writer == nil {
		t.Fatal("expected [writer] section to be present")
	}
	if cfg.Writer.BatchSize != 100 {
		t.Fatalf("default writer.batch_size = %d, want 100", cfg.Writer.BatchSize)
	}
	if cfg.Writer.FlushInterval != time.Second {
		t.Fatalf("default writer.flush_interval = %v, want 1s", cfg.Writer.FlushInterval)
	}
}

func TestWriterConfig_EnvOverride(t *testing.T) {
	t.Setenv("WRITER_BATCH_SIZE", "25")
	t.Setenv("WRITER_FLUSH_INTERVAL", "250ms")
	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Writer == nil || cfg.Writer.BatchSize != 25 || cfg.Writer.FlushInterval != 250*time.Millisecond {
		t.Fatalf("env override: got %+v, want batch_size=25 flush_interval=250ms", cfg.Writer)
	}
}
