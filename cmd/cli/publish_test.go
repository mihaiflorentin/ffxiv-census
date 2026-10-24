package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census/handler"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestComputeIDSweepRange_AutoDiscovery(t *testing.T) {
	maxIDFunc := func() (uint32, error) {
		return 500, nil
	}

	from, to, err := computeIDSweepRange(0, 0, 100, false, maxIDFunc)
	if err != nil {
		t.Fatalf("computeIDSweepRange: %v", err)
	}
	if from != 501 || to != 600 {
		t.Errorf("got [%d, %d], want [501, 600]", from, to)
	}

	// Explicit auto flag overrides explicit from/to
	from, to, err = computeIDSweepRange(10, 20, 100, true, maxIDFunc)
	if err != nil {
		t.Fatalf("computeIDSweepRange with auto=true: %v", err)
	}
	if from != 501 || to != 600 {
		t.Errorf("got [%d, %d], want [501, 600] with auto=true", from, to)
	}
}

func TestComputeIDSweepRange_AutoDiscovery_EmptyDB(t *testing.T) {
	maxIDFunc := func() (uint32, error) {
		return 0, nil
	}

	from, to, err := computeIDSweepRange(0, 0, 1000, false, maxIDFunc)
	if err != nil {
		t.Fatalf("computeIDSweepRange: %v", err)
	}
	if from != 1 || to != 1000 {
		t.Errorf("got [%d, %d], want [1, 1000]", from, to)
	}
}

func TestComputeIDSweepRange_FromOnly(t *testing.T) {
	from, to, err := computeIDSweepRange(100, 0, 50, false, nil)
	if err != nil {
		t.Fatalf("computeIDSweepRange: %v", err)
	}
	if from != 100 || to != 149 {
		t.Errorf("got [%d, %d], want [100, 149]", from, to)
	}
}

func TestComputeIDSweepRange_ToOnly(t *testing.T) {
	from, to, err := computeIDSweepRange(0, 50, 1000, false, nil)
	if err != nil {
		t.Fatalf("computeIDSweepRange: %v", err)
	}
	if from != 1 || to != 50 {
		t.Errorf("got [%d, %d], want [1, 50]", from, to)
	}
}

func TestComputeIDSweepRange_ExplicitBoth(t *testing.T) {
	from, to, err := computeIDSweepRange(10, 20, 1000, false, nil)
	if err != nil {
		t.Fatalf("computeIDSweepRange: %v", err)
	}
	if from != 10 || to != 20 {
		t.Errorf("got [%d, %d], want [10, 20]", from, to)
	}
}

func TestComputeIDSweepRange_Invalid(t *testing.T) {
	if _, _, err := computeIDSweepRange(20, 10, 1000, false, nil); err == nil {
		t.Fatal("expected error for from > to")
	}

	if _, _, err := computeIDSweepRange(0, 0, 0, false, nil); err == nil {
		t.Fatal("expected error for count = 0")
	}

	errMaxID := errors.New("db error")
	if _, _, err := computeIDSweepRange(0, 0, 100, false, func() (uint32, error) { return 0, errMaxID }); !errors.Is(err, errMaxID) {
		t.Fatalf("expected db error, got %v", err)
	}
}

func TestBuildIDSweepJobs(t *testing.T) {
	jobs := buildIDSweepJobs(1, 250, 100, "lodestone")
	if len(jobs) != 3 {
		t.Fatalf("jobs len = %d, want 3", len(jobs))
	}

	expectedRanges := [][2]uint32{
		{1, 100},
		{101, 200},
		{201, 250},
	}

	for i, j := range jobs {
		if j.Type != handler.EventIDSweep {
			t.Errorf("job[%d] type = %q, want %q", i, j.Type, handler.EventIDSweep)
		}
		var p handler.IDSweepPayload
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			t.Fatalf("job[%d] unmarshal: %v", i, err)
		}
		if p.From != expectedRanges[i][0] || p.To != expectedRanges[i][1] {
			t.Errorf("job[%d] range = [%d, %d], want [%d, %d]", i, p.From, p.To, expectedRanges[i][0], expectedRanges[i][1])
		}
		if p.Source != "lodestone" {
			t.Errorf("job[%d] source = %q, want lodestone", i, p.Source)
		}
	}
}

