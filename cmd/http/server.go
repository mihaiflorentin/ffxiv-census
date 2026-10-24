package http

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/pprof"
	"runtime"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/cmd/http/middleware"
	"github.com/mihaiflorentin88/ffxiv-census/container"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/logging"
)

// RegisterRoutes mounts every route on mux and returns the handler to serve.
func RegisterRoutes(mux *http.ServeMux) http.Handler {
	router := NewRouter()
	RegisterSwagger(mux)
	return router.RegisterRoutes(mux)
}

func StartServer(ctx context.Context, port int, poolSize int, certFile, keyFile string, profile bool, maxRequests uint64) error {
	runtime.GOMAXPROCS(poolSize)

	mux := http.NewServeMux()
	routes := RegisterRoutes(mux)

	mw := middleware.NewMiddleware()
	mw.Register(middleware.Recoverer(logging.Logger))
	initLoggingMiddleware(mw)
	mw.Register(middleware.NewMaxReqMiddleware(maxRequests).Handler)
	initMetricsMiddleWare(mw)

	handler := mw.Chain(routes)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	if profile {
		startPprofServer()
	}

	logging.Info("http.server", fmt.Sprintf("listening on http://0.0.0.0:%d", port))

	errCh := make(chan error, 1)
	go func() {
		var err error
		if certFile != "" && keyFile != "" {
			err = srv.ListenAndServeTLS(certFile, keyFile)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return <-errCh
	case err := <-errCh:
		return err
	}
}

func initLoggingMiddleware(mw *middleware.Middleware) {
	cfg := container.Load.Config().Logging
	serverDefault := logging.LoggerTypeColor
	if cfg != nil && cfg.ServerDefault != "" {
		serverDefault = cfg.ServerDefault
	}

	switch serverDefault {
	case logging.LoggerTypeColor, "color-simple":
		mw.Register(middleware.LoggingColored)
	case logging.LoggerTypeJson, logging.LoggerTypePrettyJson:
		mw.Register(middleware.LoggingSimple)
	default:
		mw.Register(middleware.LoggingColored)
	}
}
func initMetricsMiddleWare(mw *middleware.Middleware) {
	statsd := container.Load.Statsd()
	registry := container.Load.PrometheusRegistry()
	mw.Register(middleware.MetricsWithRegistry(statsd, registry))
}

func startPprofServer() {
	log.Println("🟢 pprof listening on http://localhost:6060")
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", http.HandlerFunc(pprof.Index))
	mux.HandleFunc("/debug/pprof/cmdline", http.HandlerFunc(pprof.Cmdline))
	mux.HandleFunc("/debug/pprof/profile", http.HandlerFunc(pprof.Profile))
	mux.HandleFunc("/debug/pprof/symbol", http.HandlerFunc(pprof.Symbol))
	mux.HandleFunc("/debug/pprof/trace", http.HandlerFunc(pprof.Trace))
	go func() {
		if err := http.ListenAndServe("localhost:6060", mux); err != nil {
			log.Printf("🔴 pprof server error: %v", err)
		}
	}()
}
