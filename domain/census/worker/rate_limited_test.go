package worker

import (
	"errors"
	"fmt"
	"testing"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestIsRateLimited_UsesTheTypedStatusNeverErrorText(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"typed 429", &contract.ProxyCheckError{Kind: contract.CheckTarget, Status: 429}, true},
		{"wrapped typed 429", fmt.Errorf("request: %w", &contract.ProxyCheckError{Kind: contract.CheckTarget, Status: 429}), true},
		{"typed 503", &contract.ProxyCheckError{Kind: contract.CheckTarget, Status: 503}, false},
		{"typed challenge", &contract.ProxyCheckError{Kind: contract.CheckTarget, Challenge: true}, false},
		{"429 only in the text", errors.New("upstream returned HTTP 429"), false},
		{"rate limit only in the text", errors.New("rate limit exceeded"), false},
		{"429 in the text of a typed non-429", &contract.ProxyCheckError{Kind: contract.CheckTarget, Status: 502, Reason: "status 429 seen earlier"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRateLimited(tt.err); got != tt.want {
				t.Fatalf("isRateLimited(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
