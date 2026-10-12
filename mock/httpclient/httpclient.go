package mockhttpclient

import (
	"context"
	"io"

	requestdto "github.com/mihaiflorentin88/ffxiv-census/port/dto/request"
	responsedto "github.com/mihaiflorentin88/ffxiv-census/port/dto/response"
)

// Client is a lightweight test double for the HTTP client contract.
type Client struct {
	DoFunc      func(ctx context.Context, req requestdto.HTTPRequest) (responsedto.HTTPResponse, error)
	GetStreamFn func(ctx context.Context, url string, queryParams, headers map[string]string, consume func(int, io.Reader) error) error
	// GetStreamHeadersFn serves GetStreamHeaders; when nil it falls back to GetStream.
	GetStreamHeadersFn func(ctx context.Context, url string, queryParams, headers map[string]string, consume func(int, map[string][]string, io.Reader) error) error
	Response           responsedto.HTTPResponse
	Err                error
	Requests           []requestdto.HTTPRequest
}

func (c *Client) Do(ctx context.Context, req requestdto.HTTPRequest) (responsedto.HTTPResponse, error) {
	c.Requests = append(c.Requests, req)
	if c.DoFunc != nil {
		return c.DoFunc(ctx, req)
	}
	return c.Response, c.Err
}

func (c *Client) Get(ctx context.Context, url string, queryParams, headers map[string]string) (responsedto.HTTPResponse, error) {
	return c.Do(ctx, requestdto.HTTPRequest{
		Method:      "GET",
		URL:         url,
		QueryParams: queryParams,
		Headers:     headers,
	})
}

func (c *Client) Post(ctx context.Context, url string, queryParams, headers map[string]string, body []byte) (responsedto.HTTPResponse, error) {
	return c.Do(ctx, requestdto.HTTPRequest{
		Method:      "POST",
		URL:         url,
		QueryParams: queryParams,
		Headers:     headers,
		Body:        body,
	})
}

func (c *Client) Patch(ctx context.Context, url string, queryParams, headers map[string]string, body []byte) (responsedto.HTTPResponse, error) {
	return c.Do(ctx, requestdto.HTTPRequest{
		Method:      "PATCH",
		URL:         url,
		QueryParams: queryParams,
		Headers:     headers,
		Body:        body,
	})
}

func (c *Client) Delete(ctx context.Context, url string, queryParams, headers map[string]string) (responsedto.HTTPResponse, error) {
	return c.Do(ctx, requestdto.HTTPRequest{
		Method:      "DELETE",
		URL:         url,
		QueryParams: queryParams,
		Headers:     headers,
	})
}

// GetStreamHeaders runs GetStreamHeadersFn when set; otherwise it behaves like
// GetStream with no response headers.
func (c *Client) GetStreamHeaders(ctx context.Context, url string, queryParams, headers map[string]string, consume func(int, map[string][]string, io.Reader) error) error {
	if c.GetStreamHeadersFn != nil {
		return c.GetStreamHeadersFn(ctx, url, queryParams, headers, consume)
	}
	return c.GetStream(ctx, url, queryParams, headers, func(status int, body io.Reader) error {
		return consume(status, nil, body)
	})
}

func (c *Client) GetStream(ctx context.Context, url string, queryParams, headers map[string]string, consume func(int, io.Reader) error) error {
	if c.GetStreamFn != nil {
		return c.GetStreamFn(ctx, url, queryParams, headers, consume)
	}
	return c.Err
}
