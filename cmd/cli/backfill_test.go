package cli

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestBackfillJobMasksCmdRegisteredWithDocumentedFlags(t *testing.T) {
	command, _, err := rootCmd.Find([]string{"backfill", "job-masks"})
	if err != nil || command == nil || command.Name() != "job-masks" {
		t.Fatalf("backfill job-masks command not registered: command=%v err=%v", command, err)
	}
	batch := command.Flags().Lookup("batch-size")
	pause := command.Flags().Lookup("pause")
	if batch == nil || batch.DefValue != "1000" || pause == nil || pause.DefValue != "200ms" {
		t.Fatalf("flags batch-size=%#v pause=%#v, want defaults 1000 and 200ms", batch, pause)
	}
	help := command.UsageString() + command.Long
	for _, want := range []string{"--batch-size", "--pause", "resum", "never runs at boot"} {
		if !strings.Contains(help, want) {
			t.Errorf("help does not mention %q:\n%s", want, help)
		}
	}
}

func TestRunBackfillJobMasksWalksUntilDonePausingBetweenBatches(t *testing.T) {
	fake := mockrepo.NewJobMaskBackfillFake(
		contract.JobMaskBackfillBatch{Scanned: 2, Filled: 2, Cursor: 20},
		contract.JobMaskBackfillBatch{Scanned: 2, Filled: 1, Cursor: 40},
		contract.JobMaskBackfillBatch{Scanned: 1, Filled: 1, Cursor: 50, Done: true},
	)
	var pauses []time.Duration
	sleep := func(_ context.Context, d time.Duration) error {
		pauses = append(pauses, d)
		return nil
	}
	var out bytes.Buffer
	if err := runBackfillJobMasks(context.Background(), fake, 2, 3*time.Second, sleep, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fake.Limits, []int{2, 2, 2}) {
		t.Errorf("batch limits = %v, want [2 2 2]", fake.Limits)
	}
	if !reflect.DeepEqual(pauses, []time.Duration{3 * time.Second, 3 * time.Second}) {
		t.Errorf("pauses = %v, want two of 3s (none after the last batch)", pauses)
	}
	if !strings.Contains(out.String(), "complete: scanned=5 filled=4 cursor=50") {
		t.Errorf("output = %q, want the completion summary", out.String())
	}
}

func TestRunBackfillJobMasksStopsCleanlyWhenInterrupted(t *testing.T) {
	fake := mockrepo.NewJobMaskBackfillFake(
		contract.JobMaskBackfillBatch{Scanned: 2, Filled: 2, Cursor: 20},
		contract.JobMaskBackfillBatch{Scanned: 2, Filled: 2, Cursor: 40, Done: true},
	)
	sleep := func(context.Context, time.Duration) error { return context.Canceled }
	var out bytes.Buffer
	err := runBackfillJobMasks(context.Background(), fake, 2, time.Second, sleep, &out)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(fake.Limits) != 1 || !strings.Contains(out.String(), "cursor=20") {
		t.Fatalf("batches = %d, output %q; want one batch and the cursor to resume from", len(fake.Limits), out.String())
	}
}

func TestRunBackfillJobMasksRejectsNonPositiveBatchSize(t *testing.T) {
	fake := mockrepo.NewJobMaskBackfillFake()
	if err := runBackfillJobMasks(context.Background(), fake, 0, 0, nil, &bytes.Buffer{}); err == nil {
		t.Fatal("batch size 0 accepted, want error")
	}
	if len(fake.Limits) != 0 {
		t.Fatal("a batch ran with batch size 0")
	}
}
