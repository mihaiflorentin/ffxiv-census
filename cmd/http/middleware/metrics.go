package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/metrics"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// MetricsWithRegistry creates a middleware tracking HTTP requests with StatsD and Prometheus.
func MetricsWithRegistry(client contract.StatsdClient, registry *metrics.Registry) func(next http.Handler) http.Handler {
	var httpRequestsTotal *metrics.Counter
	var httpRequestDuration *metrics.Histogram

	if registry != nil {
		httpRequestsTotal = registry.NewCounter(
			"http_requests_total",
			"Total number of HTTP requests processed partitioned by method, path, and status code",
		)
		buckets := []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0}
		httpRequestDuration = registry.NewHistogram(
			"http_request_duration_seconds",
			"Histogram of HTTP request latencies partitioned by method and path",
			buckets,
		)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			wrappedWriter := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

			next.ServeHTTP(wrappedWriter, r)

			duration := time.Since(start)

			if client != nil {
				segment := strings.Trim(strings.ReplaceAll(r.URL.Path, "/", "_"), "_")
				if segment == "" {
					segment = "root"
				}
				client.Timing("http."+segment, duration)
			}

			if registry != nil && httpRequestsTotal != nil && httpRequestDuration != nil {
				statusStr := strconv.Itoa(wrappedWriter.statusCode)
				path := r.URL.Path
				if path == "" {
					path = "/"
				}
				labels := map[string]string{
					"method": r.Method,
					"path":   path,
					"status": statusStr,
				}
				httpRequestsTotal.Inc(labels)

				durationLabels := map[string]string{
					"method": r.Method,
					"path":   path,
				}
				httpRequestDuration.Observe(durationLabels, duration.Seconds())
			}
		})
	}
}
