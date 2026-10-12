package pubproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
	requestdto "github.com/mihaiflorentin88/ffxiv-census/port/dto/request"
	"github.com/mihaiflorentin88/ffxiv-census/port/dto/response"
)

type fakeHTTPClient struct {
	responses map[string]response.HTTPResponse
	err       error
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

func (f *fakeHTTPClient) GetStream(_ context.Context, url string, _, _ map[string]string, consume func(int, io.Reader) error) error {
	if f.err != nil {
		return f.err
	}
	if resp, ok := f.responses[url]; ok {
		return consume(resp.StatusCode, bytes.NewReader(resp.Body))
	}
	return consume(404, bytes.NewReader(nil))
}

func (f *fakeHTTPClient) GetStreamHeaders(ctx context.Context, url string, q, h map[string]string, consume func(int, map[string][]string, io.Reader) error) error {
	return f.GetStream(ctx, url, q, h, func(status int, body io.Reader) error {
		return consume(status, nil, body)
	})
}

func TestFetchProxies_Success(t *testing.T) {
	body := `{
		"data": [
			{"ip": "1.2.3.4", "port": "8080", "type": "http", "country": "US", "proxy_level": "anonymous", "speed": "6"},
			{"ip": "5.6.7.8", "port": "1080", "type": "socks4", "country": "DE", "proxy_level": "elite", "speed": "3"}
		],
		"count": 2
	}`

	client := New(&fakeHTTPClient{
		responses: map[string]response.HTTPResponse{
			defaultURL: {StatusCode: 200, Body: []byte(body)},
		},
	}, "")

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

	if records[0].IP != "1.2.3.4" {
		t.Errorf("expected IP 1.2.3.4, got %s", records[0].IP)
	}
	if records[0].Port != 8080 {
		t.Errorf("expected port 8080, got %d", records[0].Port)
	}
	if records[0].Protocol != "http" {
		t.Errorf("expected protocol http, got %s", records[0].Protocol)
	}
	if records[0].Source != "pubproxy" {
		t.Errorf("expected source pubproxy, got %s", records[0].Source)
	}
	if records[0].Status != contract.ProxyStatusInactive {
		t.Errorf("expected status inactive, got %s", records[0].Status)
	}
	if records[0].Country == nil || *records[0].Country != "US" {
		t.Errorf("expected country US, got %v", records[0].Country)
	}
	if records[0].Anonymity == nil || *records[0].Anonymity != "anonymous" {
		t.Errorf("expected anonymity anonymous, got %v", records[0].Anonymity)
	}

	if records[1].Protocol != "socks4" {
		t.Errorf("expected protocol socks4, got %s", records[1].Protocol)
	}
	if records[1].Port != 1080 {
		t.Errorf("expected port 1080, got %d", records[1].Port)
	}
}

func TestFetchProxies_EmptyData(t *testing.T) {
	client := New(&fakeHTTPClient{
		responses: map[string]response.HTTPResponse{
			defaultURL: {StatusCode: 200, Body: []byte(`{"data": [], "count": 0}`)},
		},
	}, "")

	var records []contract.ProxyRecord
	err := client.FetchProxies(context.Background(), func(rec contract.ProxyRecord) error {
		records = append(records, rec)
		return nil
	})
	if err != nil {
		t.Fatalf("FetchProxies: %v", err)
	}

	if len(records) != 0 {
		t.Fatalf("expected 0 records, got %d", len(records))
	}
}

func TestFetchProxies_InvalidPort(t *testing.T) {
	body := `{
		"data": [
			{"ip": "1.2.3.4", "port": "bad", "type": "http", "country": "US"},
			{"ip": "5.6.7.8", "port": "8080", "type": "http", "country": "US"}
		],
		"count": 2
	}`

	client := New(&fakeHTTPClient{
		responses: map[string]response.HTTPResponse{
			defaultURL: {StatusCode: 200, Body: []byte(body)},
		},
	}, "")

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
	if records[0].IP != "5.6.7.8" {
		t.Errorf("expected IP 5.6.7.8, got %s", records[0].IP)
	}
}

func TestFetchProxies_DefaultURL(t *testing.T) {
	client := New(nil, "")
	if client.baseURL != defaultURL {
		t.Errorf("expected default URL %s, got %s", defaultURL, client.baseURL)
	}
}

func TestName(t *testing.T) {
	client := New(nil, "")
	if client.Name() != "pubproxy" {
		t.Errorf("expected name pubproxy, got %s", client.Name())
	}
}