func TestPublishIDSweepCmd_FlagsRegistered(t *testing.T) {
	flags := publishIDSweepCmd.Flags()
	for _, name := range []string{
		"auto", "batch-size", "from", "to", "count", "chunk-size", "source",
		"fill-gaps", "daemon", "daemon-interval", "min-id",
	} {
		if flags.Lookup(name) == nil {
			t.Errorf("flag --%s not registered on publish id-sweep", name)
		}
	}
}

func TestBuildGapSweepJobs(t *testing.T) {
	gaps := [][2]uint32{
		{1, 50},
		{100, 250},
	}
	jobs := buildGapSweepJobs(gaps, 100, "auto")
	// Gap 1: [1, 50] -> 1 job
	// Gap 2: [100, 250] -> 2 jobs ([100, 199], [200, 250])
	// Total: 3 jobs
	if len(jobs) != 3 {
		t.Fatalf("jobs len = %d, want 3", len(jobs))
	}

	expected := [][2]uint32{
		{1, 50},
		{100, 199},
		{200, 250},
	}
	for i, j := range jobs {
		var p handler.IDSweepPayload
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			t.Fatalf("job[%d] unmarshal: %v", i, err)
		}
		if p.From != expected[i][0] || p.To != expected[i][1] {
			t.Errorf("job[%d] = [%d, %d], want [%d, %d]", i, p.From, p.To, expected[i][0], expected[i][1])
		}
		if p.Source != "auto" {
			t.Errorf("job[%d] source = %q, want auto", i, p.Source)
		}
	}
}

// errorQueue is a queue fake that fails on the Nth publish.
type errorQueue struct {
	failOn int // 1-based position to fail on
	calls  int
	jobs   []contract.QueueJob
}

func (q *errorQueue) Publish(_ context.Context, job contract.QueueJob) error {
	q.calls++
	if q.calls == q.failOn {
		return fmt.Errorf("broker nack on job %d", q.calls)
	}
	q.jobs = append(q.jobs, job)
	return nil
}

func (q *errorQueue) Consume(context.Context, []string, int, func(context.Context, contract.QueueJob) error) error {
	return nil
}
func (q *errorQueue) ConsumeFailed(context.Context, []string, int) error { return nil }
func (q *errorQueue) Close() error                                       { return nil }

type fakeIDSweepCursor struct {
	next         uint32
	maxID        uint32
	advanceCalls [][2]uint32
}

func (f *fakeIDSweepCursor) MaxID(context.Context) (uint32, error) {
	return f.maxID, nil
}

func (f *fakeIDSweepCursor) RewindIDSweepCursor(_ context.Context, expected, next uint32) error {
	if f.next == expected {
		f.next = next
	}
	return nil
}

func (f *fakeIDSweepCursor) IDSweepCursor(context.Context) (uint32, error) {
	return f.next, nil
}

func (f *fakeIDSweepCursor) AdvanceIDSweepCursor(_ context.Context, expected, next uint32) error {
	f.advanceCalls = append(f.advanceCalls, [2]uint32{expected, next})
	if f.next != expected {
		return fmt.Errorf("stale cursor: got %d, expected %d", f.next, expected)
	}
	f.next = next
	return nil
}

func TestPublishAutoIDSweep_AdvancesAcrossEmptyDiscoveryBatches(t *testing.T) {
	cursor := &fakeIDSweepCursor{next: 1584839, maxID: 1584838}
	q := &errorQueue{}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	for range 2 {
		if err := publishAutoIDSweep(context.Background(), cursor, q, logger, 550, 550, 10000, "auto"); err != nil {
			t.Fatalf("publishAutoIDSweep: %v", err)
		}
	}

	if cursor.next != 1585939 {
		t.Fatalf("cursor = %d, want 1585939", cursor.next)
	}
	if len(q.jobs) != 2 {
		t.Fatalf("published jobs = %d, want 2", len(q.jobs))
	}
	want := [][2]uint32{{1584839, 1585388}, {1585389, 1585938}}
	for i, job := range q.jobs {
		var payload handler.IDSweepPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.From != want[i][0] || payload.To != want[i][1] {
			t.Errorf("job %d range = [%d,%d], want [%d,%d]", i, payload.From, payload.To, want[i][0], want[i][1])
		}
	}
	for _, field := range []string{"from_id=1585389", "to_id=1585938", "next_id=1585939"} {
		if !strings.Contains(logs.String(), field) {
			t.Errorf("completion logs missing %q: %s", field, logs.String())
		}
	}
}

