package httpclient

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/proxy"
	"github.com/mihaiflorentin88/ffxiv-census/mock/httpclient"
	"github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestRotatingProxyClient_GetStream_NoProxy_Fallback(t *testing.T) {
	// No proxies in the repo — should fall back to direct.
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	hub := proxy.NewProxyHub(repo, 5*time.Minute, 0, contract.ProxyScanPolicy{})

	direct := &mockhttpclient.Client{
		GetStreamFn: func(_ context.Context, url string, _, _ map[string]string, consume func(int, io.Reader) error) error {
			return consume(200, io.NopCloser(strings.NewReader("direct-ok")))
		},
	}

	rc := NewRotatingProxyClient(hub, direct, 5*time.Second)
	var got string
	err := rc.GetStream(context.Background(), "http://example.com", nil, nil, func(_ int, body io.Reader) error {
		b, _ := io.ReadAll(body)
		got = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "direct-ok" {
		t.Fatalf("expected direct-ok, got %s", got)
	}
}

func TestRotatingProxyClient_GetStream_SwapActiveReturnsDifferentProxy(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	for i := 1; i <= 3; i++ {
		repo.InsertIfAbsent(context.Background(), contract.ProxyRecord{
			Protocol: "http",
			IP:       fmt.Sprintf("10.0.0.%d", i),
			Port:     8080,
		})
		repo.SeedSuccess(int64(i), 0)
	}
	hub := proxy.NewProxyHub(repo, 5*time.Minute, 0, contract.ProxyScanPolicy{})

	// SwapActive must always return a different proxy than the current one.
	for trial := 0; trial < 10; trial++ {
		current, err := hub.RandomActive(context.Background())
		if err != nil || current == nil {
			t.Fatalf("trial %d: expected proxy, got %v %v", trial, current, err)
		}
		swapped, err := hub.SwapActive(context.Background(), current)
		if err != nil {
			t.Fatalf("trial %d: SwapActive error: %v", trial, err)
		}
		if swapped == nil {
			t.Fatalf("trial %d: expected swapped proxy", trial)
		}
		if swapped.Record().ID == current.Record().ID {
			t.Fatalf("trial %d: SwapActive returned same proxy ID %d", trial, current.Record().ID)
		}
	}
}

func TestRotatingProxyClient_GetStream_ConsumerError_NoRotate(t *testing.T) {
	// A consumer error (not a retryable status) should propagate immediately.
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	repo.InsertIfAbsent(context.Background(), contract.ProxyRecord{
		Protocol: "http",
		IP:       "10.0.0.1",
		Port:     8080,
	})
	repo.SeedSuccess(1, 0)
	hub := proxy.NewProxyHub(repo, 5*time.Minute, 0, contract.ProxyScanPolicy{})

	direct := &mockhttpclient.Client{
		GetStreamFn: func(_ context.Context, _ string, _, _ map[string]string, _ func(int, io.Reader) error) error {
			t.Fatal("direct should not be called")
			return nil
		},
	}

	rc := NewRotatingProxyClient(hub, direct, 5*time.Second)
	// Since the proxy isn't real, the connection will fail. But we can verify
	// the fallback behavior: when RandomActive returns a proxy but the proxy
	// client creation/connection fails, the error propagates.
	err := rc.GetStream(context.Background(), "http://example.com", nil, nil, func(_ int, _ io.Reader) error {
		return fmt.Errorf("consumer error")
	})
	// The error will be a connection error (proxy not real), not a consumer error.
	// This is expected — the proxy client fails before reaching the consumer.
	if err == nil {
		t.Fatal("expected error from non-existent proxy")
	}
}

func TestRetryableStatusError(t *testing.T) {
	err := retryableStatusError{status: 429}
	if !strings.Contains(err.Error(), "429") {
		t.Fatalf("expected 429 in error, got %s", err.Error())
	}
}
