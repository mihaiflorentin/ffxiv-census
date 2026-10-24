package handler

import (
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// Export serves GET /api/v1/census/export: streaming bulk characters in CSV, JSON, or NDJSON format,
// optionally compressed with Gzip.
func (c *CensusController) Export(w http.ResponseWriter, r *http.Request) {
	if c.svc == nil {
		writeError(w, http.StatusInternalServerError, "census service unavailable")
		return
	}

	query := r.URL.Query()
	format := strings.ToLower(query.Get("format"))
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "json" && format != "ndjson" && format != "jsonl" {
		writeError(w, http.StatusBadRequest, "invalid format, supported formats: csv, json, ndjson (or jsonl)")
		return
	}
	if format == "jsonl" {
		format = "ndjson"
	}

	// Determine if gzip compression is requested via ?gzip=true or Accept-Encoding header.
	useGzip := false
	if gzipParam := query.Get("gzip"); gzipParam != "" {
		if val, err := strconv.ParseBool(gzipParam); err == nil && val {
			useGzip = true
		}
	} else if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		useGzip = true
	}

	f := contract.CharacterFilter{
		World:      query.Get("world"),
		Datacenter: query.Get("datacenter"),
		Region:     query.Get("region"),
		Race:       query.Get("race"),
		Name:       query.Get("name"),
	}

	var filename string
	var contentType string

	switch format {
	case "csv":
		contentType = "text/csv; charset=utf-8"
		filename = "ffxiv-census-characters.csv"
	case "json":
		contentType = "application/json"
		filename = "ffxiv-census-characters.json"
	case "ndjson":
		contentType = "application/x-ndjson"
		filename = "ffxiv-census-characters.ndjson"
	}

	if useGzip {
		w.Header().Set("Content-Encoding", "gzip")
		filename += ".gz"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	var out io.Writer = w
	var gzWriter *gzip.Writer
	if useGzip {
		gzWriter = gzip.NewWriter(w)
		out = gzWriter
		defer func() {
			_ = gzWriter.Close()
		}()
	}

	flusher, _ := w.(http.Flusher)

	flushCount := 0
	maybeFlush := func() {
		flushCount++
		if flushCount%500 == 0 {
			if gzWriter != nil {
				_ = gzWriter.Flush()
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}

	activeSince := c.svc.ActivitySince()

	switch format {
	case "csv":
		csvWriter := csv.NewWriter(out)
		header := []string{
			"id", "name", "world", "datacenter", "region", "race", "gender",
			"fc_id", "fc_name", "achievements_private", "latest_achievement_id",
			"is_active", "first_seen_at", "last_census_at",
		}
		if err := csvWriter.Write(header); err != nil {
			return
		}
		csvWriter.Flush()

		_ = c.svc.StreamCharacters(r.Context(), f, func(rec contract.CharacterRecord) error {
			item := toCharacterListItem(&rec, activeSince)
			fcID := ""
			if item.FreeCompanyID != nil {
				fcID = *item.FreeCompanyID
			}
			fcName := ""
			if item.FreeCompanyName != nil {
				fcName = *item.FreeCompanyName
			}
			latestAchID := ""
			if item.LatestAchievementID != nil {
				latestAchID = strconv.FormatUint(uint64(*item.LatestAchievementID), 10)
			}
			lastCensus := ""
			if item.LastCensusAt != nil {
				lastCensus = item.LastCensusAt.UTC().Format(time.RFC3339)
			}

			row := []string{
				strconv.FormatUint(uint64(item.ID), 10),
				item.Name,
				item.World,
				item.Datacenter,
				item.Region,
				item.Race,
				strconv.Itoa(int(item.Gender)),
				fcID,
				fcName,
				strconv.FormatBool(item.AchievementsPrivate),
				latestAchID,
				strconv.FormatBool(item.IsActive),
				item.FirstSeenAt.UTC().Format(time.RFC3339),
				lastCensus,
			}
			if err := csvWriter.Write(row); err != nil {
				return err
			}
			maybeFlush()
			return nil
		})
		csvWriter.Flush()

	case "json":
		if _, err := io.WriteString(out, "[\n"); err != nil {
			return
		}
		first := true
		_ = c.svc.StreamCharacters(r.Context(), f, func(rec contract.CharacterRecord) error {
			item := toCharacterListItem(&rec, activeSince)
			if !first {
				if _, err := io.WriteString(out, ",\n"); err != nil {
					return err
				}
			} else {
				first = false
			}
			data, err := json.Marshal(item)
			if err != nil {
				return err
			}
			if _, err := out.Write(data); err != nil {
				return err
			}
			maybeFlush()
			return nil
		})
		_, _ = io.WriteString(out, "\n]\n")

	case "ndjson":
		_ = c.svc.StreamCharacters(r.Context(), f, func(rec contract.CharacterRecord) error {
			item := toCharacterListItem(&rec, activeSince)
			data, err := json.Marshal(item)
			if err != nil {
				return err
			}
			if _, err := out.Write(data); err != nil {
				return err
			}
			if _, err := io.WriteString(out, "\n"); err != nil {
				return err
			}
			maybeFlush()
			return nil
		})
	}

	if gzWriter != nil {
		_ = gzWriter.Flush()
	}
	if flusher != nil {
		flusher.Flush()
	}
}
