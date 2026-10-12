package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// MetricType represents the Prometheus metric type.
type MetricType string

const (
	TypeCounter   MetricType = "counter"
	TypeGauge     MetricType = "gauge"
	TypeHistogram MetricType = "histogram"
)

// Registry manages in-memory Prometheus metrics and renders Prometheus exposition format.
type Registry struct {
	mu         sync.RWMutex
	counters   map[string]*Counter
	gauges     map[string]*Gauge
	histograms map[string]*Histogram
	collectors []func()
}

// NewRegistry creates a new Prometheus metrics registry.
func NewRegistry() *Registry {
	return &Registry{
		counters:   make(map[string]*Counter),
		gauges:     make(map[string]*Gauge),
		histograms: make(map[string]*Histogram),
	}
}

// RegisterCollector registers a custom function to update dynamic metrics right before Gather.
func (r *Registry) RegisterCollector(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.collectors = append(r.collectors, fn)
}

// Counter represents a Prometheus counter.
type Counter struct {
	name   string
	help   string
	mu     sync.RWMutex
	values map[string]float64
}

// Inc increments the counter by 1 for the given labels.
func (c *Counter) Inc(labels map[string]string) {
	c.Add(labels, 1)
}

// Add adds the given delta to the counter for the given labels.
func (c *Counter) Add(labels map[string]string, delta float64) {
	if delta < 0 {
		return
	}
	key := labelsToKey(labels)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[key] += delta
}

// Gauge represents a Prometheus gauge.
type Gauge struct {
	name   string
	help   string
	mu     sync.RWMutex
	values map[string]float64
}

// Set sets the gauge value for the given labels.
func (g *Gauge) Set(labels map[string]string, val float64) {
	key := labelsToKey(labels)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.values[key] = val
}

// Inc increments the gauge by 1 for the given labels.
func (g *Gauge) Inc(labels map[string]string) {
	g.Add(labels, 1)
}

// Dec decrements the gauge by 1 for the given labels.
func (g *Gauge) Dec(labels map[string]string) {
	g.Sub(labels, 1)
}

// Add adds the given delta to the gauge for the given labels.
func (g *Gauge) Add(labels map[string]string, delta float64) {
	key := labelsToKey(labels)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.values[key] += delta
}

// Sub subtracts the given delta from the gauge for the given labels.
func (g *Gauge) Sub(labels map[string]string, delta float64) {
	key := labelsToKey(labels)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.values[key] -= delta
}

// Histogram represents a Prometheus histogram.
type Histogram struct {
	name    string
	help    string
	buckets []float64
	mu      sync.RWMutex
	counts  map[string]map[float64]uint64
	sums    map[string]float64
	totals  map[string]uint64
}

// Observe records a sample value into the histogram for the given labels.
func (h *Histogram) Observe(labels map[string]string, val float64) {
	key := labelsToKey(labels)
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.counts[key]; !ok {
		h.counts[key] = make(map[float64]uint64)
	}

	for _, b := range h.buckets {
		if val <= b {
			h.counts[key][b]++
		}
	}
	h.sums[key] += val
	h.totals[key]++
}

// NewCounter registers and returns a Counter in the registry.
func (r *Registry) NewCounter(name, help string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.counters[name]; ok {
		return c
	}
	c := &Counter{
		name:   name,
		help:   help,
		values: make(map[string]float64),
	}
	r.counters[name] = c
	return c
}

// NewGauge registers and returns a Gauge in the registry.
func (r *Registry) NewGauge(name, help string) *Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()
	if g, ok := r.gauges[name]; ok {
		return g
	}
	g := &Gauge{
		name:   name,
		help:   help,
		values: make(map[string]float64),
	}
	r.gauges[name] = g
	return g
}

// NewHistogram registers and returns a Histogram in the registry.
func (r *Registry) NewHistogram(name, help string, buckets []float64) *Histogram {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.histograms[name]; ok {
		return h
	}
	b := make([]float64, len(buckets))
	copy(b, buckets)
	sort.Float64s(b)

	h := &Histogram{
		name:    name,
		help:    help,
		buckets: b,
		counts:  make(map[string]map[float64]uint64),
		sums:    make(map[string]float64),
		totals:  make(map[string]uint64),
	}
	r.histograms[name] = h
	return h
}

