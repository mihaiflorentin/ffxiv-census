package metrics

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestLodestoneRequestObserver_CountsEachOutcome(t *testing.T) {
	registry := NewRegistry()
	observer := NewLodestoneRequestObserver(registry)
	proxy := "http://10.0.0.1:8080"
	bucket := proxyBucket(proxy)

	observer.ObserveRequest(proxy, contract.LodestoneOutcomeSuccess)
	observer.ObserveRequest(proxy, contract.LodestoneOutcomeSuccess)
	observer.ObserveRequest(proxy, contract.LodestoneOutcomeRateLimited)
	observer.ObserveRequest(proxy, contract.LodestoneOutcomeChallenge)
	observer.ObserveRequest(proxy, contract.LodestoneOutcomeFailure)

	out := registry.Gather()
	for outcome, n := range map[string]int{"success": 2, "rate_limited": 1, "challenge": 1, "failure": 1} {
		want := fmt.Sprintf(`lodestone_requests_total{outcome="%s",proxy_bucket="%s"} %d`, outcome, bucket, n)
		if !strings.Contains(out, want) {
			t.Errorf("metrics missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "10.0.0.1") {
		t.Errorf("proxy address leaked into labels:\n%s", out)
	}
}

func TestLodestoneRequestObserver_BucketLabelIsBounded(t *testing.T) {
	registry := NewRegistry()
	observer := NewLodestoneRequestObserver(registry)
	for i := 0; i < 5000; i++ {
		observer.ObserveRequest(fmt.Sprintf("http://10.%d.%d.1:8080", i/250, i%250), contract.LodestoneOutcomeSuccess)
	}
	observer.ObserveRequest("", contract.LodestoneOutcomeSuccess)

	buckets := map[string]bool{}
	for _, m := range regexp.MustCompile(`proxy_bucket="([^"]*)"`).FindAllStringSubmatch(registry.Gather(), -1) {
		buckets[m[1]] = true
	}
	if len(buckets) > proxyBuckets+1 {
		t.Fatalf("distinct proxy_bucket values = %d, want <= %d", len(buckets), proxyBuckets+1)
	}
	if !buckets["direct"] {
		t.Errorf("direct (no proxy) requests should use bucket %q: %v", "direct", buckets)
	}
}

func TestLodestoneRequestObserver_SameProxySameBucket(t *testing.T) {
	first := proxyBucket("socks5://a:1")
	if again := proxyBucket("socks5://a:1"); again != first {
		t.Fatalf("bucket must be deterministic: %q then %q", first, again)
	}
}
