package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	roothandler "github.com/mihaiflorentin88/ffxiv-census/cmd/http/app/root/handler"
	"github.com/mihaiflorentin88/ffxiv-census/container"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/metrics"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// metricsShutdownTimeout bounds how long stopping the metrics endpoint can take.
const metricsShutdownTimeout = 5 * time.Second

// startMetricsServer serves /metrics from the shared Prometheus registry on the
// given port, so counters recorded in a worker process can be scraped. A port
// of 0 disables the endpoint. The returned stop function is always safe to call.
//
// The endpoint is deliberately independent of the worker's signal context: it
// keeps serving while in-flight jobs finish and the result writer flushes, and
// callers defer stop first so it runs last, never delaying or reordering that
// sequence.
func startMetricsServer(port int, logger contract.Logger) (func(), error) {
	if port <= 0 {
		return func() {}, nil
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, fmt.Errorf("listen on metrics port %d: %w", port, err)
	}
	return serveMetrics(ln, container.Load.PrometheusRegistry(), logger), nil
}

// serveMetrics serves /metrics on ln until the returned stop function is
// called. Stop waits at most metricsShutdownTimeout before closing connections.
func serveMetrics(ln net.Listener, registry *metrics.Registry, logger contract.Logger) func() {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", roothandler.NewMetricsController(registry).Metrics)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && logger != nil {
			logger.ErrorContext(context.Background(), "metrics.serve_error", slog.Any("error", err))
		}
	}()
	if logger != nil {
		logger.InfoContext(context.Background(), "metrics.listening", slog.String("addr", ln.Addr().String()))
	}

	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), metricsShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			_ = srv.Close()
		}
	}
}
