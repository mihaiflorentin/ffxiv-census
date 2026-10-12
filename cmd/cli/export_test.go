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
