package k8s

import (
	"os"
	"strings"
	"testing"
)

func cronjobSection(t *testing.T, src, name string) string {
	t.Helper()
	marker := "    - name: " + name + "\n"
	start := strings.Index(src, marker)
	if start < 0 {
		t.Fatalf("cronjob %s missing from values.yaml", name)
	}
	rest := src[start+len(marker):]
	if end := strings.Index(rest, "\n    - name: "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

func TestUIStatsRefreshRunsNightlyFullAndHalfHourlyIncremental(t *testing.T) {
	values, err := os.ReadFile("values.yaml")
	if err != nil {
		t.Fatalf("read Helm values: %v", err)
	}
	src := string(values)

	if strings.Contains(src, "    - name: refresh-ui-stats\n") {
		t.Fatal("the hourly refresh-ui-stats CronJob must be replaced by the full and incremental ones")
	}
	for name, want := range map[string]struct{ schedule, mode string }{
		"refresh-ui-stats-full":        {`schedule: "0 1 * * *"`, "--mode=full"},
		"refresh-ui-stats-incremental": {`schedule: "*/30 * * * *"`, "--mode=incremental"},
	} {
		section := cronjobSection(t, src, name)
		for _, needle := range []string{want.schedule, want.mode, "concurrencyPolicy: Forbid"} {
			if !strings.Contains(section, needle) {
				t.Fatalf("%s: missing %q", name, needle)
			}
		}
	}
}
