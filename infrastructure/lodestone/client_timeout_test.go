package lodestone

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/mihaiflorentin88/ffxiv-census/mock"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// TestDoRequest_ResponseTimeoutReturnsImmediately pins the fast-fail
// semantics for a full response timeout: once the proxy tunnel is
// established and Lodestone does not answer within the client timeout,
// retrying through the same proxy after a sub-second backoff cannot clear
// the stall. The delivery must return to the queue immediately so the
// retry ladder redelivers it — usually through a different proxy — instead
// of the goroutine babysitting the request for minutes (the unacked window
// that showed up as 1m-2m43s jobs). A response timeout is target-side
// ambiguity, not a proven proxy death, so it must not be classified as
// CheckProxy either: it is typed CheckDeadline so the consumer can rotate
// and cool the proxy without recording general failure evidence.
func TestDoRequest_ResponseTimeoutReturnsImmediately(t *testing.T) {
	requests := 0
	c := testCustomClientWithLimiter(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, &url.Error{
			Op:  "Get",
			URL: "https://na.finalfantasyxiv.com/lodestone/character/1/",
			Err: context.DeadlineExceeded,
		}
	}), mock.NewProviderRateLimiter(), 3)

	_, _, err := c.doRequest(context.Background(), "https://na.finalfantasyxiv.com/lodestone/character/1/")
	if err == nil {
		t.Fatal("expected the timeout error to surface")
	}
	if requests != 1 {
		t.Fatalf("a response timeout must abort the in-client ladder after 1 attempt, got %d requests", requests)
	}
	var checkErr *contract.ProxyCheckError
	if errors.As(err, &checkErr) && checkErr.Kind == contract.CheckProxy {
		t.Fatal("a response timeout is target-side ambiguity and must not rotate the proxy")
	}
}

func TestDoRequest_TimeoutIsTypedDeadlineAndKeepsCause(t *testing.T) {
	c := testCustomClientWithLimiter(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: "https://na.finalfantasyxiv.com/", Err: context.DeadlineExceeded}
	}), mock.NewProviderRateLimiter(), 3)

	_, _, err := c.doRequest(context.Background(), "https://na.finalfantasyxiv.com/lodestone/character/1/")
	var checkErr *contract.ProxyCheckError
	if !errors.As(err, &checkErr) || checkErr.Kind != contract.CheckDeadline {
		t.Fatalf("expected a typed CheckDeadline error, got %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("the original deadline cause must stay in the chain")
	}
}

func TestDoRequest_CallerDeadlineIsNotATypedProxyTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	c := testCustomClientWithLimiter(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	}), mock.NewProviderRateLimiter(), 3)

	_, _, err := c.doRequest(ctx, "https://na.finalfantasyxiv.com/lodestone/character/1/")
	var checkErr *contract.ProxyCheckError
	if errors.As(err, &checkErr) {
		t.Fatalf("the caller's own expired context must not blame the proxy, got %v", err)
	}
}

func TestFetch_ServerErrorIsTypedTargetWithStatus(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		c := testCustomClientWithLimiter(observedResponse(status, nil, "boom"), mock.NewProviderRateLimiter(), 3)

		_, charErr := c.FetchCharacter(context.Background(), 1)
		_, achErr := c.FetchAchievements(context.Background(), 1, []uint32{1})
		for name, err := range map[string]error{"character": charErr, "achievements": achErr} {
			var checkErr *contract.ProxyCheckError
			if !errors.As(err, &checkErr) || checkErr.Kind != contract.CheckTarget || checkErr.Status != status {
				t.Errorf("%s %d: expected typed CheckTarget with status, got %v", name, status, err)
			}
			if checkErr != nil && checkErr.Challenge {
				t.Errorf("%s %d: a 5xx is not a challenge", name, status)
			}
		}
	}
}

func TestFetch_GenuineNotFoundAndPrivateStayUntyped(t *testing.T) {
	c := testCustomClientWithLimiter(observedResponse(http.StatusNotFound, nil, strings.Repeat("x", 10)), mock.NewProviderRateLimiter(), 3)
	_, err := c.FetchCharacter(context.Background(), 1)
	if !errors.Is(err, contract.ErrCharacterNotFound) {
		t.Fatalf("404 must stay ErrCharacterNotFound, got %v", err)
	}
	var checkErr *contract.ProxyCheckError
	if errors.As(err, &checkErr) {
		t.Fatalf("404 must not be a proxy failure, got %v", err)
	}

	c = testCustomClientWithLimiter(observedResponse(http.StatusForbidden, nil, "private"), mock.NewProviderRateLimiter(), 3)
	summary, err := c.FetchAchievements(context.Background(), 1, []uint32{1})
	if err != nil || summary == nil || !summary.Private {
		t.Fatalf("genuine 403 must stay a private summary, got %v, %v", summary, err)
	}
}
