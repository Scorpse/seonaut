package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthReturnsReadinessWithoutBuildMetadata(t *testing.T) {
	h := NewHandler(Dependencies{
		Ready: func(context.Context) error { return nil },
		Build: BuildInfo{ForkVersion: "0.1.0", ForkRevision: "fork-sha", UpstreamRevision: "upstream-sha", SchemaVersion: "76"},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.Header.Set("X-Request-ID", "req-health")
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" || body["request_id"] != "req-health" {
		t.Fatalf("body = %#v", body)
	}
	for _, forbidden := range []string{"fork_version", "fork_revision", "upstream_revision", "schema_version"} {
		if _, ok := body[forbidden]; ok {
			t.Fatalf("health leaked %s: %#v", forbidden, body)
		}
	}
}

func TestHealthReturnsServiceUnavailableWhenDatabaseIsNotReady(t *testing.T) {
	h := NewHandler(Dependencies{Ready: func(context.Context) error { return errors.New("db unavailable") }})
	res := httptest.NewRecorder()

	h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusServiceUnavailable)
	}
	assertErrorCode(t, res, "service_unavailable")
}

func TestRequestIDAcceptsSafeCorrelationTokensAndRegeneratesUnsafeValues(t *testing.T) {
	handler := NewHandler(Dependencies{})
	for _, test := range []struct {
		name     string
		supplied string
		kept     bool
	}{
		{name: "safe", supplied: "client.trace-01", kept: true},
		{name: "oversized", supplied: strings.Repeat("a", 81)},
		{name: "credential shaped", supplied: "snk_test_public.secret"},
		{name: "non ascii", supplied: "client-é"},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
			req.Header.Set("X-Request-ID", test.supplied)
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			got := res.Header().Get("X-Request-ID")
			if test.kept && got != test.supplied {
				t.Fatalf("safe request ID changed: %q", got)
			}
			if !test.kept && (got == test.supplied || !strings.HasPrefix(got, "req_") || len(got) != 36) {
				t.Fatalf("unsafe request ID not regenerated: %q", got)
			}
		})
	}
}

func TestMetaRequiresNonRootMetaReadPrincipal(t *testing.T) {
	tests := []struct {
		name       string
		principal  Principal
		authErr    error
		wantStatus int
		wantCode   string
	}{
		{name: "missing key", authErr: ErrUnauthenticated, wantStatus: http.StatusUnauthorized, wantCode: "unauthorized"},
		{name: "root is structurally forbidden", principal: Principal{Kind: KeyRoot, Scopes: NewScopeSet(ScopeMetaRead)}, wantStatus: http.StatusForbidden, wantCode: "scope_forbidden"},
		{name: "tenant missing scope", principal: Principal{Kind: KeyTenant}, wantStatus: http.StatusForbidden, wantCode: "scope_forbidden"},
		{name: "tenant with scope", principal: Principal{Kind: KeyTenant, Scopes: NewScopeSet(ScopeMetaRead)}, wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(Dependencies{
				Ready: func(context.Context) error { return nil },
				Authenticate: func(context.Context, string) (Principal, error) {
					return tt.principal, tt.authErr
				},
				Build: BuildInfo{ForkVersion: "0.1.0", ForkRevision: "fork-sha", UpstreamRevision: "upstream-sha", SchemaVersion: "76"},
			})
			req := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
			req.Header.Set("Authorization", "Bearer test-key")
			res := httptest.NewRecorder()

			h.ServeHTTP(res, req)

			if res.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", res.Code, tt.wantStatus, res.Body.String())
			}
			if tt.wantCode != "" {
				assertErrorCode(t, res, tt.wantCode)
			}
		})
	}
}

func TestMetaReturnsBuildProvenanceAndCapabilities(t *testing.T) {
	h := NewHandler(Dependencies{
		Ready: func(context.Context) error { return nil },
		Authenticate: func(context.Context, string) (Principal, error) {
			return Principal{Kind: KeyReadOnly, Scopes: NewScopeSet(ScopeMetaRead)}, nil
		},
		Build: BuildInfo{ForkVersion: "0.1.0", ForkRevision: "fork-sha", UpstreamRevision: "upstream-sha", SchemaVersion: "76"},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", res.Code, res.Body.String())
	}
	var body struct {
		ForkVersion      string   `json:"fork_version"`
		ForkRevision     string   `json:"fork_revision"`
		UpstreamRevision string   `json:"upstream_revision"`
		SchemaVersion    string   `json:"schema_version"`
		Capabilities     []string `json:"capabilities"`
		RequestID        string   `json:"request_id"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ForkVersion != "0.1.0" || body.ForkRevision != "fork-sha" || body.UpstreamRevision != "upstream-sha" || body.SchemaVersion != "76" {
		t.Fatalf("unexpected provenance: %#v", body)
	}
	wantCapabilities := []string{"health", "meta", "openapi", "key_management", "tenant_provisioning", "projects", "crawls", "findings", "exports", "audit", "rate_limits", "ssrf_protection"}
	if len(body.Capabilities) != len(wantCapabilities) {
		t.Fatalf("capabilities = %#v", body.Capabilities)
	}
	for i, capability := range wantCapabilities {
		if body.Capabilities[i] != capability {
			t.Fatalf("capabilities = %#v", body.Capabilities)
		}
	}
	if body.RequestID == "" || res.Header().Get("X-Request-ID") != body.RequestID {
		t.Fatalf("request id missing or mismatched: header=%q body=%q", res.Header().Get("X-Request-ID"), body.RequestID)
	}
}

func assertErrorCode(t *testing.T, res *httptest.ResponseRecorder, want string) {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != want {
		t.Fatalf("error code = %q, want %q; body=%s", body.Error.Code, want, res.Body.String())
	}
	if body.RequestID == "" || res.Header().Get("X-Request-ID") != body.RequestID {
		t.Fatalf("request id missing or mismatched: header=%q body=%q", res.Header().Get("X-Request-ID"), body.RequestID)
	}
}
