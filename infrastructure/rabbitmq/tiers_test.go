package rabbitmq

import (
	"errors"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestTierQueueName(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{5 * time.Second, "census.id-sweep.delay.5s"},
		{30 * time.Second, "census.id-sweep.delay.30s"},
		{5 * time.Minute, "census.id-sweep.delay.5m"},
		{time.Hour, "census.id-sweep.delay.1h"},
		{90 * time.Second, "census.id-sweep.delay.90s"},
		{1500 * time.Millisecond, "census.id-sweep.delay.1500ms"},
	}
	for _, tt := range tests {
		if got := tierQueueFor("id-sweep", tt.d); got != tt.want {
			t.Errorf("tierQueueFor(%s) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestTierQueueArgsExpireIntoTheMainQueue(t *testing.T) {
	args := tierQueueArgs("character-census", 30*time.Second)
	want := amqp.Table{
		"x-message-ttl":             int64(30000),
		"x-dead-letter-exchange":    "census",
		"x-dead-letter-routing-key": "character-census",
	}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for k, v := range want {
		if args[k] != v {
			t.Errorf("args[%q] = %v (%T), want %v (%T)", k, args[k], args[k], v, v)
		}
	}
}

func TestLegacyFailedQueueKeepsItsOriginalArguments(t *testing.T) {
	// Redeclaring census.<type>.failed with other arguments would fail the
	// whole topology with PRECONDITION_FAILED, so these must never change.
	args := legacyFailedArgs("id-sweep")
	if len(args) != 2 || args["x-dead-letter-exchange"] != "census" || args["x-dead-letter-routing-key"] != "id-sweep" {
		t.Fatalf("legacy failed args = %v", args)
	}
}

func TestTopologyDeclaresEveryQueueOncePerEventType(t *testing.T) {
	tiers := contract.DefaultRetryTiers()
	specs := topology(tiers)

	seen := map[string]queueSpec{}
	for _, s := range specs {
		if _, dup := seen[s.name]; dup {
			t.Fatalf("queue %s declared twice", s.name)
		}
		seen[s.name] = s
	}
	for _, et := range eventTypes() {
		main, ok := seen["census."+et]
		if !ok || main.bindKey != et || main.args != nil {
			t.Errorf("main queue for %s = %+v", et, main)
		}
		if f, ok := seen["census."+et+".failed"]; !ok || f.bindKey != "" || f.args["x-dead-letter-routing-key"] != et {
			t.Errorf("legacy failed queue for %s = %+v", et, f)
		}
		if d, ok := seen["census."+et+".dead"]; !ok || d.args != nil {
			t.Errorf("dead queue for %s = %+v", et, d)
		}
		for _, delay := range tiers {
			tq, ok := seen[tierQueueFor(et, delay)]
			if !ok || tq.bindKey != "" || tq.args["x-message-ttl"] != delay.Milliseconds() {
				t.Errorf("tier queue %s/%s = %+v", et, delay, tq)
			}
		}
	}
	if want := len(eventTypes()) * (3 + len(tiers)); len(specs) != want {
		t.Fatalf("topology has %d queues, want %d", len(specs), want)
	}
}

func TestTopologyIsStableAcrossCalls(t *testing.T) {
	a, b := topology(contract.DefaultRetryTiers()), topology(contract.DefaultRetryTiers())
	if len(a) != len(b) {
		t.Fatal("topology length differs between calls")
	}
	for i := range a {
		if a[i].name != b[i].name {
			t.Fatalf("queue %d: %s != %s", i, a[i].name, b[i].name)
		}
	}
}

func TestValidateTiers(t *testing.T) {
	tests := []struct {
		name    string
		tiers   []time.Duration
		wantErr bool
	}{
		{"defaults", contract.DefaultRetryTiers(), false},
		{"single tier", []time.Duration{time.Second}, false},
		{"empty", nil, true},
		{"zero delay", []time.Duration{0, time.Second}, true},
		{"negative delay", []time.Duration{-time.Second}, true},
		{"not ascending", []time.Duration{time.Minute, time.Second}, true},
		{"duplicate", []time.Duration{time.Second, time.Second}, true},
		{"sub-millisecond", []time.Duration{time.Microsecond}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateTiers(tt.tiers); (err != nil) != tt.wantErr {
				t.Fatalf("validateTiers(%v) = %v, wantErr %v", tt.tiers, err, tt.wantErr)
			}
		})
	}
}

func TestNewRejectsInvalidTiersBeforeDialling(t *testing.T) {
	_, err := New("amqp://127.0.0.1:1/", nil, 5, WithRetryTiers([]time.Duration{time.Minute, time.Second}))
	if !errors.Is(err, ErrInvalidRetryTiers) {
		t.Fatalf("New error = %v, want ErrInvalidRetryTiers", err)
	}
}

func TestNewRejectsAProxyFailureCapBelowOne(t *testing.T) {
	_, err := New("amqp://127.0.0.1:1/", nil, 5, WithProxyFailureCap(0))
	if err == nil || !strings.Contains(err.Error(), "proxy failure cap") {
		t.Fatalf("New error = %v, want a proxy failure cap error", err)
	}
}

func TestDecideFailureCountsProxyFailuresPastTheCap(t *testing.T) {
	q := &Queue{maxAttempts: 50, proxyFailureCap: 2, tiers: contract.DefaultRetryTiers()}
	proxyErr := &contract.ProxyCheckError{Kind: contract.CheckTarget, Challenge: true}

	d := q.decideFailure(amqp.Table{headerProxyFailures: int32(1)}, proxyErr)
	if !d.ProxyFault || d.Attempts != 0 {
		t.Fatalf("below the cap: %+v, want an uncounted proxy fault", d)
	}
	d = q.decideFailure(amqp.Table{headerProxyFailures: int32(2), headerAttempts: int32(4)}, proxyErr)
	if d.ProxyFault || d.Attempts != 5 {
		t.Fatalf("at the cap: %+v, want the failure counted as attempt 5", d)
	}
}

func TestRetryHeadersCarryTheDecision(t *testing.T) {
	prev := amqp.Table{
		headerAttempts:        int32(2),
		headerProxyFailures:   int32(4),
		headerNotBefore:       int64(123),
		"x-death":             []interface{}{amqp.Table{"count": int64(3)}},
		"x-first-death-queue": "census.id-sweep.delay.5s",
		"trace":               "keep-me",
	}

	t.Run("job failure raises attempts only", func(t *testing.T) {
		got := retryHeaders(prev, contract.RetryDecision{Attempts: 3})
		if got[headerAttempts] != int32(3) || got[headerProxyFailures] != int32(4) {
			t.Fatalf("headers = %v", got)
		}
	})
	t.Run("proxy failure keeps attempts and counts the fault", func(t *testing.T) {
		got := retryHeaders(prev, contract.RetryDecision{Attempts: 2, ProxyFault: true})
		if got[headerAttempts] != int32(2) || got[headerProxyFailures] != int32(5) {
			t.Fatalf("headers = %v", got)
		}
	})
	t.Run("broker bookkeeping and the old not-before are dropped", func(t *testing.T) {
		got := retryHeaders(prev, contract.RetryDecision{Attempts: 3})
		for _, k := range []string{"x-death", "x-first-death-queue", headerNotBefore} {
			if _, ok := got[k]; ok {
				t.Errorf("header %s must not be republished", k)
			}
		}
		if got["trace"] != "keep-me" {
			t.Error("unrelated headers must be preserved")
		}
	})
	t.Run("input table is not modified", func(t *testing.T) {
		if prev[headerAttempts] != int32(2) {
			t.Fatal("retryHeaders mutated its input")
		}
	})
	t.Run("nil headers", func(t *testing.T) {
		got := retryHeaders(nil, contract.RetryDecision{Attempts: 1})
		if got[headerAttempts] != int32(1) {
			t.Fatalf("headers = %v", got)
		}
	})
}

func TestGetProxyFailures(t *testing.T) {
	if getProxyFailures(nil) != 0 || getProxyFailures(amqp.Table{headerProxyFailures: "x"}) != 0 {
		t.Fatal("missing or malformed counter must read as zero")
	}
	if getProxyFailures(amqp.Table{headerProxyFailures: int32(7)}) != 7 {
		t.Fatal("counter not read")
	}
}

func TestFailedQueueEventTypeIsTheCanonicalKey(t *testing.T) {
	for in, want := range map[string]string{
		"census.id-sweep.failed":           "id-sweep",
		"census.character-census.failed":   "character-census",
		"census.achievement-census.failed": "achievement-census",
	} {
		if got := failedQueueEventType(in); got != want {
			t.Errorf("failedQueueEventType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLegacyRoute(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tiers := contract.DefaultRetryTiers()
	hdr := func(attempts int32, notBefore time.Time) amqp.Table {
		return amqp.Table{headerAttempts: attempts, headerNotBefore: notBefore.UnixMilli()}
	}

	t.Run("elapsed backoff republishes to main", func(t *testing.T) {
		d := legacyRoute(hdr(3, now.Add(-time.Minute)), 50, now, tiers)
		if !d.Direct || d.Dead {
			t.Fatalf("decision = %+v", d)
		}
	})
	t.Run("a message with no not-before header goes straight to main", func(t *testing.T) {
		d := legacyRoute(amqp.Table{headerAttempts: int32(1)}, 50, now, tiers)
		if !d.Direct {
			t.Fatalf("decision = %+v", d)
		}
	})
	t.Run("remaining wait picks the covering tier", func(t *testing.T) {
		d := legacyRoute(hdr(3, now.Add(20*time.Second)), 50, now, tiers)
		if d.Direct || d.Dead || d.Tier != 1 {
			t.Fatalf("decision = %+v, want tier 1", d)
		}
	})
	t.Run("exhausted attempts dead-park", func(t *testing.T) {
		d := legacyRoute(hdr(50, now), 50, now, tiers)
		if !d.Dead {
			t.Fatalf("decision = %+v", d)
		}
	})
}
