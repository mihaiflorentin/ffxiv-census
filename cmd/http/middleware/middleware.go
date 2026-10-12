package middleware

import (
	"fmt"
	"net/http"
	"reflect"
	"runtime"
	"strings"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/logging"
)

type Middleware struct {
	middleware []func(handler http.Handler) http.Handler
}

func NewMiddleware() *Middleware {
	return &Middleware{}
}

func (m *Middleware) Register(middleware func(handler http.Handler) http.Handler) {
	m.middleware = append(m.middleware, middleware)
}

func (m *Middleware) Chain(handler http.Handler) http.Handler {
	for i := len(m.middleware) - 1; i >= 0; i-- {
		handler = m.middleware[i](handler)
		logging.Info("http.middleware", fmt.Sprintf("middleware %s started", m.getFuncName(m.middleware[i])))
	}
	return handler
}

func (m *Middleware) getFuncName(mw func(handler http.Handler) http.Handler) string {
	fnName := runtime.FuncForPC(reflect.ValueOf(mw).Pointer()).Name()
	slashParts := strings.Split(fnName, "/")
	lastSlash := slashParts[len(slashParts)-1]
	dotParts := strings.Split(lastSlash, ".")
	return strings.TrimSuffix(dotParts[len(dotParts)-1], "-fm")
}
