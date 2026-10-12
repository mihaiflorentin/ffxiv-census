package mockmetrics

import (
	"sync"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// LodestoneRequest is one observation recorded by LodestoneRequestObserver.
type LodestoneRequest struct {
	Proxy   string
	Outcome contract.LodestoneOutcome
}

// LodestoneRequestObserver is a contract.LodestoneRequestObserver that
// records every observation.
type LodestoneRequestObserver struct {
	mu       sync.Mutex
	requests []LodestoneRequest
}

func (o *LodestoneRequestObserver) ObserveRequest(proxy string, outcome contract.LodestoneOutcome) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.requests = append(o.requests, LodestoneRequest{Proxy: proxy, Outcome: outcome})
}

// Requests returns a copy of the recorded observations in arrival order.
func (o *LodestoneRequestObserver) Requests() []LodestoneRequest {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]LodestoneRequest(nil), o.requests...)
}

var _ contract.LodestoneRequestObserver = (*LodestoneRequestObserver)(nil)
