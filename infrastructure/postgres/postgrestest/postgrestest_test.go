package postgrestest

import "testing"

func TestPostgresPortDefaultsTo5433(t *testing.T) {
	t.Setenv("TEST_POSTGRES_PORT", "")
	if got := postgresPort(); got != 5433 {
		t.Fatalf("default port = %d, want 5433 (a developer's 5432 server is never a fallback target)", got)
	}

	t.Setenv("TEST_POSTGRES_PORT", "5555")
	if got := postgresPort(); got != 5555 {
		t.Fatalf("explicit port = %d, want 5555", got)
	}

	t.Setenv("TEST_POSTGRES_PORT", "5432")
	if got := postgresPort(); got != 5432 {
		t.Fatalf("explicit port = %d, want 5432 (explicit env still wins)", got)
	}

	for _, garbage := range []string{"nope", "0", "-1"} {
		t.Setenv("TEST_POSTGRES_PORT", garbage)
		if got := postgresPort(); got != 5433 {
			t.Fatalf("TEST_POSTGRES_PORT=%q: port = %d, want default 5433", garbage, got)
		}
	}
}
