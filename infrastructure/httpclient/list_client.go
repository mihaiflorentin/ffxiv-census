package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// ErrBodyTooLarge is returned while reading a response body that is longer
// than the list client's limit.
var ErrBodyTooLarge = errors.New("response body exceeds the size limit")

// listClient is the direct client for proxy provider lists and APIs: it
// identifies itself, bounds each request by a timeout and refuses oversized
// bodies. It never goes through a proxy.
type listClient struct {
	contract.HTTPClient
	maxBody int64
}

// NewListClient returns a direct HTTP client for fetching provider lists. Every
// request carries userAgent unless the caller sets its own, runs under timeout
// (connect to last body byte) and a body longer than maxBodyBytes fails the
// read with ErrBodyTooLarge.
func NewListClient(userAgent string, timeout time.Duration, maxBodyBytes int64) contract.HTTPClient {
	base := New(&http.Client{
		Timeout:   timeout,
		Transport: &userAgentTransport{next: http.DefaultTransport, userAgent: userAgent},
	})
	return &listClient{HTTPClient: base, maxBody: maxBodyBytes}
}

func (c *listClient) GetStream(ctx context.Context, url string, queryParams, headers map[string]string, consume func(int, io.Reader) error) error {
	if consume == nil {
		return c.HTTPClient.GetStream(ctx, url, queryParams, headers, nil)
	}
	return c.HTTPClient.GetStream(ctx, url, queryParams, headers, func(status int, body io.Reader) error {
		return consume(status, &limitedBody{r: body, left: c.maxBody})
	})
}

func (c *listClient) GetStreamHeaders(ctx context.Context, url string, queryParams, headers map[string]string, consume func(int, map[string][]string, io.Reader) error) error {
	if consume == nil {
		return c.HTTPClient.GetStreamHeaders(ctx, url, queryParams, headers, nil)
	}
	return c.HTTPClient.GetStreamHeaders(ctx, url, queryParams, headers, func(status int, header map[string][]string, body io.Reader) error {
		return consume(status, header, &limitedBody{r: body, left: c.maxBody})
	})
}

// userAgentTransport sets a default User-Agent on requests that carry none.
type userAgentTransport struct {
	next      http.RoundTripper
	userAgent string
}

func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req = req.Clone(req.Context())
		req.Header.Set("User-Agent", t.userAgent)
	}
	return t.next.RoundTrip(req)
}

// limitedBody reads at most left bytes of r and fails with ErrBodyTooLarge if
// the body goes on after that.
type limitedBody struct {
	r    io.Reader
	left int64
}

func (l *limitedBody) Read(p []byte) (int, error) {
	if l.left <= 0 {
		// The limit is used up: a further byte means the body is too long, EOF
		// means it was exactly the limit.
		var probe [1]byte
		n, err := l.r.Read(probe[:])
		if n > 0 {
			return 0, ErrBodyTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	return n, err
}
