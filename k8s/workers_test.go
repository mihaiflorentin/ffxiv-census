package k8s

import (
	"os"
	"strings"
	"testing"
)

// TestProxyWorkersPostgresPoolOverrides guards the per-worker Postgres pool
// overrides. Instance-level env replaces workers.defaults.env wholesale, so
// each proxy worker must also re-declare the default queue env vars.
func TestProxyWorkersPostgresPoolOverrides(t *testing.T) {
	values, err := os.ReadFile("values.yaml")
	if err != nil {
		t.Fatalf("read Helm values: %v", err)
	}
	src := string(values)

	consumers := []string{"proxy-id-sweep", "proxy-character-census", "proxy-achievement-census"}
	for _, name := range consumers {
		section := workerSection(t, src, name)
		assertWorkerEnvValue(t, name, section, "POSTGRES_MAX_OPEN_CONNS", "3")
		assertWorkerEnvValue(t, name, section, "POSTGRES_MAX_IDLE_CONNS", "2")
		assertWorkerKeepsQueueEnv(t, name, section)
	}

	scan := workerSection(t, src, "proxy-scan")
	assertWorkerEnvValue(t, "proxy-scan", scan, "POSTGRES_MAX_OPEN_CONNS", "2")
	assertWorkerEnvValue(t, "proxy-scan", scan, "POSTGRES_MAX_IDLE_CONNS", "1")
	assertWorkerKeepsQueueEnv(t, "proxy-scan", scan)
}

// TestWorkerDefaultsPostgresPool guards the pool inherited by every worker
// instance that declares no env of its own (census-consumer).
func TestWorkerDefaultsPostgresPool(t *testing.T) {
	values, err := os.ReadFile("values.yaml")
	if err != nil {
		t.Fatalf("read Helm values: %v", err)
	}
	src := string(values)
	start := strings.Index(src, "\nworkers:\n  defaults:\n")
	end := strings.Index(src, "\n  instances:\n")
	if start < 0 || end < start {
		t.Fatal("workers.defaults block not found in values.yaml")
	}
	defaults := src[start:end]
	assertWorkerEnvValue(t, "workers.defaults", defaults, "POSTGRES_MAX_OPEN_CONNS", "3")
	assertWorkerEnvValue(t, "workers.defaults", defaults, "POSTGRES_MAX_IDLE_CONNS", "2")

	if strings.Contains(workerSection(t, src, "census-consumer"), "    env:") {
		t.Fatal("census-consumer declares its own env and would not inherit the default pool")
	}
}

// TestProxyScanResourcesOverride guards the resource requests/limits nesting:
// requests and limits must sit under resources, not beside it.
func TestProxyScanResourcesOverride(t *testing.T) {
	values, err := os.ReadFile("values.yaml")
	if err != nil {
		t.Fatalf("read Helm values: %v", err)
	}
	section := workerSection(t, string(values), "proxy-scan") + "\n"

	for _, want := range []string{
		"      resources:\n        requests:\n",
		"        limits:\n          memory: 1Gi\n          cpu: 1000m\n",
	} {
		if !strings.Contains(section, want) {
			t.Fatalf("proxy-scan resources block malformed, want nested block containing %q", want)
		}
	}
}

// workerSection extracts the values.yaml block for one workers.instances entry,
// ending at the next sibling entry or the next comment/top-level key.
func workerSection(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "    - name: "+name+"\n")
	if start < 0 {
		t.Fatalf("worker %q not found in values.yaml", name)
	}
	rest := src[start+len("    - name: "+name+"\n"):]
	end := len(src)
	for _, marker := range []string{"\n    - name: ", "\n    # ---", "\ncronjobs:"} {
		if i := strings.Index(rest, marker); i >= 0 {
			if abs := start + len("    - name: "+name+"\n") + i; abs < end {
				end = abs
			}
		}
	}
	return src[start:end]
}

func assertWorkerEnvValue(t *testing.T, worker, section, envName, wantValue string) {
	t.Helper()
	marker := "- name: " + envName
	idx := strings.Index(section, marker)
	if idx < 0 {
		t.Fatalf("worker %s: missing env %s", worker, envName)
	}
	rest := section[idx+len(marker):]
	if !strings.HasPrefix(strings.TrimLeft(rest, "\n "), "value: \""+wantValue+"\"") {
		t.Fatalf("worker %s: env %s value != %q, got %.40q", worker, envName, wantValue, strings.TrimSpace(rest))
	}
}

// TestMetricsPortOnlyOnConsumeWorkers guards the worker metrics endpoint: only
// the `consume` commands accept --metrics-port, so `proxy scan` workers must
// not declare one.
func TestMetricsPortOnlyOnConsumeWorkers(t *testing.T) {
	values, err := os.ReadFile("values.yaml")
	if err != nil {
		t.Fatalf("read Helm values: %v", err)
	}
	src := string(values)

	for _, name := range []string{"census-consumer", "proxy-id-sweep", "proxy-character-census", "proxy-achievement-census"} {
		if !strings.Contains(workerSection(t, src, name), "      metricsPort: 9090\n") {
			t.Fatalf("worker %s: missing metricsPort", name)
		}
	}
	for _, name := range []string{"proxy-scan"} {
		if strings.Contains(workerSection(t, src, name), "metricsPort") {
			t.Fatalf("worker %s: proxy commands have no --metrics-port flag", name)
		}
	}
}

func assertWorkerKeepsQueueEnv(t *testing.T, worker, section string) {
	t.Helper()
	for _, envName := range []string{"QUEUE_MAX_ATTEMPTS", "RABBITMQ_URL"} {
		if !strings.Contains(section, "- name: "+envName+"\n") {
			t.Fatalf("worker %s: env %s missing (instance env replaces workers.defaults.env)", worker, envName)
		}
	}
}
