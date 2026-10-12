package proxy

import (
	"context"
	"sync"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// FakeList is one list of a FakeListProvider; the server "sends" ETag with it.
type FakeList struct {
	URL     string
	ETag    string
	Records []contract.ProxyRecord
}

// FakeListProvider is an in-memory ConditionalProvider for tests: a provider
// made of lists that the "server" skips (like an HTTP 304) when the known
// validators carry the list's current ETag.
type FakeListProvider struct {
	name  string
	lists []FakeList
	// Read and Skipped record the URLs read completely or partially and the
	// URLs answered "not modified", in order, across calls.
	Read    []string
	Skipped []string
}

// Ensure FakeListProvider implements contract.ConditionalProvider at compile time.
var _ contract.ConditionalProvider = (*FakeListProvider)(nil)

func NewFakeListProvider(name string, lists ...FakeList) *FakeListProvider {
	return &FakeListProvider{name: name, lists: lists}
}

func (f *FakeListProvider) Name() string { return f.name }

// SetETag changes the version of a list, as if the upstream file was updated.
func (f *FakeListProvider) SetETag(url, etag string) {
	for i := range f.lists {
		if f.lists[i].URL == url {
			f.lists[i].ETag = etag
		}
	}
}

func (f *FakeListProvider) FetchProxies(ctx context.Context, emit func(contract.ProxyRecord) error) error {
	return f.FetchProxiesIfChanged(ctx, nil, emit, nil)
}

func (f *FakeListProvider) FetchProxiesIfChanged(_ context.Context, known func(string) (contract.ListValidators, bool),
	emit func(contract.ProxyRecord) error, listDone func(string, contract.ListValidators) error,
) error {
	for _, l := range f.lists {
		if known != nil && l.ETag != "" {
			if v, ok := known(l.URL); ok && v.ETag == l.ETag {
				f.Skipped = append(f.Skipped, l.URL)
				continue
			}
		}
		f.Read = append(f.Read, l.URL)
		for _, rec := range l.Records {
			if err := emit(rec); err != nil {
				return err
			}
		}
		if listDone != nil {
			if err := listDone(l.URL, contract.ListValidators{ETag: l.ETag}); err != nil {
				return err
			}
		}
	}
	return nil
}

// FakeListCache is an in-memory ProxyListCache for tests.
type FakeListCache struct {
	mu      sync.Mutex
	entries map[string]contract.ListValidators
	// LoadErr and SaveErr are returned by the matching method when set.
	LoadErr error
	SaveErr error
	// Saves counts successful SaveListValidators calls.
	Saves int
}

// Ensure FakeListCache implements contract.ProxyListCache at compile time.
var _ contract.ProxyListCache = (*FakeListCache)(nil)

func NewFakeListCache() *FakeListCache {
	return &FakeListCache{entries: map[string]contract.ListValidators{}}
}

func (f *FakeListCache) LoadListValidators(_ context.Context) (map[string]contract.ListValidators, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.LoadErr != nil {
		return nil, f.LoadErr
	}
	out := make(map[string]contract.ListValidators, len(f.entries))
	for k, v := range f.entries {
		out[k] = v
	}
	return out, nil
}

func (f *FakeListCache) SaveListValidators(_ context.Context, url string, v contract.ListValidators) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SaveErr != nil {
		return f.SaveErr
	}
	f.entries[url] = v
	f.Saves++
	return nil
}

// Get returns the validators stored for url.
func (f *FakeListCache) Get(url string) (contract.ListValidators, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.entries[url]
	return v, ok
}
