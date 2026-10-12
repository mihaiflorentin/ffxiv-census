package repository

import "github.com/mihaiflorentin88/ffxiv-census/port/contract"

// ClaimQueryForTest exposes the assembled claim statement and its bind
// arguments so tests can inspect the plan PostgreSQL builds for it.
func (r *ProxyRepository) ClaimQueryForTest(queue contract.ScanQueue, limit int) (string, []any, error) {
	return r.claimQuery(queue, limit, "test-token")
}
