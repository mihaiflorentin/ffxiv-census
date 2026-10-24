package cli

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/container"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestCLI_Export(t *testing.T) {
	container.Load = container.NewServiceContainer()
	db := container.Load.Database()
	if db == nil {
		t.Skip("postgres not available")
	}
	t.Cleanup(func() {
		_, _ = db.Execute(context.Background(), "TRUNCATE characters, character_jobs RESTART IDENTITY CASCADE")
	})
	_, _ = db.Execute(context.Background(), "TRUNCATE characters, character_jobs RESTART IDENTITY CASCADE")
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	charRepo := container.Load.CharacterRepository()
	_ = charRepo.Upsert(ctx, contract.CharacterRecord{
		ID:          100,
		Name:        "Thancred Waters",
		World:       "Balmung",
		Datacenter:  "Crystal",
		Region:      "NA",
		Race:        "Hyur",
		Gender:      1,
		FirstSeenAt: now,
	}, nil)

	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "export.csv.gz")

	err := runExport(ctx, outPath, "csv", true, contract.CharacterFilter{})
	if err != nil {
		t.Fatalf("runExport: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read export file: %v", err)
	}

	gzReader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer func() { _ = gzReader.Close() }()

	reader := csv.NewReader(gzReader)
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("csv ReadAll: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d rows, want 2", len(records))
	}
	if records[1][1] != "Thancred Waters" {
		t.Errorf("name = %q, want Thancred Waters", records[1][1])
	}
}

func TestCLI_Export_ActiveFlagUsesActivityWindow(t *testing.T) {
	container.Load = container.NewServiceContainer()
	db := container.Load.Database()
	if db == nil {
		t.Skip("postgres not available")
	}
	t.Cleanup(func() {
		_, _ = db.Execute(context.Background(), "TRUNCATE characters, character_jobs RESTART IDENTITY CASCADE")
	})
	_, _ = db.Execute(context.Background(), "TRUNCATE characters, character_jobs RESTART IDENTITY CASCADE")
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	sixtyDaysAgo := now.Add(-60 * 24 * time.Hour)
	hundredDaysAgo := now.Add(-100 * 24 * time.Hour)

	charRepo := container.Load.CharacterRepository()
	for _, rec := range []contract.CharacterRecord{
		{ID: 100, Name: "Thancred Waters", World: "Balmung", Datacenter: "Crystal", Region: "NA", Race: "Hyur", Gender: 1, FirstSeenAt: now, LatestAchievementAt: &sixtyDaysAgo},
		{ID: 101, Name: "Urianger Augurelt", World: "Balmung", Datacenter: "Crystal", Region: "NA", Race: "Elezen", Gender: 1, FirstSeenAt: now, LatestAchievementAt: &hundredDaysAgo},
	} {
		if err := charRepo.Upsert(ctx, rec, nil); err != nil {
			t.Fatalf("Upsert(%d): %v", rec.ID, err)
		}
	}

	outPath := filepath.Join(t.TempDir(), "export.csv")
	if err := runExport(ctx, outPath, "csv", false, contract.CharacterFilter{}); err != nil {
		t.Fatalf("runExport: %v", err)
	}
	f, err := os.Open(outPath)
	if err != nil {
		t.Fatalf("open export: %v", err)
	}
	defer func() { _ = f.Close() }()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("csv ReadAll: %v", err)
	}
	active := map[string]string{}
	for _, row := range records[1:] {
		active[row[1]] = row[11]
	}
	// The embedded config sets a 90-day activity window.
	if active["Thancred Waters"] != "true" || active["Urianger Augurelt"] != "false" {
		t.Fatalf("is_active by name = %v, want Thancred true (60 days) and Urianger false (100 days)", active)
	}
}
