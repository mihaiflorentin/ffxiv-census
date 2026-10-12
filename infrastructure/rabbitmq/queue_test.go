package rabbitmq

import (
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestCanonicalEventType(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "id-sweep", want: "id-sweep"},
		{in: "id-sweep.failed", want: "id-sweep"},
		{in: "character-census.failed", want: "character-census"},
		{in: "achievement-census", want: "achievement-census"},
		{in: "new-proxy.failed", want: "new-proxy"},
	}
	for _, tt := range tests {
		if got := canonicalEventType(tt.in); got != tt.want {
			t.Fatalf("canonicalEventType(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestKnownEventType(t *testing.T) {
	for _, et := range eventTypes() {
		if !knownEventType(et) {
			t.Fatalf("knownEventType(%q) = false, want true", et)
		}
	}
	if knownEventType("census.id-sweep") {
		t.Fatal("knownEventType(\"census.id-sweep\") = true, want false")
	}
	if knownEventType("no-such-event") {
		t.Fatal("knownEventType(\"no-such-event\") = true, want false")
	}
}

func TestRemainingBackoff(t *testing.T) {
	future := time.Now().Add(30 * time.Second).UnixMilli()
	past := time.Now().Add(-30 * time.Second).UnixMilli()

	tests := []struct {
		name    string
		headers amqp.Table
		wantMin time.Duration
		wantMax time.Duration
	}{
		{
			name:    "missing header means no wait",
			headers: nil,
			wantMax: 0,
		},
		{
			name:    "past not-before means no wait",
			headers: amqp.Table{"x-not-before-ms": past},
			wantMax: 0,
		},
		{
			name:    "future not-before waits the remainder",
			headers: amqp.Table{"x-not-before-ms": future},
			wantMin: 29 * time.Second,
			wantMax: 31 * time.Second,
		},
		{
			name:    "wrong header type means no wait",
			headers: amqp.Table{"x-not-before-ms": "not-a-number"},
			wantMax: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := remainingBackoff(tt.headers, time.Now())
			if got < tt.wantMin || got > tt.wantMax {
				t.Fatalf("remainingBackoff = %v, want in [%v, %v]", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}
