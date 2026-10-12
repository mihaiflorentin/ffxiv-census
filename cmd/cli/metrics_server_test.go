package cli

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/metrics"
)

func TestServeMetrics_ServesRegisteredCollectors(t *testing.T) {
	reg := metrics.NewRegistry()
	reg.NewCounter("worker_test_total", "test counter").Inc(map[string]string{"outcome": "success"})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	stop := serveMetrics(ln, reg, nil)

	resp, err := http.Get("http://" + ln.Addr().String() + "/metrics")
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), `worker_test_total{outcome="success"} 1`) {
		t.Fatalf("counter missing from scrape:\n%s", body)
	}

	stop()
	if _, err := http.Get("http://" + ln.Addr().String() + "/metrics"); err == nil {
		t.Fatal("expected scrape to fail after stop")
	}
}

func TestServeMetrics_StopIsBounded(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	stop := serveMetrics(ln, metrics.NewRegistry(), nil)

	// A half-sent request must not hold shutdown past its deadline.
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = conn.Write([]byte("GET /metrics HTTP/1.1\r\nHost: x\r\n"))

	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(metricsShutdownTimeout + 2*time.Second):
		t.Fatal("stop blocked past the shutdown timeout")
	}
}

func TestStartMetricsServer_DisabledWhenPortZero(t *testing.T) {
	stop, err := startMetricsServer(0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stop() // must be a safe no-op
}

func TestStartMetricsServer_BindFailure(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	if _, err := startMetricsServer(port, nil); err == nil {
		t.Fatal("expected error binding an occupied port")
	}
}

func TestConsumeCmds_HaveMetricsPortFlag(t *testing.T) {
	for _, cmd := range []*cobra.Command{consumeCmd, consumeFailedCmd} {
		fl := cmd.Flags().Lookup("metrics-port")
		if fl == nil {
			t.Fatalf("%s: missing --metrics-port flag", cmd.Name())
		}
		if fl.DefValue != "0" {
			t.Fatalf("%s: --metrics-port default = %s, want 0 (disabled)", cmd.Name(), fl.DefValue)
		}
	}
}
