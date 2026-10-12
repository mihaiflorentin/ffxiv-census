package repository_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func listedRec(ip string, port int) contract.ProxyRecord {
	return contract.ProxyRecord{Protocol: "http", IP: ip, Port: port, Source: "prov"}
}

// proxyIDByIP returns the id of the single proxy stored under ip.
func proxyIDByIP(t *testing.T, driver contract.DatabaseDriver, ip string) int64 {
	t.Helper()
	db, err := driver.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	var id int64
	if err := db.QueryRowContext(context.Background(), `SELECT id FROM proxies WHERE ip = $1`, ip).Scan(&id); err != nil {
		t.Fatalf("lookup %s: %v", ip, err)
	}
	return id
}

func TestInsertNew_InsertsOnlyUnknownEndpoints(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	mustInsertProxy(t, repo, "10.9.0.1", 9001)
	country, anon, uptime := "DE", "elite", 87.5

	rich := listedRec("10.9.0.3", 9003)
	rich.Country, rich.Anonymity, rich.UptimePercent = &country, &anon, &uptime
	got, err := repo.InsertNew(ctx, []contract.ProxyRecord{
		listedRec("10.9.0.1", 9001), // already stored
		listedRec("10.9.0.2", 9002),
		rich,
		listedRec("10.9.0.2", 9002), // duplicate inside the call
	}, 0)
	if err != nil {
		t.Fatalf("InsertNew: %v", err)
	}
	want := []contract.ProxyEndpoint{endpoint("10.9.0.2", 9002), endpoint("10.9.0.3", 9003)}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("inserted = %v, want %v", got, want)
	}
	if n, err := repo.Count(ctx); err != nil || n != 3 {
		t.Fatalf("Count = %d, %v; want 3", n, err)
	}
	p := mustGetProxy(t, repo, proxyIDByIP(t, driver, "10.9.0.3"))
	if p.Source != "prov" || p.Status != contract.ProxyStatusInactive || p.FailCount != 0 || p.GeneralHealthy {
		t.Errorf("new row = %+v, want inactive, no evidence, source prov", p)
	}
	if p.Country == nil || *p.Country != "DE" || p.Anonymity == nil || *p.Anonymity != "elite" ||
		p.UptimePercent == nil || *p.UptimePercent != 87.5 {
		t.Errorf("optional fields not stored: %+v", p)
	}
	if p.LastListedAt == nil || p.RetiredAt != nil {
		t.Errorf("last_listed_at = %v, retired_at = %v; want stamped and live", p.LastListedAt, p.RetiredAt)
	}
	plain := mustGetProxy(t, repo, proxyIDByIP(t, driver, "10.9.0.2"))
	if plain.Country != nil || plain.Anonymity != nil || plain.UptimePercent != nil {
		t.Errorf("absent optional fields stored as values: %+v", plain)
	}
}

func TestInsertNew_DoesNotReinsertRetiredProxy(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	id := mustInsertProxy(t, repo, "10.9.1.1", 9001)
	seedScanState(t, driver, id, `retired_at = now() - interval '3 days', fail_count = 14`)

	got, err := repo.InsertNew(ctx, []contract.ProxyRecord{listedRec("10.9.1.1", 9001)}, 0)
	if err != nil {
		t.Fatalf("InsertNew: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("inserted = %v, want nothing", got)
	}
	if n, err := repo.Count(ctx); err != nil || n != 1 {
		t.Fatalf("Count = %d, %v; want 1", n, err)
	}
	if p := mustGetProxy(t, repo, id); p.RetiredAt == nil || p.FailCount != 14 {
		t.Fatalf("retired row changed by InsertNew: %+v", p)
	}
}

func TestInsertNew_LimitKeepsTheFirstNewRecords(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	mustInsertProxy(t, repo, "10.9.2.1", 9001)

	got, err := repo.InsertNew(ctx, []contract.ProxyRecord{
		listedRec("10.9.2.1", 9001), // known: does not use the budget
		listedRec("10.9.2.9", 9001),
		listedRec("10.9.2.3", 9001),
		listedRec("10.9.2.5", 9001),
	}, 2)
	if err != nil {
		t.Fatalf("InsertNew: %v", err)
	}
	want := []contract.ProxyEndpoint{endpoint("10.9.2.9", 9001), endpoint("10.9.2.3", 9001)}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("inserted = %v, want %v", got, want)
	}
	if n, err := repo.Count(ctx); err != nil || n != 3 {
		t.Fatalf("Count = %d, %v; want 3", n, err)
	}
}

func TestInsertNew_KnownEndpointsDoNotConsumeIDs(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	first := mustInsertProxy(t, repo, "10.9.3.1", 9001)

	known := make([]contract.ProxyRecord, 300)
	for i := range known {
		known[i] = listedRec("10.9.3.1", 9001)
	}
	if _, err := repo.InsertNew(ctx, known, 0); err != nil {
		t.Fatalf("InsertNew: %v", err)
	}
	next := mustInsertProxy(t, repo, "10.9.3.2", 9001)
	if next != first+1 {
		t.Fatalf("next id = %d, want %d: re-listed known endpoints burned serial ids", next, first+1)
	}
}

func TestInsertNew_HandlesMoreRecordsThanOneChunk(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())

	recs := make([]contract.ProxyRecord, 12000)
	for i := range recs {
		recs[i] = listedRec(fmt.Sprintf("10.10.%d.%d", i/250, i%250), 9001)
	}
	got, err := repo.InsertNew(ctx, recs, 0)
	if err != nil {
		t.Fatalf("InsertNew: %v", err)
	}
	if len(got) != len(recs) {
		t.Fatalf("inserted %d, want %d", len(got), len(recs))
	}
	capped, err := repo.InsertNew(ctx, []contract.ProxyRecord{listedRec("10.20.0.1", 1), listedRec("10.20.0.2", 1)}, 1)
	if err != nil || len(capped) != 1 {
		t.Fatalf("capped insert = %v, %v", capped, err)
	}
}
