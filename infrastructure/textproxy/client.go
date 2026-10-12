package textproxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/logging"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// errEmitFailed wraps an error returned by the emit callback so callers can
// distinguish callback-directed failures from fetch/decode/scan errors.
var errEmitFailed = errors.New("emit failed")

// Client implements contract.ConditionalProvider for plain-text ip:port proxy
// lists. Used for GitHub-hosted lists like Proxifly and TheSpeedX.
type Client struct {
	httpClient contract.HTTPClient
	name       string
	urls       map[string]string // protocol -> URL
}

// Ensure Client implements contract.ConditionalProvider at compile time.
var _ contract.ConditionalProvider = (*Client)(nil)

// New creates a new text proxy provider client.
func New(httpClient contract.HTTPClient, name string, urls map[string]string) *Client {
	return &Client{httpClient: httpClient, name: name, urls: urls}
}

func (c *Client) Name() string { return c.name }

// FetchProxies streams every list, in a random protocol order.
func (c *Client) FetchProxies(ctx context.Context, emit func(contract.ProxyRecord) error) error {
	return c.FetchProxiesIfChanged(ctx, nil, emit, nil)
}

// FetchProxiesIfChanged implements contract.ConditionalProvider. A list whose
// known validators the server accepts (HTTP 304) is skipped; every list read to
// its last line is reported through listDone with the validators of that copy.
//
// The lists are read in a random order: with a per-run budget cutting the
// stream short, a fixed (or map-iteration) order would decide which protocol
// gets the budget.
func (c *Client) FetchProxiesIfChanged(ctx context.Context, known func(url string) (contract.ListValidators, bool),
	emit func(contract.ProxyRecord) error, listDone func(url string, v contract.ListValidators) error,
) error {
	fetched, unchanged := 0, 0

	for _, protocol := range c.protocolOrder() {
		url := c.urls[protocol]
		var prior contract.ListValidators
		if known != nil {
			prior, _ = known(url)
		}
		notModified, err := c.fetchFromURL(ctx, url, protocol, prior, emit, listDone, &fetched)
		if err != nil {
			// Emit errors are caller-directed and must propagate immediately;
			// only fetch/decode/scan failures participate in partial-failure policy.
			if errors.Is(err, errEmitFailed) {
				return err
			}
			logging.Warn(c.name, fmt.Sprintf("failed to fetch %s proxies from %s: %v", protocol, url, err))
			continue
		}
		if notModified {
			unchanged++
			logging.Info(c.name, fmt.Sprintf("skipped unchanged %s list %s", protocol, url))
		}
	}

	if fetched == 0 && unchanged == 0 {
		return fmt.Errorf("%s: no proxies fetched from any source", c.name)
	}

	return nil
}

// protocolOrder returns the protocols of the configured lists in random order.
func (c *Client) protocolOrder() []string {
	protocols := make([]string, 0, len(c.urls))
	for protocol := range c.urls {
		protocols = append(protocols, protocol)
	}
	rand.Shuffle(len(protocols), func(i, j int) { protocols[i], protocols[j] = protocols[j], protocols[i] })
	return protocols
}

// fetchFromURL streams one list. It sends the prior validators as a
// conditional request and reports whether the server answered "not modified".
func (c *Client) fetchFromURL(ctx context.Context, url, protocol string, prior contract.ListValidators,
	emit func(contract.ProxyRecord) error, listDone func(string, contract.ListValidators) error, fetched *int,
) (notModified bool, err error) {
	var headers map[string]string
	if !prior.IsZero() {
		headers = map[string]string{}
		if prior.ETag != "" {
			headers["If-None-Match"] = prior.ETag
		}
		if prior.LastModified != "" {
			headers["If-Modified-Since"] = prior.LastModified
		}
	}

	err = c.httpClient.GetStreamHeaders(ctx, url, nil, headers, func(statusCode int, header map[string][]string, body io.Reader) error {
		if statusCode == http.StatusNotModified && headers != nil {
			notModified = true
			return nil
		}
		if statusCode != http.StatusOK {
			return fmt.Errorf("%s: unexpected status %d for %s", c.name, statusCode, protocol)
		}

		now := time.Now().UTC()
		scanner := bufio.NewScanner(body)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			// Strip protocol prefix if present (e.g. "http://1.2.3.4:8080" -> "1.2.3.4:8080")
			if idx := strings.Index(line, "://"); idx != -1 {
				line = line[idx+3:]
			}

			host, portStr, err := net.SplitHostPort(line)
			if err != nil {
				logging.Warn(c.name, fmt.Sprintf("skipping malformed line %q: %v", line, err))
				continue
			}

			var port int
			if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
				logging.Warn(c.name, fmt.Sprintf("skipping invalid port in %q: %v", line, err))
				continue
			}

			rec := contract.ProxyRecord{
				Protocol:    protocol,
				IP:          host,
				Port:        port,
				FirstSeenAt: now,
				Source:      c.name,
				Status:      contract.ProxyStatusInactive,
			}
			if err := emit(rec); err != nil {
				return fmt.Errorf("%w: %w", errEmitFailed, err)
			}
			*fetched++
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		if listDone != nil {
			h := http.Header(header)
			v := contract.ListValidators{ETag: h.Get("ETag"), LastModified: h.Get("Last-Modified")}
			if err := listDone(url, v); err != nil {
				return fmt.Errorf("%w: %w", errEmitFailed, err)
			}
		}
		return nil
	})
	return notModified, err
}
