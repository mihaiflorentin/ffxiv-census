package httpclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
	requestdto "github.com/mihaiflorentin88/ffxiv-census/port/dto/request"
	responsedto "github.com/mihaiflorentin88/ffxiv-census/port/dto/response"
)

const defaultTimeout = 30 * time.Second

type client struct {
	httpClient *http.Client
}

// New constructs an HTTP client adapter with a sensible default timeout.
func New(httpClient *http.Client) contract.HTTPClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return &client{httpClient: httpClient}
}

// buildRequest validates the request DTO, encodes query parameters, applies an
// optional per-request timeout, and returns a ready *http.Request plus a cancel
// function that the caller MUST defer.
func (c *client) buildRequest(ctx context.Context, req requestdto.HTTPRequest) (*http.Request, context.CancelFunc, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		return nil, nil, fmt.Errorf("http method is required")
	}
	if strings.TrimSpace(req.URL) == "" {
		return nil, nil, fmt.Errorf("request url is required")
	}

	parsedURL, err := url.Parse(req.URL)
	if err != nil {
		return nil, nil, fmt.Errorf("parse url %q: %w", req.URL, err)
	}

	query := parsedURL.Query()
	for key, value := range req.QueryParams {
		query.Set(key, value)
	}
	parsedURL.RawQuery = query.Encode()

	requestCtx := ctx
	var cancel context.CancelFunc = func() {}
	if req.Timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	}

	httpReq, err := http.NewRequestWithContext(requestCtx, method, parsedURL.String(), bytes.NewReader(req.Body))
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("build request %s %s: %w", method, parsedURL.String(), err)
	}
	for key, value := range req.Headers {
		httpReq.Header.Set(key, value)
	}

	return httpReq, cancel, nil
}

func (c *client) Do(ctx context.Context, req requestdto.HTTPRequest) (responsedto.HTTPResponse, error) {
	httpReq, cancel, err := c.buildRequest(ctx, req)
	if err != nil {
		return responsedto.HTTPResponse{}, err
	}
	defer cancel()

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return responsedto.HTTPResponse{}, fmt.Errorf("execute request %s %s: %w", httpReq.Method, httpReq.URL.String(), err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return responsedto.HTTPResponse{}, fmt.Errorf("read response body %s %s: %w", httpReq.Method, httpReq.URL.String(), err)
	}

	response := responsedto.HTTPResponse{
		StatusCode: resp.StatusCode,
		Headers:    cloneHeaders(resp.Header),
		Body:       body,
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return response, fmt.Errorf("request %s %s failed with status %d: %s", httpReq.Method, httpReq.URL.String(), resp.StatusCode, message)
	}

	return response, nil
}

func (c *client) GetStream(
	ctx context.Context,
	rawURL string,
	queryParams, headers map[string]string,
	consume func(int, io.Reader) error,
) error {
	if consume == nil {
		return fmt.Errorf("stream response consumer is required")
	}
	return c.GetStreamHeaders(ctx, rawURL, queryParams, headers, func(status int, _ map[string][]string, body io.Reader) error {
		return consume(status, body)
	})
}

func (c *client) GetStreamHeaders(
	ctx context.Context,
	rawURL string,
	queryParams, headers map[string]string,
	consume func(int, map[string][]string, io.Reader) error,
) error {
	if consume == nil {
		return fmt.Errorf("stream response consumer is required")
	}
	req, cancel, err := c.buildRequest(ctx, requestdto.HTTPRequest{
		Method:      http.MethodGet,
		URL:         rawURL,
		QueryParams: queryParams,
		Headers:     headers,
	})
	if err != nil {
		return err
	}
	defer cancel()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute request %s %s: %w", req.Method, req.URL.String(), err)
	}
	defer resp.Body.Close()

	return consume(resp.StatusCode, resp.Header, resp.Body)
}

func (c *client) Get(ctx context.Context, url string, queryParams, headers map[string]string) (responsedto.HTTPResponse, error) {
	return c.Do(ctx, requestdto.HTTPRequest{
		Method:      http.MethodGet,
		URL:         url,
		QueryParams: queryParams,
		Headers:     headers,
	})
}

func (c *client) Post(ctx context.Context, url string, queryParams, headers map[string]string, body []byte) (responsedto.HTTPResponse, error) {
	return c.Do(ctx, requestdto.HTTPRequest{
		Method:      http.MethodPost,
		URL:         url,
		QueryParams: queryParams,
		Headers:     headers,
		Body:        body,
	})
}

func (c *client) Patch(ctx context.Context, url string, queryParams, headers map[string]string, body []byte) (responsedto.HTTPResponse, error) {
	return c.Do(ctx, requestdto.HTTPRequest{
		Method:      http.MethodPatch,
		URL:         url,
		QueryParams: queryParams,
		Headers:     headers,
		Body:        body,
	})
}

func (c *client) Delete(ctx context.Context, url string, queryParams, headers map[string]string) (responsedto.HTTPResponse, error) {
	return c.Do(ctx, requestdto.HTTPRequest{
		Method:      http.MethodDelete,
		URL:         url,
		QueryParams: queryParams,
		Headers:     headers,
	})
}

func cloneHeaders(headers http.Header) map[string][]string {
	if len(headers) == 0 {
		return map[string][]string{}
	}

	cloned := make(map[string][]string, len(headers))
	for key, values := range headers {
		copyValues := make([]string, len(values))
		copy(copyValues, values)
		cloned[key] = copyValues
	}
	return cloned
}
