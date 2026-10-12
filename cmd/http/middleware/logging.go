package middleware

import (
	"bytes"
	"io"
	"log"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/logging"
)

const maxLogBody = 4 << 10

var logBufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

func init() {
	log.SetFlags(0)
}

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
	body       *bytes.Buffer
}

func newLoggingResponseWriter(w http.ResponseWriter) *loggingResponseWriter {
	buf := logBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	return &loggingResponseWriter{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
		body:           buf,
	}
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.statusCode = code
	lrw.ResponseWriter.WriteHeader(code)
}

func (lrw *loggingResponseWriter) Write(b []byte) (int, error) {
	n, err := lrw.ResponseWriter.Write(b)
	if lrw.body.Len() < maxLogBody {
		toWrite := n
		if lrw.body.Len()+n > maxLogBody {
			toWrite = maxLogBody - lrw.body.Len()
		}
		lrw.body.Write(b[:toWrite])
		if lrw.body.Len() == maxLogBody {
			lrw.body.WriteString("…[truncated]")
		}
	}
	return n, err
}

func (lrw *loggingResponseWriter) finish() {
	lrw.body.Reset()
	logBufPool.Put(lrw.body)
}

func LoggingSimple(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		currentTime := time.Now().Format("2006-01-02 15:04:05")

		var reqBody []byte
		if r.Body != nil {
			var buf bytes.Buffer
			tee := io.TeeReader(r.Body, &buf)
			tmp, _ := io.ReadAll(tee)
			if len(tmp) > maxLogBody {
				reqBody = append(tmp[:maxLogBody], []byte("…[truncated]")...)
			} else {
				reqBody = tmp
			}
			r.Body = io.NopCloser(&buf)
		}

		lrw := newLoggingResponseWriter(w)
		next.ServeHTTP(lrw, r)
		duration := time.Since(start)
		logging.Logger.InfoContext(
			r.Context(),
			"HTTP",
			slog.String("timestamp", currentTime),
			slog.String("method", r.Method),
			slog.String("url", r.URL.String()),
			slog.Int("statusCode", lrw.statusCode),
			slog.Float64("durationSeconds", duration.Seconds()),
			slog.String("requestBody", string(reqBody)),
			slog.String("remoteAddress", r.RemoteAddr),
			slog.String("agent", r.UserAgent()),
			slog.String("responseBody", func() string {
				if lrw.statusCode >= 300 {
					return lrw.body.String()
				}
				return ""
			}()),
		)
		lrw.finish()
	})
}

func LoggingColored(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		currentTime := time.Now().Format("2006-01-02 15:04:05")

		var reqBody []byte
		if r.Body != nil {
			limitReader := io.LimitReader(r.Body, 64*1024)
			reqBody, _ = io.ReadAll(limitReader)
			r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(reqBody), r.Body))
		}

		lrw := newLoggingResponseWriter(w)
		next.ServeHTTP(lrw, r)
		duration := time.Since(start)

		reset := "\033[0m"
		blue := "\033[34m"
		green := "\033[32m"
		yellow := "\033[33m"
		red := "\033[31m"

		var statusColor, icon string
		switch {
		case lrw.statusCode >= 200 && lrw.statusCode < 300:
			statusColor = green
			icon = "🟢"
		case lrw.statusCode >= 300 && lrw.statusCode < 400:
			statusColor = blue
			icon = "🔵"
		case lrw.statusCode >= 400:
			statusColor = red
			icon = "🔴"
		default:
			statusColor = yellow
			icon = "🟡"
		}

		log.Println("---------------------------------------------")
		log.Printf("%sDate:          %s %s", blue, reset, currentTime)
		log.Printf("%sRequest:     %s\n  Method:       %s\n  URL:          %s\n  RemoteAddr:   %s\n  Agent:        %s\n  Payload:      %s\n",
			blue, reset, r.Method, r.URL.String(), r.RemoteAddr, r.UserAgent(), string(reqBody))
		log.Printf("%sResponse:%s\n  Code:         %s%d %s%s\n  Duration:     %v",
			blue, reset, statusColor, lrw.statusCode, icon, reset, duration)
		if lrw.statusCode >= 300 {
			log.Printf("  %sResponse Body:      %s %s", red, reset, lrw.body.String())
		}
		log.Println("---------------------------------------------")
		lrw.finish()
	})
}
