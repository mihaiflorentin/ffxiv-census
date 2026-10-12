package handler

import (
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
	"github.com/mihaiflorentin88/ffxiv-census/port/dto/response"
)

func TestCensusController_Export_CSV(t *testing.T) {
	rig := newRig(t)
	now := time.Now().UTC().Truncate(time.Second)

	_ = rig.chars.Upsert(t.Context(), contract.CharacterRecord{
		ID:          1,
		Name:        "Tataru Taru",
		World:       "Ultros",
		Datacenter:  "Primal",
		Region:      "NA",
		Race:        "Lalafell",
		Gender:      2,
		FirstSeenAt: now,
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/census/export?format=csv", nil)
	rec := httptest.NewRecorder()
	rig.c.Export(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/csv; charset=utf-8")
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "ffxiv-census-characters.csv") {
		t.Errorf("Content-Disposition = %q, want containing ffxiv-census-characters.csv", cd)
	}

	reader := csv.NewReader(rec.Body)
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("csv ReadAll: %v", err)
	}
	if len(records) != 2 { // Header + 1 record
		t.Fatalf("got %d csv rows, want 2", len(records))
	}
	if records[0][0] != "id" || records[0][1] != "name" {
		t.Errorf("header = %v, unexpected", records[0])
	}
	if records[1][0] != "1" || records[1][1] != "Tataru Taru" {
		t.Errorf("row = %v, unexpected", records[1])
	}
}

func TestCensusController_Export_JSON(t *testing.T) {
	rig := newRig(t)
	now := time.Now().UTC().Truncate(time.Second)

	_ = rig.chars.Upsert(t.Context(), contract.CharacterRecord{
		ID:          1,
		Name:        "Tataru Taru",
		World:       "Ultros",
		FirstSeenAt: now,
	}, nil)
	_ = rig.chars.Upsert(t.Context(), contract.CharacterRecord{
		ID:          2,
		Name:        "Alphinaud Leveilleur",
		World:       "Leviathan",
		FirstSeenAt: now,
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/census/export?format=json", nil)
	rec := httptest.NewRecorder()
	rig.c.Export(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}

	var items []response.CharacterListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("json Unmarshal: %v (body: %s)", err, rec.Body.String())
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].ID != 1 || items[1].ID != 2 {
		t.Errorf("item IDs = [%d, %d], want [1, 2]", items[0].ID, items[1].ID)
	}
}

func TestCensusController_Export_NDJSON(t *testing.T) {
	rig := newRig(t)
	now := time.Now().UTC().Truncate(time.Second)

	_ = rig.chars.Upsert(t.Context(), contract.CharacterRecord{
		ID:          1,
		Name:        "Tataru Taru",
		World:       "Ultros",
		FirstSeenAt: now,
	}, nil)
	_ = rig.chars.Upsert(t.Context(), contract.CharacterRecord{
		ID:          2,
		Name:        "Alphinaud Leveilleur",
		World:       "Leviathan",
		FirstSeenAt: now,
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/census/export?format=ndjson", nil)
	rec := httptest.NewRecorder()
	rig.c.Export(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/x-ndjson")
	}

	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	var c1, c2 response.CharacterListItem
	if err := json.Unmarshal([]byte(lines[0]), &c1); err != nil {
		t.Fatalf("line 1 unmarshal: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &c2); err != nil {
		t.Fatalf("line 2 unmarshal: %v", err)
	}
	if c1.ID != 1 || c2.ID != 2 {
		t.Errorf("IDs = [%d, %d], want [1, 2]", c1.ID, c2.ID)
	}
}

func TestCensusController_Export_Gzip(t *testing.T) {
	rig := newRig(t)
	now := time.Now().UTC().Truncate(time.Second)

	_ = rig.chars.Upsert(t.Context(), contract.CharacterRecord{
		ID:          1,
		Name:        "Tataru Taru",
		World:       "Ultros",
		FirstSeenAt: now,
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/census/export?format=csv&gzip=true", nil)
	rec := httptest.NewRecorder()
	rig.c.Export(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ce := rec.Header().Get("Content-Encoding"); ce != "gzip" {
		t.Errorf("Content-Encoding = %q, want %q", ce, "gzip")
	}

	gzReader, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer func() { _ = gzReader.Close() }()

	decompressed, err := io.ReadAll(gzReader)
	if err != nil {
		t.Fatalf("decompression error: %v", err)
	}

	reader := csv.NewReader(bytes.NewReader(decompressed))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("csv ReadAll: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d rows, want 2", len(records))
	}
	if records[1][1] != "Tataru Taru" {
		t.Errorf("name = %q, want Tataru Taru", records[1][1])
	}
}

func TestCensusController_Export_InvalidFormat(t *testing.T) {
	rig := newRig(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/census/export?format=xml", nil)
	rec := httptest.NewRecorder()
	rig.c.Export(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
