package textproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
	requestdto "github.com/mihaiflorentin88/ffxiv-census/port/dto/request"
	"github.com/mihaiflorentin88/ffxiv-census/port/dto/response"
)

type fakeRequest struct {
	url     string
	headers map[string]string
}

type fakeHTTPClient struct {
	responses map[string]response.HTTPResponse
	err       error
	requests  []fakeRequest
}

func (f *fakeHTTPClient) Do(_ context.Context, _ requestdto.HTTPRequest) (response.HTTPResponse, error) {
	return response.HTTPResponse{}, fmt.Errorf("not implemented")
}

func (f *fakeHTTPClient) Get(_ context.Context, url string, _, _ map[string]string) (response.HTTPResponse, error) {
	if f.err != nil {
		return response.HTTPResponse{}, f.err
	}
	if resp, ok := f.responses[url]; ok {
		return resp, nil
	}
	return response.HTTPResponse{StatusCode: 404}, nil
}

func (f *fakeHTTPClient) Post(_ context.Context, _ string, _, _ map[string]string, _ []byte) (response.HTTPResponse, error) {
	return response.HTTPResponse{}, fmt.Errorf("not implemented")
}

func (f *fakeHTTPClient) Patch(_ context.Context, _ string, _, _ map[string]string, _ []byte) (response.HTTPResponse, error) {
	return response.HTTPResponse{}, fmt.Errorf("not implemented")
}

func (f *fakeHTTPClient) Delete(_ context.Context, _ string, _, _ map[string]string) (response.HTTPResponse, error) {
	return response.HTTPResponse{}, fmt.Errorf("not implemented")
}

func (f *fakeHTTPClient) GetStream(ctx context.Context, url string, q, h map[string]string, consume func(int, io.Reader) error) error {
	return f.GetStreamHeaders(ctx, url, q, h, func(status int, _ map[string][]string, body io.Reader) error {
		return consume(status, body)
	})
}

// GetStreamHeaders serves the configured response and, like a server that
// supports conditional GET, answers 304 with no body when the request carries
// the ETag of the response.
func (f *fakeHTTPClient) GetStreamHeaders(_ context.Context, url string, _, headers map[string]string, consume func(int, map[string][]string, io.Reader) error) error {
	f.requests = append(f.requests, fakeRequest{url: url, headers: headers})
	if f.err != nil {
		return f.err
	}
	resp, ok := f.responses[url]
	if !ok {
		return consume(404, nil, bytes.NewReader(nil))
	}
	if etag := http.Header(resp.Headers).Get("ETag"); etag != "" && headers["If-None-Match"] == etag {
		return consume(304, resp.Headers, bytes.NewReader(nil))
	}
	return consume(resp.StatusCode, resp.Headers, bytes.NewReader(resp.Body))
}

func TestFetchProxies_Success(t *testing.T) {
	httpURL := "http://test/http.txt"
	socks4URL := "http://test/socks4.txt"

	client := New(&fakeHTTPClient{
		responses: map[string]response.HTTPResponse{
			httpURL:   {StatusCode: 200, Body: []byte("1.2.3.4:8080\n5.6.7.8:1080\n9.10.11.12:4145\n")},
			socks4URL: {StatusCode: 200, Body: []byte("1.2.3.4:8080\n5.6.7.8:1080\n9.10.11.12:4145\n")},
		},
	}, "test", map[string]string{
		"http":   httpURL,
		"socks4": socks4URL,
	})

	var records []contract.ProxyRecord
	err := client.FetchProxies(context.Background(), func(rec contract.ProxyRecord) error {
		records = append(records, rec)
		return nil
	})
	if err != nil {
		t.Fatalf("FetchProxies: %v", err)
	}

	if len(records) != 6 {
		t.Fatalf("expected 6 records, got %d", len(records))
	}

	if records[0].IP != "1.2.3.4" {
		t.Errorf("expected IP 1.2.3.4, got %s", records[0].IP)
	}
	if records[0].Port != 8080 {
		t.Errorf("expected port 8080, got %d", records[0].Port)
	}
	if records[0].Source != "test" {
		t.Errorf("expected source test, got %s", records[0].Source)
	}
	if records[0].Status != contract.ProxyStatusInactive {
		t.Errorf("expected status inactive, got %s", records[0].Status)
	}
}

