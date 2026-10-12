package metrics_test

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/metrics"
	_ "modernc.org/sqlite"
)

type dummyQueue struct {
	depth int
}

func (d *dummyQueue) Depth() int {
	return d.depth
}

func TestRegistry_BuiltInCollectors(t *testing.T) {
	reg := metrics.NewRegistry()

	// 1. Process uptime collector
	startTime := time.Now().Add(-5 * time.Second)
	metrics.RegisterProcessMetrics(reg, startTime)

	// 2. SQLite pool metrics collector
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	defer func() { _ = db.Close() }()

	metrics.RegisterSQLiteMetrics(reg, db)

	// 3. Queue depth metrics collector
	q := &dummyQueue{depth: 42}
	metrics.RegisterQueueMetrics(reg, func() int {
		return q.Depth()
	})

	out := reg.Gather()

	if !strings.Contains(out, "process_uptime_seconds") {
		t.Errorf("expected process_uptime_seconds in output:\n%s", out)
	}
	if !strings.Contains(out, "sqlite_open_connections") {
		t.Errorf("expected sqlite_open_connections in output:\n%s", out)
	}
	if !strings.Contains(out, "queue_jobs_depth 42") {
		t.Errorf("expected queue_jobs_depth 42 in output:\n%s", out)
	}
}
