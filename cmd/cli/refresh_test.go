package cli

import (
	"context"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestRefreshCmdRegistered(t *testing.T) {
	command, _, err := rootCmd.Find([]string{"refresh", "ui-stats"})
	if err != nil || command == nil || command.Name() != "ui-stats" {
		t.Fatalf("refresh ui-stats command not registered: command=%v err=%v", command, err)
	}
}

func TestRunRefreshUIStatsWithService(t *testing.T) {
	generated := time.Now().UTC()
	fake := mockrepo.NewUIStatsFake(&contract.UIStatsSnapshot{
		SchemaVersion:    contract.UIStatsSchemaVersion,
		GeneratedAt:      generated,
		ActivitySince:    generated.Add(-30 * 24 * time.Hour),
		MaxLevel:         100,
		SourceCharacters: 10,
		Summary:          contract.StatsSummary{Total: 10},
	})
	svc := census.NewUIStatsService(fake, time.Minute, time.Hour)
	opts := contract.UIStatsRefreshOptions{ActivitySince: generated.Add(-30 * 24 * time.Hour), MaxLevel: 100, Timeout: time.Minute}
	if err := runRefreshUIStatsWithService(context.Background(), svc, opts); err != nil {
		t.Fatal(err)
	}
	if fake.RefreshCalls != 1 || fake.LastRefreshOptions.MaxLevel != 100 {
		t.Fatalf("refresh calls/options = %d %#v", fake.RefreshCalls, fake.LastRefreshOptions)
	}
}

func TestRefreshUIStatsModeFlag(t *testing.T) {
	flag := refreshUIStatsCmd.Flags().Lookup("mode")
	if flag == nil || flag.DefValue != "full" {
		t.Fatalf("--mode flag = %#v, want default full", flag)
	}
}

func TestParseRefreshMode(t *testing.T) {
	for in, want := range map[string]contract.UIStatsRefreshMode{
		"":            contract.UIStatsRefreshFull,
		"full":        contract.UIStatsRefreshFull,
		"incremental": contract.UIStatsRefreshIncremental,
	} {
		got, err := parseRefreshMode(in)
		if err != nil || got != want {
			t.Fatalf("parseRefreshMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := parseRefreshMode("sometimes"); err == nil {
		t.Fatal("parseRefreshMode(sometimes) succeeded, want error")
	}
}
