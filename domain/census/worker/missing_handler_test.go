package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census/handler"
	proxydomain "github.com/mihaiflorentin88/ffxiv-census/domain/proxy"
	"github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// TestConsumerJob_MissingHandlerWrapsErrNoHandler proves that a delivery
// whose event type has no registered handler in this process surfaces an
// error carrying contract.ErrNoHandler. The queue adapter classifies that
// sentinel and dead-parks the delivery — retrying it forever would churn
// the cluster, and dropping it silently would lose the message.
func TestConsumerJob_MissingHandlerWrapsErrNoHandler(t *testing.T) {
	repo := repository.NewFakeProxyRepository(workerPolicy)
	seedTwoFreshProxies(t, repo)
	hub := proxydomain.NewProxyHub(repo, 5*time.Minute, time.Minute, workerPolicy)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := &fakeProxyQueue{}
	w := newTestCensusWorker(q, logger)
	if err := q.Publish(context.Background(), contract.QueueJob{
		Type:    handler.EventIDSweep,
		Payload: []byte(`{}`),
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Empty registry: no process in this deployment can ever handle the
	// event, so the only sane outcomes are dead-park (this sentinel) or
	// observable loss — never an endless retry ladder.
	handlers := func(contract.LodestoneClient, contract.ProviderRateLimiter) *handler.Registry {
		return handler.NewRegistry()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- w.RunEventsWithProxy(
			ctx,
			[]string{handler.EventIDSweep},
			1,
			"test",
			hub,
			handlers,
			func(string, contract.ProviderRateLimiter) (contract.LodestoneClient, error) {
				return &fakeLodestoneClient{}, nil
			},
			func() contract.ProviderRateLimiter { return nil },
		)
	}()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected missing-handler dispatch to surface an error, got nil")
		}
		if !errors.Is(err, contract.ErrNoHandler) {
			t.Fatalf("missing-handler error must wrap contract.ErrNoHandler, got %v", err)
		}
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("worker did not exit in time")
	}
}