func TestPublishAutoIDSweep_PublishFailureDoesNotAdvanceCursor(t *testing.T) {
	cursor := &fakeIDSweepCursor{next: 1001, maxID: 1000}
	q := &errorQueue{failOn: 2}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	err := publishAutoIDSweep(context.Background(), cursor, q, logger, 300, 100, 10000, "auto")
	if err == nil {
		t.Fatal("expected publish error")
	}
	if cursor.next != 1001 || len(cursor.advanceCalls) != 0 {
		t.Fatalf("cursor advanced after partial publish: next=%d calls=%v", cursor.next, cursor.advanceCalls)
	}
}

func TestPublishAutoIDSweep_RejectsUint32Overflow(t *testing.T) {
	cursor := &fakeIDSweepCursor{next: ^uint32(0) - 4, maxID: ^uint32(0) - 5}
	err := publishAutoIDSweep(context.Background(), cursor, &errorQueue{}, slog.New(slog.NewTextHandler(io.Discard, nil)), 10, 1, 10000, "auto")
	if err == nil {
		t.Fatal("expected overflow error")
	}
}

func TestPublishAutoIDSweep_ClampsBatchAtFrontier(t *testing.T) {
	cursor := &fakeIDSweepCursor{next: 1095, maxID: 1000}
	q := &errorQueue{}

	if err := publishAutoIDSweep(context.Background(), cursor, q, slog.New(slog.NewTextHandler(io.Discard, nil)), 50, 1, 100, "auto"); err != nil {
		t.Fatalf("publishAutoIDSweep: %v", err)
	}

	if len(q.jobs) != 6 {
		t.Fatalf("published jobs = %d, want 6 (IDs 1095..1100)", len(q.jobs))
	}
	if cursor.next != 1101 {
		t.Fatalf("cursor = %d, want 1101", cursor.next)
	}
}