func TestFetchProxies_SkipsMalformedLines(t *testing.T) {
	body := "1.2.3.4:8080\nbad-line\n5.6.7.8:1080\n\n"
	client := New(&fakeHTTPClient{
		responses: map[string]response.HTTPResponse{
			"http://test/http.txt": {StatusCode: 200, Body: []byte(body)},
		},
	}, "test", map[string]string{
		"http": "http://test/http.txt",
	})

	var records []contract.ProxyRecord
	err := client.FetchProxies(context.Background(), func(rec contract.ProxyRecord) error {
		records = append(records, rec)
		return nil
	})
	if err != nil {
		t.Fatalf("FetchProxies: %v", err)
	}

	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
}

func TestFetchProxies_PartialFailure(t *testing.T) {
	client := New(&fakeHTTPClient{
		responses: map[string]response.HTTPResponse{
			"http://test/http.txt":   {StatusCode: 200, Body: []byte("1.2.3.4:8080\n")},
			"http://test/socks5.txt": {StatusCode: 500},
		},
	}, "test", map[string]string{
		"http":   "http://test/http.txt",
		"socks5": "http://test/socks5.txt",
	})

	var records []contract.ProxyRecord
	err := client.FetchProxies(context.Background(), func(rec contract.ProxyRecord) error {
		records = append(records, rec)
		return nil
	})
	if err != nil {
		t.Fatalf("FetchProxies: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Protocol != "http" {
		t.Errorf("expected protocol http, got %s", records[0].Protocol)
	}
}

func TestFetchProxies_AllFail(t *testing.T) {
	client := New(&fakeHTTPClient{
		responses: map[string]response.HTTPResponse{
			"http://test/http.txt": {StatusCode: 500},
		},
	}, "test", map[string]string{
		"http": "http://test/http.txt",
	})

	err := client.FetchProxies(context.Background(), func(_ contract.ProxyRecord) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected error when all sources fail")
	}
}

func TestFetchProxies_StripsProtocolPrefix(t *testing.T) {
	body := "http://1.2.3.4:8080\nsocks4://5.6.7.8:1080\nsocks5://9.10.11.12:4145\n"
	client := New(&fakeHTTPClient{
		responses: map[string]response.HTTPResponse{
			"http://test/http.txt": {StatusCode: 200, Body: []byte(body)},
		},
	}, "test", map[string]string{
		"http": "http://test/http.txt",
	})

	var records []contract.ProxyRecord
	err := client.FetchProxies(context.Background(), func(rec contract.ProxyRecord) error {
		records = append(records, rec)
		return nil
	})
	if err != nil {
		t.Fatalf("FetchProxies: %v", err)
	}

	if len(records) != 3 {
		t.Fatalf("expected 3 records, got %d", len(records))
	}
	if records[0].IP != "1.2.3.4" || records[0].Port != 8080 {
		t.Errorf("expected 1.2.3.4:8080, got %s:%d", records[0].IP, records[0].Port)
	}
	if records[1].IP != "5.6.7.8" || records[1].Port != 1080 {
		t.Errorf("expected 5.6.7.8:1080, got %s:%d", records[1].IP, records[1].Port)
	}
}

func TestName(t *testing.T) {
	client := New(nil, "proxifly", nil)
	if client.Name() != "proxifly" {
		t.Errorf("expected name proxifly, got %s", client.Name())
	}
}

func etagResponse(etag, body string) response.HTTPResponse {
	return response.HTTPResponse{
		StatusCode: 200,
		Headers:    map[string][]string{"Etag": {etag}, "Last-Modified": {"Mon, 12 Oct 2026 10:00:00 GMT"}},
		Body:       []byte(body),
	}
}

func TestFetchProxiesIfChanged_SendsKnownValidatorsAndSkipsNotModified(t *testing.T) {
	fake := &fakeHTTPClient{responses: map[string]response.HTTPResponse{
		"http://test/http.txt":   etagResponse(`"v1"`, "1.2.3.4:8080\n"),
		"http://test/socks5.txt": etagResponse(`"s9"`, "5.6.7.8:1080\n"),
	}}
	client := New(fake, "test", map[string]string{"http": "http://test/http.txt", "socks5": "http://test/socks5.txt"})
	known := map[string]contract.ListValidators{
		"http://test/http.txt":   {ETag: `"v1"`, LastModified: "Mon, 12 Oct 2026 10:00:00 GMT"}, // unchanged
		"http://test/socks5.txt": {ETag: `"s8"`},                                                // changed upstream
	}

	var records []contract.ProxyRecord
	done := map[string]contract.ListValidators{}
	err := client.FetchProxiesIfChanged(context.Background(),
		func(url string) (contract.ListValidators, bool) { v, ok := known[url]; return v, ok },
		func(rec contract.ProxyRecord) error { records = append(records, rec); return nil },
		func(url string, v contract.ListValidators) error { done[url] = v; return nil })
	if err != nil {
		t.Fatalf("FetchProxiesIfChanged: %v", err)
	}

	if len(records) != 1 || records[0].IP != "5.6.7.8" {
		t.Fatalf("records = %+v, want only the changed socks5 list", records)
	}
	if len(done) != 1 || done["http://test/socks5.txt"].ETag != `"s9"` || done["http://test/socks5.txt"].LastModified == "" {
		t.Fatalf("listDone calls = %+v, want only the changed list with its new validators", done)
	}
	for _, r := range fake.requests {
		if r.url == "http://test/http.txt" && (r.headers["If-None-Match"] != `"v1"` || r.headers["If-Modified-Since"] == "") {
			t.Errorf("request for the known list carried %v, want If-None-Match and If-Modified-Since", r.headers)
		}
	}
}

func TestFetchProxiesIfChanged_AllListsUnchangedIsNotAFailure(t *testing.T) {
	fake := &fakeHTTPClient{responses: map[string]response.HTTPResponse{
		"http://test/http.txt": etagResponse(`"v1"`, "1.2.3.4:8080\n"),
	}}
	client := New(fake, "test", map[string]string{"http": "http://test/http.txt"})

	err := client.FetchProxiesIfChanged(context.Background(),
		func(string) (contract.ListValidators, bool) { return contract.ListValidators{ETag: `"v1"`}, true },
		func(contract.ProxyRecord) error { t.Fatal("emitted from an unchanged list"); return nil },
		func(string, contract.ListValidators) error { t.Fatal("listDone for an unchanged list"); return nil })
	if err != nil {
		t.Fatalf("FetchProxiesIfChanged: %v", err)
	}
}

func TestFetchProxiesIfChanged_ListDoneRunsAfterTheLastRecordAndItsErrorAborts(t *testing.T) {
	fake := &fakeHTTPClient{responses: map[string]response.HTTPResponse{
		"http://test/http.txt": etagResponse(`"v1"`, "1.2.3.4:8080\n5.6.7.8:80\n"),
	}}
	client := New(fake, "test", map[string]string{"http": "http://test/http.txt"})

	emitted := 0
	err := client.FetchProxiesIfChanged(context.Background(), nil,
		func(contract.ProxyRecord) error { emitted++; return nil },
		func(string, contract.ListValidators) error {
			if emitted != 2 {
				t.Errorf("listDone ran after %d records, want 2", emitted)
			}
			return fmt.Errorf("store failed")
		})
	if err == nil || err.Error() == "" {
		t.Fatal("a listDone error was swallowed")
	}
}

func TestFetchProxies_ProtocolOrderIsNotFixed(t *testing.T) {
	urls := map[string]string{"http": "http://test/h", "socks4": "http://test/s4", "socks5": "http://test/s5"}
	resp := map[string]response.HTTPResponse{}
	for _, u := range urls {
		resp[u] = response.HTTPResponse{StatusCode: 200, Body: []byte("1.2.3.4:80\n")}
	}
	first := map[string]bool{}
	for i := 0; i < 64; i++ {
		fake := &fakeHTTPClient{responses: resp}
		_ = New(fake, "test", urls).FetchProxies(context.Background(), func(contract.ProxyRecord) error { return nil })
		first[fake.requests[0].url] = true
	}
	if len(first) < 2 {
		t.Fatalf("the same list was always fetched first: %v", first)
	}
}