// Gather runs any registered collectors and formats all registered metrics into Prometheus text format.
func (r *Registry) Gather() string {
	r.mu.RLock()
	collectors := append([]func(){}, r.collectors...)
	r.mu.RUnlock()

	for _, col := range collectors {
		col()
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	var sb strings.Builder

	// Sorted metric names for deterministic output
	var counterNames []string
	for name := range r.counters {
		counterNames = append(counterNames, name)
	}
	sort.Strings(counterNames)

	for _, name := range counterNames {
		c := r.counters[name]
		c.mu.RLock()
		if len(c.values) > 0 {
			fmt.Fprintf(&sb, "# HELP %s %s\n", c.name, c.help)
			fmt.Fprintf(&sb, "# TYPE %s %s\n", c.name, TypeCounter)
			var keys []string
			for k := range c.values {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				val := c.values[k]
				if k == "" {
					fmt.Fprintf(&sb, "%s %g\n", c.name, val)
				} else {
					fmt.Fprintf(&sb, "%s{%s} %g\n", c.name, k, val)
				}
			}
		}
		c.mu.RUnlock()
	}

	var gaugeNames []string
	for name := range r.gauges {
		gaugeNames = append(gaugeNames, name)
	}
	sort.Strings(gaugeNames)

	for _, name := range gaugeNames {
		g := r.gauges[name]
		g.mu.RLock()
		if len(g.values) > 0 {
			fmt.Fprintf(&sb, "# HELP %s %s\n", g.name, g.help)
			fmt.Fprintf(&sb, "# TYPE %s %s\n", g.name, TypeGauge)
			var keys []string
			for k := range g.values {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				val := g.values[k]
				if k == "" {
					fmt.Fprintf(&sb, "%s %g\n", g.name, val)
				} else {
					fmt.Fprintf(&sb, "%s{%s} %g\n", g.name, k, val)
				}
			}
		}
		g.mu.RUnlock()
	}

	var histNames []string
	for name := range r.histograms {
		histNames = append(histNames, name)
	}
	sort.Strings(histNames)

	for _, name := range histNames {
		h := r.histograms[name]
		h.mu.RLock()
		if len(h.totals) > 0 {
			fmt.Fprintf(&sb, "# HELP %s %s\n", h.name, h.help)
			fmt.Fprintf(&sb, "# TYPE %s %s\n", h.name, TypeHistogram)
			var keys []string
			for k := range h.totals {
				keys = append(keys, k)
			}
			sort.Strings(keys)

			for _, k := range keys {
				total := h.totals[k]
				sum := h.sums[k]
				bucketCounts := h.counts[k]

				for _, b := range h.buckets {
					count := bucketCounts[b]
					bucketLabel := fmt.Sprintf(`le="%g"`, b)
					if k != "" {
						bucketLabel = k + "," + bucketLabel
					}
					fmt.Fprintf(&sb, "%s_bucket{%s} %d\n", h.name, bucketLabel, count)
				}

				infLabel := `le="+Inf"`
				if k != "" {
					infLabel = k + "," + infLabel
				}
				fmt.Fprintf(&sb, "%s_bucket{%s} %d\n", h.name, infLabel, total)
				if k != "" {
					fmt.Fprintf(&sb, "%s_sum{%s} %g\n", h.name, k, sum)
					fmt.Fprintf(&sb, "%s_count{%s} %d\n", h.name, k, total)
				} else {
					fmt.Fprintf(&sb, "%s_sum %g\n", h.name, sum)
					fmt.Fprintf(&sb, "%s_count %d\n", h.name, total)
				}
			}
		}
		h.mu.RUnlock()
	}

	return sb.String()
}

func labelsToKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	var pairs []string
	for k, v := range labels {
		pairs = append(pairs, fmt.Sprintf(`%s="%s"`, k, v))
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}
