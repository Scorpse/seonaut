package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/stjudewashere/seonaut/internal/buildinfo"
)

type BuildInfo = buildinfo.Info

type KeyKind string

const (
	KeyRoot     KeyKind = "root"
	KeyPlatform KeyKind = "platform"
	KeyTenant   KeyKind = "tenant"
	KeyReadOnly KeyKind = "read_only"

	ScopeMetaRead = "meta:read"

	ScopePlatformKeysCreate = "platform_keys:create"
	ScopePlatformKeysList   = "platform_keys:list"
	ScopePlatformKeysRotate = "platform_keys:rotate"
	ScopePlatformKeysRevoke = "platform_keys:revoke"
	ScopeTenantsProvision   = "tenants:provision"
	ScopeTenantKeysCreate   = "tenant_keys:create"
	ScopeTenantKeysRotate   = "tenant_keys:rotate"
	ScopeTenantKeysRevoke   = "tenant_keys:revoke"
	ScopeProjectsRead       = "projects:read"
	ScopeProjectsWrite      = "projects:write"
	ScopeCrawlsRead         = "crawls:read"
	ScopeCrawlsRun          = "crawls:run"
	ScopeCrawlsCancel       = "crawls:cancel"
	ScopeFindingsRead       = "findings:read"
	ScopeExportsRead        = "exports:read"
	ScopeKeysManage         = "keys:manage"
)

var ErrUnauthenticated = errors.New("unauthenticated")

type ScopeSet map[string]struct{}

func NewScopeSet(scopes ...string) ScopeSet {
	set := make(ScopeSet, len(scopes))
	for _, scope := range scopes {
		set[scope] = struct{}{}
	}
	return set
}

func (s ScopeSet) Has(scope string) bool {
	_, ok := s[scope]
	return ok
}

type Principal struct {
	Kind      KeyKind
	KeyID     string
	TenantID  string
	ProjectID string
	Scopes    ScopeSet
}

type AuthenticateFunc func(context.Context, string) (Principal, error)

type Dependencies struct {
	Ready        func(context.Context) error
	Authenticate AuthenticateFunc
	Build        BuildInfo
	PlatformKeys PlatformKeyService
	Tenants      TenantService
	Projects     ProjectService
	Crawls       CrawlService
	Findings     FindingService
	CursorSecret []byte
	Exports      ExportService
	RateLimiter  RequestLimiter
	Audit        AuditSink
	ExportSlots  *ConcurrencyBudget
}

type server struct {
	deps Dependencies
	mux  *http.ServeMux
}

func NewHandler(deps Dependencies) http.Handler {
	mux := http.NewServeMux()
	RegisterRoutes(mux, deps)
	return mux
}

func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	s := &server{deps: deps, mux: mux}
	handlers := map[string]http.HandlerFunc{
		"health.read": s.health, "openapi.read": s.openAPI, "meta.read": s.meta,
		"platform_keys.create": s.createPlatformKey, "platform_keys.list": s.listPlatformKeys, "platform_keys.rotate": s.rotatePlatformKey, "platform_keys.revoke": s.revokePlatformKey,
		"tenants.provision": s.provisionTenant, "tenants.read": s.getTenant, "tenant_keys.create_initial": s.issueInitialTenantKey,
		"tenant_keys.create": s.issueDelegatedKey, "tenant_keys.list": s.listTenantKeys, "tenant_keys.rotate": s.rotateTenantKey, "tenant_keys.revoke": s.revokeTenantKey,
		"projects.put": s.putProject, "projects.list": s.listProjects, "projects.read": s.getProject, "projects.patch": s.patchProject,
		"crawls.start": s.startCrawl, "crawls.list": s.listCrawls, "crawls.read": s.getCrawl, "crawls.cancel": s.cancelCrawl,
		"findings.issues": s.listIssues, "findings.pages": s.listPages, "findings.links": s.listLinks, "findings.resources": s.listResources,
		"exports.issues_csv": s.exportIssuesCSV, "exports.pages_csv": s.exportPagesCSV, "exports.resources_csv": s.exportResourcesCSV, "exports.sitemap": s.exportSitemap, "exports.archive": s.exportArchive,
	}
	for _, route := range openAPIRoutes {
		handler := handlers[route.action]
		if handler == nil {
			panic("API route has no handler: " + route.action)
		}
		pattern := strings.ToUpper(route.method) + " " + route.path
		if route.public {
			mux.Handle(pattern, requestIDMiddleware(handler))
			continue
		}
		s.register(pattern, route.action, route.class, handler)
	}
}

func (s *server) register(pattern, action string, class RateClass, handler http.HandlerFunc) {
	s.mux.Handle(pattern, requestIDMiddleware(s.operationalMiddleware(action, class, handler)))
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	if s.deps.Ready != nil {
		if err := s.deps.Ready(r.Context()); err != nil {
			writeError(w, r, http.StatusServiceUnavailable, "service_unavailable", "Service is not ready")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"request_id": requestIDFrom(r.Context()),
	})
}

func (s *server) meta(w http.ResponseWriter, r *http.Request) {
	principal, err := s.authenticate(r)
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "Authentication is required")
		return
	}
	if principal.Kind == KeyRoot || !principal.Scopes.Has(ScopeMetaRead) {
		writeError(w, r, http.StatusForbidden, "scope_forbidden", "The credential cannot access this resource")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"fork_version":      s.deps.Build.ForkVersion,
		"fork_revision":     s.deps.Build.ForkRevision,
		"upstream_revision": s.deps.Build.UpstreamRevision,
		"schema_version":    s.deps.Build.SchemaVersion,
		"capabilities":      []string{"health", "meta", "openapi", "key_management", "tenant_provisioning", "projects", "crawls", "findings", "exports", "audit", "rate_limits", "ssrf_protection"},
		"request_id":        requestIDFrom(r.Context()),
	})
}

func (s *server) authenticate(r *http.Request) (Principal, error) {
	if result, ok := r.Context().Value(authenticationResultKey{}).(authenticationResult); ok {
		return result.principal, result.err
	}
	if s.deps.Authenticate == nil {
		return Principal{}, ErrUnauthenticated
	}
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if authorization == "" {
		return Principal{}, ErrUnauthenticated
	}
	return s.deps.Authenticate(r.Context(), authorization)
}

type requestIDKey struct{}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if !validRequestID(requestID) {
			requestID = newRequestID()
		}
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, requestID)))
	})
}

func validRequestID(value string) bool {
	if value == "" || len(value) > 80 {
		return false
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "snk_") || strings.Contains(lower, "secret") || strings.Contains(lower, "bearer") {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func newRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return "req_" + hex.EncodeToString(raw[:])
}

func requestIDFrom(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDKey{}).(string)
	return requestID
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
			"details": map[string]any{},
		},
		"request_id": requestIDFrom(r.Context()),
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
