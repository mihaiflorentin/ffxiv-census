package metrics

import (
	"hash/fnv"
	"strconv"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// proxyBuckets bounds the proxy_bucket label: proxy addresses are hashed into
// this many buckets so label cardinality stays fixed however large the pool.
const proxyBuckets = 64

// directBucket labels requests that bypass any proxy.
const directBucket = "direct"

// LodestoneRequestObserver counts Lodestone request outcomes per proxy bucket.
type LodestoneRequestObserver struct {
	requests *Counter
}

func NewLodestoneRequestObserver(registry *Registry) *LodestoneRequestObserver {
	return &LodestoneRequestObserver{
		requests: registry.NewCounter("lodestone_requests_total", "Lodestone request attempts by outcome and hashed proxy bucket"),
	}
}

func (o *LodestoneRequestObserver) ObserveRequest(proxy string, outcome contract.LodestoneOutcome) {
	o.requests.Inc(map[string]string{"outcome": string(outcome), "proxy_bucket": proxyBucket(proxy)})
}

// proxyBucket maps a proxy address to one of proxyBuckets stable buckets
// ("00".."63"), or "direct" when no proxy is used. The address itself is
// never exposed.
func proxyBucket(proxy string) string {
	if proxy == "" {
		return directBucket
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(proxy))
	n := int(h.Sum32() % proxyBuckets)
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

var _ contract.LodestoneRequestObserver = (*LodestoneRequestObserver)(nil)
