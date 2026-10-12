package lodestone

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	mockmetrics "github.com/mihaiflorentin88/ffxiv-census/mock/metrics"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func observedResponse(status int, header http.Header, body string) http.RoundTripper {
	return roundTripperFunc(func(*http.Request) (*http.Response, error) {
		if header == nil {
			header = http.Header{}
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
}

func TestDoRequest_ObservesOutcomePerProxy(t *testing.T) {
	cases := []struct {
		name string
		rt   http.RoundTripper
		want contract.LodestoneOutcome
	}{
		{"success", observedResponse(http.StatusOK, nil, "ok"), contract.LodestoneOutcomeSuccess},
		{"genuine 404", observedResponse(http.StatusNotFound, nil, "gone"), contract.LodestoneOutcomeSuccess},
		{"genuine 403", observedResponse(http.StatusForbidden, nil, "private"), contract.LodestoneOutcomeSuccess},
		{"429", observedResponse(http.StatusTooManyRequests, nil, ""), contract.LodestoneOutcomeRateLimited},
		{"202", observedResponse(http.StatusAccepted, nil, ""), contract.LodestoneOutcomeChallenge},
		{"403 challenge", observedResponse(http.StatusForbidden, http.Header{"Cf-Mitigated": []string{"challenge"}}, ""), contract.LodestoneOutcomeChallenge},
		{"5xx", observedResponse(http.StatusBadGateway, nil, ""), contract.LodestoneOutcomeFailure},
		{"transport error", roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("reset") }), contract.LodestoneOutcomeFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := &mockmetrics.LodestoneRequestObserver{}
			c := testCustomClient(tc.rt)
			c.proxyURL = "http://p1:8080"
			c.observer = obs
			_, _, _ = c.doRequest(context.Background(), "https://na.finalfantasyxiv.com/lodestone/")
			got := obs.Requests()
			if len(got) != 1 || got[0].Outcome != tc.want || got[0].Proxy != "http://p1:8080" {
				t.Fatalf("observations = %+v, want one %q for the proxy", got, tc.want)
			}
		})
	}
}

func TestDoRequest_NilObserverIsSafe(t *testing.T) {
	c := testCustomClient(observedResponse(http.StatusOK, nil, "ok"))
	if _, _, err := c.doRequest(context.Background(), "https://na.finalfantasyxiv.com/lodestone/"); err != nil {
		t.Fatal(err)
	}
}