func TestPublishAutoIDSweep_RewindsToNewestCharacterPastFrontier(t *testing.T) {
	cursor := &fakeIDSweepCursor{next: 67834638, maxID: 62692885}
	q := &errorQueue{}

	if err := publishAutoIDSweep(context.Background(), cursor, q, slog.New(slog.NewTextHandler(io.Discard, nil)), 50, 50, 10000, "auto"); err != nil {
		t.Fatalf("publishAutoIDSweep: %v", err)
	}

	if len(q.jobs) != 1 {
		t.Fatalf("published jobs = %d, want 1", len(q.jobs))
	}
	var payload handler.IDSweepPayload
	if err := json.Unmarshal(q.jobs[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.From != 62692886 || payload.To != 62692935 {
		t.Errorf("range = [%d,%d], want [62692886,62692935]", payload.From, payload.To)
	}
	if cursor.next != 62692936 {
		t.Fatalf("cursor = %d, want 62692936", cursor.next)
	}
}

func TestPublishAllStopsOnFirstUnconfirmedJob(t *testing.T) {
	q := &errorQueue{failOn: 3}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	jobs := []contract.QueueJob{
		{Type: "id-sweep", Payload: []byte(`{"from":1,"to":100}`)},
		{Type: "id-sweep", Payload: []byte(`{"from":101,"to":200}`)},
		{Type: "id-sweep", Payload: []byte(`{"from":201,"to":300}`)},
		{Type: "id-sweep", Payload: []byte(`{"from":301,"to":400}`)},
	}

	confirmed, err := publishAll(q, logger, context.Background(), jobs)
	if err == nil {
		t.Fatal("expected error from publishAll")
	}
	if confirmed != 2 {
		t.Errorf("confirmed = %d, want 2", confirmed)
	}
	if !strings.Contains(err.Error(), "id-sweep") {
		t.Errorf("error should contain event type: %v", err)
	}
	if !strings.Contains(err.Error(), "job 3 of 4") {
		t.Errorf("error should contain job position: %v", err)
	}
	if len(q.jobs) != 2 {
		t.Errorf("only %d jobs should have been published, got %d", 2, len(q.jobs))
	}
}

func TestPublishIDSweep_AutoAndGapBatchGeneration(t *testing.T) {
	// Test Auto-Range generation for CronJob
	maxID := uint32(124000)
	maxIDFunc := func() (uint32, error) {
		return maxID, nil
	}

	from, to, err := computeIDSweepRange(0, 0, 1000, true, maxIDFunc)
	if err != nil {
		t.Fatalf("computeIDSweepRange auto failed: %v", err)
	}
	if from != 124001 || to != 125000 {
		t.Fatalf("expected range [124001, 125000], got [%d, %d]", from, to)
	}

	jobs := buildIDSweepJobs(from, to, 100, "auto")
	if len(jobs) != 10 {
		t.Fatalf("expected 10 jobs for 1000 IDs with chunk size 100, got %d", len(jobs))
	}

	for idx, j := range jobs {
		var p handler.IDSweepPayload
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			t.Fatalf("job %d payload unmarshal error: %v", idx, err)
		}
		expectedFrom := 124001 + uint32(idx*100)
		expectedTo := expectedFrom + 99
		if p.From != expectedFrom || p.To != expectedTo {
			t.Errorf("job %d range = [%d, %d], want [%d, %d]", idx, p.From, p.To, expectedFrom, expectedTo)
		}
	}

	// Test Gap-fill generation
	gaps := [][2]uint32{
		{50, 150},  // 101 IDs -> 2 chunks: [50, 149], [150, 150]
		{500, 550}, // 51 IDs -> 1 chunk: [500, 550]
	}
	gapJobs := buildGapSweepJobs(gaps, 100, "lodestone")
	if len(gapJobs) != 3 {
		t.Fatalf("expected 3 gap jobs, got %d", len(gapJobs))
	}
}

func TestCharacterCensusCutoff(t *testing.T) {
	now := time.Date(2025, 8, 17, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		olderThan time.Duration
		wantZero  bool
	}{
		{"positive duration", 720 * time.Hour, false},
		{"zero duration", 0, true},
		{"negative duration", -1 * time.Hour, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := characterCensusCutoff(now, tc.olderThan)
			if tc.wantZero {
				if !got.IsZero() {
					t.Errorf("got %v, want zero time", got)
				}
			} else {
				if got.IsZero() {
					t.Fatal("got zero time, want non-zero")
				}
				want := now.UTC().Add(-tc.olderThan)
				if !got.Equal(want) {
					t.Errorf("got %v, want %v", got, want)
				}
			}
		})
	}
}

func TestPublishCharacterCensusCmd_FlagsRegistered(t *testing.T) {
	flags := publishCharacterCensusCmd.Flags()
	for _, name := range []string{"older-than", "limit"} {
		if flags.Lookup(name) == nil {
			t.Errorf("flag --%s not registered on publish character-census", name)
		}
	}

	// Verify --older-than defaults to 0 (disabled).
	olderThan, err := flags.GetDuration("older-than")
	if err != nil {
		t.Fatalf("GetDuration older-than: %v", err)
	}
	if olderThan != 0 {
		t.Errorf("older-than default = %v, want 0", olderThan)
	}

	// Verify --limit defaults to 1000.
	limit, err := flags.GetInt("limit")
	if err != nil {
		t.Fatalf("GetInt limit: %v", err)
	}
	if limit != 1000 {
		t.Errorf("limit default = %d, want 1000", limit)
	}
}

