package repository_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// jobsByID indexes a character's stored jobs by class job ID.
func jobsByID(t *testing.T, repo contract.CharacterRepository, id uint32) map[uint8]contract.ClassJobRecord {
	t.Helper()
	jobs, err := repo.GetJobs(context.Background(), id)
	if err != nil {
		t.Fatalf("GetJobs(%d): %v", id, err)
	}
	out := make(map[uint8]contract.ClassJobRecord, len(jobs))
	for _, j := range jobs {
		out[j.ClassJobID] = j
	}
	return out
}

// logJobDeletes records every row deleted from character_jobs so tests can
// assert that an upsert only deletes jobs that are no longer present.
func logJobDeletes(t *testing.T, driver contract.DatabaseDriver) {
	t.Helper()
	mustExec(t, driver, `CREATE TABLE job_delete_log (class_job_id INTEGER NOT NULL)`)
	mustExec(t, driver, `CREATE FUNCTION log_job_delete() RETURNS trigger AS $$
		BEGIN INSERT INTO job_delete_log VALUES (OLD.class_job_id); RETURN OLD; END
		$$ LANGUAGE plpgsql`)
	mustExec(t, driver, `CREATE TRIGGER character_jobs_delete_log AFTER DELETE ON character_jobs
		FOR EACH ROW EXECUTE FUNCTION log_job_delete()`)
}

func deletedJobIDs(t *testing.T, driver contract.DatabaseDriver) []int {
	t.Helper()
	db, err := driver.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(context.Background(), `SELECT class_job_id FROM job_delete_log ORDER BY class_job_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestCharacterRepository_UpsertJobs(t *testing.T) {
	const charID = 3001
	rec := contract.CharacterRecord{ID: charID, Name: "Jobber", World: "Balmung", FirstSeenAt: time.Now().UTC().Truncate(time.Millisecond)}
	job := func(id uint8, name string, level uint8, exp uint32) contract.ClassJobRecord {
		return contract.ClassJobRecord{CharacterID: charID, ClassJobID: id, Name: name, Level: level, ExpLevel: exp}
	}

	tests := []struct {
		name        string
		first       []contract.ClassJobRecord
		second      []contract.ClassJobRecord
		want        map[uint8]contract.ClassJobRecord
		wantDeleted []int
	}{
		{
			name:   "new jobs are inserted",
			second: []contract.ClassJobRecord{job(1, "Gladiator", 50, 100), job(16, "Miner", 90, 5000)},
			want: map[uint8]contract.ClassJobRecord{
				1:  job(1, "Gladiator", 50, 100),
				16: job(16, "Miner", 90, 5000),
			},
		},
		{
			name:   "unchanged jobs stay and nothing is deleted",
			first:  []contract.ClassJobRecord{job(1, "Gladiator", 50, 100), job(16, "Miner", 90, 5000)},
			second: []contract.ClassJobRecord{job(1, "Gladiator", 50, 100), job(16, "Miner", 90, 5000)},
			want: map[uint8]contract.ClassJobRecord{
				1:  job(1, "Gladiator", 50, 100),
				16: job(16, "Miner", 90, 5000),
			},
		},
		{
			name:   "changed jobs are updated in place",
			first:  []contract.ClassJobRecord{job(1, "Gladiator", 50, 100), job(16, "Miner", 90, 5000)},
			second: []contract.ClassJobRecord{job(1, "Paladin", 60, 250), job(16, "Miner", 90, 5000)},
			want: map[uint8]contract.ClassJobRecord{
				1:  job(1, "Paladin", 60, 250),
				16: job(16, "Miner", 90, 5000),
			},
		},
		{
			name:        "shrunk list deletes only the missing jobs",
			first:       []contract.ClassJobRecord{job(1, "Gladiator", 50, 100), job(16, "Miner", 90, 5000), job(19, "Paladin", 100, 0)},
			second:      []contract.ClassJobRecord{job(16, "Miner", 90, 5000)},
			want:        map[uint8]contract.ClassJobRecord{16: job(16, "Miner", 90, 5000)},
			wantDeleted: []int{1, 19},
		},
		{
			name:        "empty list removes every job",
			first:       []contract.ClassJobRecord{job(1, "Gladiator", 50, 100), job(16, "Miner", 90, 5000)},
			second:      nil,
			want:        map[uint8]contract.ClassJobRecord{},
			wantDeleted: []int{1, 16},
		},
		{
			name:   "grown, changed and shrunk in one upsert",
			first:  []contract.ClassJobRecord{job(1, "Gladiator", 50, 100), job(16, "Miner", 90, 5000)},
			second: []contract.ClassJobRecord{job(16, "Miner", 91, 6000), job(30, "Ninja", 80, 7)},
			want: map[uint8]contract.ClassJobRecord{
				16: job(16, "Miner", 91, 6000),
				30: job(30, "Ninja", 80, 7),
			},
			wantDeleted: []int{1},
		},
		{
			name:   "duplicate class job id keeps the last entry",
			second: []contract.ClassJobRecord{job(1, "Gladiator", 50, 100), job(1, "Paladin", 60, 250)},
			want:   map[uint8]contract.ClassJobRecord{1: job(1, "Paladin", 60, 250)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := newTestDriver(t)
			repo := repository.NewCharacterRepository(driver)
			ctx := context.Background()

			// A second character's jobs must never be touched.
			other := contract.CharacterRecord{ID: 3002, Name: "Bystander", World: "Balmung", FirstSeenAt: rec.FirstSeenAt}
			otherJob := contract.ClassJobRecord{CharacterID: 3002, ClassJobID: 1, Name: "Gladiator", Level: 12, ExpLevel: 3}
			if err := repo.Upsert(ctx, other, []contract.ClassJobRecord{otherJob}); err != nil {
				t.Fatal(err)
			}

			if tt.first != nil {
				if err := repo.Upsert(ctx, rec, tt.first); err != nil {
					t.Fatalf("first Upsert: %v", err)
				}
			}
			logJobDeletes(t, driver)
			if err := repo.Upsert(ctx, rec, tt.second); err != nil {
				t.Fatalf("second Upsert: %v", err)
			}

			got := jobsByID(t, repo, charID)
			if len(got) != len(tt.want) {
				t.Fatalf("jobs = %+v, want %+v", got, tt.want)
			}
			for id, w := range tt.want {
				if got[id] != w {
					t.Errorf("job %d = %+v, want %+v", id, got[id], w)
				}
			}
			if gotDeleted := deletedJobIDs(t, driver); !slices.Equal(gotDeleted, tt.wantDeleted) {
				t.Errorf("deleted job rows = %v, want %v", gotDeleted, tt.wantDeleted)
			}
			if bystander := jobsByID(t, repo, 3002); bystander[1] != otherJob {
				t.Errorf("other character's jobs changed: %+v", bystander)
			}
		})
	}
}
