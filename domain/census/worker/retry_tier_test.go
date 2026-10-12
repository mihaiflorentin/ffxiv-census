package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	censushandler "github.com/mihaiflorentin88/ffxiv-census/domain/census/handler"
	proxydomain "github.com/mihaiflorentin88/ffxiv-census/domain/proxy"
	"github.com/mihaiflorentin88/ffxiv-census/mock/queue"
	"github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// runTieredJob publishes one id-sweep job to the in-memory queue and runs a
// proxy-mode worker whose handler always returns jobErr. It stops once the
// queue has recorded the first retry and returns that record.
func runTieredJob(t *testing.T, jobErr error) mockqueue.Retry {
	t.Helper()
	repo := repository.NewFakeProxyRepository(workerPolicy)
	seedFreshProxies(t, repo, 4)
	hub := proxydomain.NewProxyHub(repo, 5*time.Minute, retryFloor, workerPolicy).
		WithDestinationCooldownCap(retryCap)
	q := mockqueue.NewFake()
	w := newTestCensusWorker(q, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := q.Publish(context.Background(), contract.QueueJob{Type: censushandler.EventIDSweep, Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	handlers := func(client contract.LodestoneClient, _ contract.ProviderRateLimiter) *censushandler.Registry {
		reg := censushandler.NewRegistry()
		reg.Register(censushandler.EventIDSweep, handlerFunc(func() error { return jobErr }))
		return reg
	}
	newClient := func(addr string, _ contract.ProviderRateLimiter) (contract.LodestoneClient, error) {
		return &addrClient{addr: addr}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- w.RunEventsWithProxy(ctx, []string{censushandler.EventIDSweep}, 1, "test", hub, handlers, newClient,
			func() contract.ProviderRateLimiter { return nil })
	}()
	deadline := time.Now().Add(10 * time.Second)
	for len(q.Retries()) == 0 {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("job was never returned to the queue")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-errCh
	return q.Retries()[0]
}

func TestRetryTiers_ChallengeFailureDoesNotCountAsAJobAttempt(t *testing.T) {
	r := runTieredJob(t, challengeErr())
	if r.Attempts != 0 || !r.ProxyFault || r.Tier != 0 {
		t.Fatalf("retry = %+v, want attempts 0, proxy fault, first tier", r)
	}
}

func TestRetryTiers_TimeoutAndServerErrorDoNotCountAsJobAttempts(t *testing.T) {
	for name, err := range map[string]error{"timeout": timeoutErr(), "5xx": serverErr()} {
		t.Run(name, func(t *testing.T) {
			if r := runTieredJob(t, err); r.Attempts != 0 || !r.ProxyFault {
				t.Fatalf("retry = %+v, want attempts 0 and a proxy fault", r)
			}
		})
	}
}

func TestRetryTiers_JobFailureCountsAsAnAttempt(t *testing.T) {
	r := runTieredJob(t, errors.New("parse character page"))
	if r.Attempts != 1 || r.ProxyFault || r.Tier != 0 || r.Delay != 5*time.Second {
		t.Fatalf("retry = %+v, want attempts 1 in the 5s tier", r)
	}
}
