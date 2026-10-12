package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGetStreamHeaders_PassesResponseHeadersAndSendsRequestHeaders(t *testing.T) {
	var gotConditional string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotConditional = r.Header.Get("If-None-Match")
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Last-Modified", "Mon, 12 Oct 2026 10:00:00 GMT")
		_, _ = io.WriteString(w, "1.2.3.4:80\n")
	}))
	defer srv.Close()

	var status int
	var etag, lastModified, body string
	err := New(nil).GetStreamHeaders(context.Background(), srv.URL, nil, map[string]string{"If-None-Match": `"old"`},
		func(code int, header map[string][]string, r io.Reader) error {
			status = code
			etag = http.Header(header).Get("ETag")
			lastModified = http.Header(header).Get("Last-Modified")
			b, _ := io.ReadAll(r)
			body = string(b)
			return nil
		})
	if err != nil {
		t.Fatalf("GetStreamHeaders: %v", err)
	}
	if status != 200 || etag != `"abc"` || lastModified == "" || body != "1.2.3.4:80\n" {
		t.Errorf("status %d etag %q last-modified %q body %q", status, etag, lastModified, body)
	}
	if gotConditional != `"old"` {
		t.Errorf("server saw If-None-Match %q, want the request header passed through", gotConditional)
	}
}

func TestListClient_IdentifiesItselfPolitely(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
	}))
	defer srv.Close()

	c := NewListClient("ffxiv-census-test/1.0", time.Second, 1024)
	if err := c.GetStream(context.Background(), srv.URL, nil, nil, func(int, io.Reader) error { return nil }); err != nil {
		t.Fatalf("GetStream: %v", err)
	}
	if ua != "ffxiv-census-test/1.0" {
		t.Errorf("User-Agent = %q", ua)
	}
	if err := c.GetStreamHeaders(context.Background(), srv.URL, nil, nil, func(int, map[string][]string, io.Reader) error { return nil }); err != nil {
		t.Fatalf("GetStreamHeaders: %v", err)
	}
	if ua != "ffxiv-census-test/1.0" {
		t.Errorf("User-Agent on GetStreamHeaders = %q", ua)
	}
}

func TestListClient_RefusesBodiesOverTheLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("1.2.3.4:80\n", 100))
	}))
	defer srv.Close()

	c := NewListClient("ua", time.Second, 200)
	err := c.GetStream(context.Background(), srv.URL, nil, nil, func(_ int, r io.Reader) error {
		_, err := io.Copy(io.Discard, r)
		return err
	})
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}

	small := NewListClient("ua", time.Second, 1<<20)
	var n int64
	if err := small.GetStream(context.Background(), srv.URL, nil, nil, func(_ int, r io.Reader) error {
		var err error
		n, err = io.Copy(io.Discard, r)
		return err
	}); err != nil || n != 1100 {
		t.Fatalf("within limit: read %d bytes, %v", n, err)
	}
}
