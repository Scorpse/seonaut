package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stjudewashere/seonaut/internal/api"
)

func TestRegisterAPIRoutesMountsVersionedHandlerWithoutCapturingHTMLRoutes(t *testing.T) {
	mux := http.NewServeMux()
	registerAPIRoutes(mux, api.Dependencies{Ready: func(context.Context) error { return nil }})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	health := httptest.NewRecorder()
	mux.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d; body=%s", health.Code, health.Body.String())
	}

	html := httptest.NewRecorder()
	mux.ServeHTTP(html, httptest.NewRequest(http.MethodGet, "/", nil))
	if html.Code != http.StatusNoContent {
		t.Fatalf("html status = %d, want %d", html.Code, http.StatusNoContent)
	}
}
