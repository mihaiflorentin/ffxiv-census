package cli

import (
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mihaiflorentin88/ffxiv-census/container"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

var (
	exportOutput     string
	exportFormat     string
	exportGzip       bool
	exportWorld      string
	exportDatacenter string
	exportRegion     string
	exportRace       string
	exportName       string
)

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export census character data to CSV, JSON, or NDJSON",
	Long:  "Export census character records matching optional filters directly to a file or stdout, with optional gzip compression.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runExport(cmd.Context(), exportOutput, exportFormat, exportGzip, contract.CharacterFilter{
			World:      exportWorld,
			Datacenter: exportDatacenter,
			Region:     exportRegion,
			Race:       exportRace,
			Name:       exportName,
		})
	},
}

func runExport(ctx context.Context, output, format string, useGzip bool, filter contract.CharacterFilter) error {
	format = strings.ToLower(format)
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "json" && format != "ndjson" && format != "jsonl" {
		return fmt.Errorf("invalid format %q: supported formats are csv, json, ndjson", format)
	}
	if format == "jsonl" {
		format = "ndjson"
	}

	var dest io.Writer = os.Stdout
	if output != "" && output != "-" {
		if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
			return fmt.Errorf("create output directory: %w", err)
		}
		f, err := os.Create(output)
		if err != nil {
			return fmt.Errorf("create output file %q: %w", output, err)
		}
		defer func() { _ = f.Close() }()
		dest = f
	}

	out := io.Writer(dest)
	var gzWriter *gzip.Writer
	if useGzip {
		gzWriter = gzip.NewWriter(dest)
		out = gzWriter
		defer func() {
			_ = gzWriter.Close()
		}()
	}

	svc := container.Load.CensusService()
	count := 0

	switch format {
	case "csv":
		csvWriter := csv.NewWriter(out)
		header := []string{
			"id", "name", "world", "datacenter", "region", "race", "gender",
			"fc_id", "fc_name", "achievements_private", "latest_achievement_id",
			"is_active", "first_seen_at", "last_census_at",
		}
		if err := csvWriter.Write(header); err != nil {
			return fmt.Errorf("write csv header: %w", err)
		}

		err := svc.StreamCharacters(ctx, filter, func(rec contract.CharacterRecord) error {
			count++
			fcID := ""
			if rec.FreeCompanyID != nil {
				fcID = *rec.FreeCompanyID
			}
			fcName := ""
			if rec.FreeCompanyName != nil {
				fcName = *rec.FreeCompanyName
			}
			latestAchID := ""
			if rec.LatestAchievementID != nil {
				latestAchID = strconv.FormatUint(uint64(*rec.LatestAchievementID), 10)
			}
			isActive := false
			if rec.LatestAchievementAt != nil {
				cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
				isActive = !rec.LatestAchievementAt.Before(cutoff)
			}
			lastCensus := ""
			if rec.LastCensusAt != nil {
				lastCensus = rec.LastCensusAt.UTC().Format(time.RFC3339)
			}

			row := []string{
				strconv.FormatUint(uint64(rec.ID), 10),
				rec.Name,
				rec.World,
				rec.Datacenter,
				rec.Region,
				rec.Race,
				strconv.Itoa(int(rec.Gender)),
				fcID,
				fcName,
				strconv.FormatBool(rec.AchievementsPrivate),
				latestAchID,
				strconv.FormatBool(isActive),
				rec.FirstSeenAt.UTC().Format(time.RFC3339),
				lastCensus,
			}
			return csvWriter.Write(row)
		})
		if err != nil {
			return err
		}
		csvWriter.Flush()
		if err := csvWriter.Error(); err != nil {
			return fmt.Errorf("flush csv: %w", err)
		}

	case "json":
		if _, err := io.WriteString(out, "[\n"); err != nil {
			return err
		}
		first := true
		err := svc.StreamCharacters(ctx, filter, func(rec contract.CharacterRecord) error {
			count++
			if !first {
				if _, err := io.WriteString(out, ",\n"); err != nil {
					return err
				}
			} else {
				first = false
			}
			data, err := json.Marshal(rec)
			if err != nil {
				return err
			}
			_, err = out.Write(data)
			return err
		})
		if err != nil {
			return err
		}
		if _, err := io.WriteString(out, "\n]\n"); err != nil {
			return err
		}

	case "ndjson":
		err := svc.StreamCharacters(ctx, filter, func(rec contract.CharacterRecord) error {
			count++
			data, err := json.Marshal(rec)
			if err != nil {
				return err
			}
			if _, err := out.Write(data); err != nil {
				return err
			}
			_, err = io.WriteString(out, "\n")
			return err
		})
		if err != nil {
			return err
		}
	}

	if gzWriter != nil {
		if err := gzWriter.Flush(); err != nil {
			return fmt.Errorf("flush gzip: %w", err)
		}
	}

	if output != "" && output != "-" {
		fmt.Fprintf(os.Stderr, "Exported %d character records to %s\n", count, output)
	}

	return nil
}

func init() {
	exportCmd.Flags().StringVarP(&exportOutput, "output", "o", "", "Output file path (default stdout)")
	exportCmd.Flags().StringVarP(&exportFormat, "format", "f", "csv", "Output format: csv, json, ndjson")
	exportCmd.Flags().BoolVarP(&exportGzip, "gzip", "z", false, "Compress output with gzip")
	exportCmd.Flags().StringVar(&exportWorld, "world", "", "Filter by world")
	exportCmd.Flags().StringVar(&exportDatacenter, "datacenter", "", "Filter by datacenter")
	exportCmd.Flags().StringVar(&exportRegion, "region", "", "Filter by region")
	exportCmd.Flags().StringVar(&exportRace, "race", "", "Filter by race")
	exportCmd.Flags().StringVar(&exportName, "name", "", "Filter by character name (substring)")

	rootCmd.AddCommand(exportCmd)
}
