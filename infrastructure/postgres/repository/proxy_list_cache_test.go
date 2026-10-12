package repository_test

import (
	"context"
	"testing"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestListValidators_PersistAndReplace(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	var cache contract.ProxyListCache = repository.NewProxyRepository(driver, testScanPolicy())

	got, err := cache.LoadListValidators(ctx)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty cache = %v, %v", got, err)
	}

	if err := cache.SaveListValidators(ctx, "https://x/http.txt", contract.ListValidators{ETag: `"a"`, LastModified: "Mon, 12 Oct 2026 10:00:00 GMT"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := cache.SaveListValidators(ctx, "https://x/socks.txt", contract.ListValidators{LastModified: "Mon, 12 Oct 2026 11:00:00 GMT"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := cache.SaveListValidators(ctx, "https://x/http.txt", contract.ListValidators{ETag: `"b"`}); err != nil {
		t.Fatalf("Save replace: %v", err)
	}

	got, err = cache.LoadListValidators(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Load = %v, want 2 URLs", got)
	}
	if v := got["https://x/http.txt"]; v.ETag != `"b"` || v.LastModified != "" {
		t.Errorf("replaced entry = %+v, want the new ETag only", v)
	}
	if v := got["https://x/socks.txt"]; v.ETag != "" || v.LastModified == "" {
		t.Errorf("last-modified-only entry = %+v", v)
	}
}
