package contract

import (
	"context"
	"io"

	requestdto "github.com/mihaiflorentin88/ffxiv-census/port/dto/request"
	responsedto "github.com/mihaiflorentin88/ffxiv-census/port/dto/response"
)

// HTTPClient wraps a small set of outbound HTTP operations.
type HTTPClient interface {
	Do(ctx context.Context, req requestdto.HTTPRequest) (responsedto.HTTPResponse, error)
	Get(ctx context.Context, url string, queryParams, headers map[string]string) (responsedto.HTTPResponse, error)
	Post(ctx context.Context, url string, queryParams, headers map[string]string, body []byte) (responsedto.HTTPResponse, error)
	Patch(ctx context.Context, url string, queryParams, headers map[string]string, body []byte) (responsedto.HTTPResponse, error)
	Delete(ctx context.Context, url string, queryParams, headers map[string]string) (responsedto.HTTPResponse, error)
	// GetStream performs a GET request and passes the live response body to consume.
	// The body is closed after consume returns. The callback receives every HTTP
	// status code — the caller decides what constitutes a retryable or fatal status.
	GetStream(ctx context.Context, url string, queryParams, headers map[string]string, consume func(statusCode int, body io.Reader) error) error
	// GetStreamHeaders is GetStream that also passes the response headers to
	// consume, for callers that need ETag or Last-Modified (conditional GET).
	GetStreamHeaders(ctx context.Context, url string, queryParams, headers map[string]string, consume func(statusCode int, header map[string][]string, body io.Reader) error) error
}
