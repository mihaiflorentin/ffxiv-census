package cli

import (
	"context"
	"reflect"
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

func TestUIStatsRefreshOptionsCarryTheServiceWindows(t *testing.T) {
	svc := census.NewService(nil, nil, nil)
	svc.SetActivityWindow(45 * 24 * time.Hour)
	svc.SetNewCharactersWindow(60 * 24 * time.Hour)
	opts := uiStatsRefreshOptions(svc, time.Hour, contract.UIStatsRefreshIncremental)
	if opts.NewCharactersWindowDays != 60 {
		t.Fatalf("new-characters window = %d days, want 60", opts.NewCharactersWindowDays)
	}
	if days := census.WindowDays(time.Since(opts.ActivitySince)); days != 45 {
		t.Fatalf("activity cutoff = %d days ago, want 45", days)
	}
	if opts.MaxLevel != 100 || opts.Timeout != time.Hour || opts.Mode != contract.UIStatsRefreshIncremental {
		t.Fatalf("options = %#v", opts)
	}
}

func TestUIStatsRefreshOptionsCarryTheConfiguredLevelBands(t *testing.T) {
	svc := census.NewService(nil, nil, nil)
	expansions := append(append([]census.ExpansionConfig(nil), census.DefaultExpansions...), census.ExpansionConfig{Name: "Next", LevelCap: 110})
	svc.SetConfig(110, expansions)
	opts := uiStatsRefreshOptions(svc, time.Hour, contract.UIStatsRefreshFull)
	if want := census.LevelBands(expansions, 110); !reflect.DeepEqual(opts.LevelBands, want) {
		t.Fatalf("level bands = %v, want %v", opts.LevelBands, want)
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