func seedGapCharacters(t *testing.T) *mockrepo.CharacterRepository {
	t.Helper()
	repo := mockrepo.NewCharacterFake()
	now := time.Now().UTC()
	for _, id := range []uint32{3, 4, 8, 15} {
		if err := repo.Upsert(context.Background(), contract.CharacterRecord{ID: id, Name: "C", World: "W", Race: "Hyur", FirstSeenAt: now}, nil); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

func decodeIDSweepRanges(t *testing.T, jobs []contract.QueueJob) [][2]uint32 {
	t.Helper()
	ranges := make([][2]uint32, 0, len(jobs))
	for _, j := range jobs {
		var p handler.IDSweepPayload
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			t.Fatalf("payload unmarshal: %v", err)
		}
		ranges = append(ranges, [2]uint32{p.From, p.To})
	}
	return ranges
}

func TestPublishFillGapsIDSweep_CapsJobsAndAdvancesCursor(t *testing.T) {
	repo := seedGapCharacters(t)
	q := &errorQueue{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Gaps after 0: [1,2], [5,7], [9,14]. chunk 2 → [1,2],[5,6],[7,7],[9,10],...
	// count caps at 2 jobs; cursor must land on the end of job 2 (id 6).
	if err := publishFillGapsIDSweep(context.Background(), repo, q, logger, 2, 2, "auto", 0); err != nil {
		t.Fatalf("publishFillGapsIDSweep: %v", err)
	}

	got := decodeIDSweepRanges(t, q.jobs)
	want := [][2]uint32{{1, 2}, {5, 6}}
	if len(got) != len(want) {
		t.Fatalf("jobs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("job[%d] = %v, want %v", i, got[i], want[i])
		}
	}

	cursor, err := repo.FillGapsCursor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cursor != 6 {
		t.Fatalf("cursor = %d, want 6 (end of last published job)", cursor)
	}
}

func TestPublishFillGapsIDSweep_ResumesFromCursor(t *testing.T) {
	repo := seedGapCharacters(t)
	q := &errorQueue{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// First run consumes ids 1-6; second run resumes at 7.
	if err := repo.AdvanceFillGapsCursor(context.Background(), 0, 6); err != nil {
		t.Fatal(err)
	}
	if err := publishFillGapsIDSweep(context.Background(), repo, q, logger, 10, 2, "auto", 0); err != nil {
		t.Fatalf("publishFillGapsIDSweep: %v", err)
	}

	got := decodeIDSweepRanges(t, q.jobs)
	want := [][2]uint32{{7, 7}, {9, 10}, {11, 12}, {13, 14}}
	if len(got) != len(want) {
		t.Fatalf("jobs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("job[%d] = %v, want %v", i, got[i], want[i])
		}
	}

	cursor, _ := repo.FillGapsCursor(context.Background())
	if cursor != 14 {
		t.Fatalf("cursor = %d, want 14", cursor)
	}
}

func TestPublishFillGapsIDSweep_WrapsAtMaxID(t *testing.T) {
	repo := seedGapCharacters(t)
	q := &errorQueue{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Cursor beyond MaxID (15) → reset to 0 and scan from the beginning.
	if err := repo.AdvanceFillGapsCursor(context.Background(), 0, 20); err != nil {
		t.Fatal(err)
	}
	if err := publishFillGapsIDSweep(context.Background(), repo, q, logger, 10, 2, "auto", 0); err != nil {
		t.Fatalf("publishFillGapsIDSweep: %v", err)
	}

	if len(q.jobs) == 0 {
		t.Fatal("expected jobs after wrap-around")
	}
	cursor, _ := repo.FillGapsCursor(context.Background())
	if cursor != 14 {
		t.Fatalf("cursor = %d, want 14 (highest gap end)", cursor)
	}
}

func TestPublishFillGapsIDSweep_WrapsWhenNoGapsRemainAfterCursor(t *testing.T) {
	repo := seedGapCharacters(t)
	q := &errorQueue{}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	// Cursor 14 sits past the last gap ([9,14]) but below MaxID (15): no gaps
	// remain after it, so the run wraps to 0 and publishes the lowest gaps.
	if err := repo.AdvanceFillGapsCursor(context.Background(), 0, 14); err != nil {
		t.Fatal(err)
	}
	if err := publishFillGapsIDSweep(context.Background(), repo, q, logger, 2, 2, "auto", 0); err != nil {
		t.Fatalf("publishFillGapsIDSweep: %v", err)
	}

	got := decodeIDSweepRanges(t, q.jobs)
	want := [][2]uint32{{1, 2}, {5, 6}}
	if len(got) != len(want) {
		t.Fatalf("jobs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("job[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	cursor, _ := repo.FillGapsCursor(context.Background())
	if cursor != 6 {
		t.Fatalf("cursor = %d, want 6 (end of last published job after wrap)", cursor)
	}
	if !strings.Contains(logs.String(), "wrapped=true") {
		t.Errorf("log does not record the wrap: %s", logs.String())
	}
}

func TestPublishFillGapsIDSweep_ManualMinID_DoesNotTouchCursor(t *testing.T) {
	repo := seedGapCharacters(t)
	q := &errorQueue{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := publishFillGapsIDSweep(context.Background(), repo, q, logger, 10, 2, "auto", 5); err != nil {
		t.Fatalf("publishFillGapsIDSweep: %v", err)
	}

	got := decodeIDSweepRanges(t, q.jobs)
	want := [][2]uint32{{6, 7}, {9, 10}, {11, 12}, {13, 14}}
	if len(got) != len(want) {
		t.Fatalf("jobs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("job[%d] = %v, want %v", i, got[i], want[i])
		}
	}

	cursor, _ := repo.FillGapsCursor(context.Background())
	if cursor != 0 {
		t.Fatalf("cursor = %d, want 0 (manual mode must not advance it)", cursor)
	}
}

func TestPublishFillGapsIDSweep_PublishFailureAdvancesPublishedPrefix(t *testing.T) {
	repo := seedGapCharacters(t)
	q := &errorQueue{failOn: 2}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	err := publishFillGapsIDSweep(context.Background(), repo, q, logger, 4, 2, "auto", 0)
	if err == nil {
		t.Fatal("expected publish error")
	}

	cursor, _ := repo.FillGapsCursor(context.Background())
	if cursor != 2 {
		t.Fatalf("cursor = %d, want 2 (end of the single published job)", cursor)
	}
}

func TestPublishFillGapsIDSweep_NoGaps(t *testing.T) {
	repo := mockrepo.NewCharacterFake()
	now := time.Now().UTC()
	for _, id := range []uint32{1, 2, 3} {
		if err := repo.Upsert(context.Background(), contract.CharacterRecord{ID: id, Name: "C", World: "W", Race: "Hyur", FirstSeenAt: now}, nil); err != nil {
			t.Fatal(err)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := publishFillGapsIDSweep(context.Background(), repo, &errorQueue{}, logger, 5, 2, "auto", 0); err != nil {
		t.Fatalf("publishFillGapsIDSweep: %v", err)
	}

	cursor, _ := repo.FillGapsCursor(context.Background())
	if cursor != 0 {
		t.Fatalf("cursor = %d, want 0 (nothing to publish)", cursor)
	}
}

func TestPublishFillGapsIDSweep_ManualMinIDPastLastGap_DoesNotWrap(t *testing.T) {
	repo := seedGapCharacters(t)
	q := &errorQueue{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := publishFillGapsIDSweep(context.Background(), repo, q, logger, 10, 2, "auto", 14); err != nil {
		t.Fatalf("publishFillGapsIDSweep: %v", err)
	}
	if len(q.jobs) != 0 {
		t.Fatalf("jobs = %v, want none (manual mode never wraps)", decodeIDSweepRanges(t, q.jobs))
	}
	cursor, _ := repo.FillGapsCursor(context.Background())
	if cursor != 0 {
		t.Fatalf("cursor = %d, want 0 (manual mode must not touch it)", cursor)
	}
}

func TestPublishFillGapsIDSweep_NoGapsWithCursorAboveZero_WrapsAndPublishesNothing(t *testing.T) {
	repo := mockrepo.NewCharacterFake()
	now := time.Now().UTC()
	for _, id := range []uint32{1, 2, 3} {
		if err := repo.Upsert(context.Background(), contract.CharacterRecord{ID: id, Name: "C", World: "W", Race: "Hyur", FirstSeenAt: now}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.AdvanceFillGapsCursor(context.Background(), 0, 2); err != nil {
		t.Fatal(err)
	}
	q := &errorQueue{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := publishFillGapsIDSweep(context.Background(), repo, q, logger, 5, 2, "auto", 0); err != nil {
		t.Fatalf("publishFillGapsIDSweep: %v", err)
	}
	if len(q.jobs) != 0 {
		t.Fatalf("jobs = %v, want none", decodeIDSweepRanges(t, q.jobs))
	}
	cursor, _ := repo.FillGapsCursor(context.Background())
	if cursor != 0 {
		t.Fatalf("cursor = %d, want 0 (wrapped, nothing to publish)", cursor)
	}
}

func TestPublishFillGapsIDSweep_RejectsZeroCount(t *testing.T) {
	repo := seedGapCharacters(t)
	err := publishFillGapsIDSweep(context.Background(), repo, &errorQueue{}, slog.New(slog.NewTextHandler(io.Discard, nil)), 0, 2, "auto", 0)
	if err == nil {
		t.Fatal("expected error for count = 0")
	}
}
