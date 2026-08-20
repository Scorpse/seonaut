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
	mux.Handle("GET /api/v1/health", requestIDMiddleware(http.HandlerFunc(s.health)))
	mux.Handle("GET /api/v1/openapi.json", requestIDMiddleware(http.HandlerFunc(s.openAPI)))
	s.register("GET /api/v1/meta", "meta.read", RateRead, s.meta)
	s.register("POST /api/v1/root/platform-keys", "platform_keys.create", RateNone, s.createPlatformKey)
	s.register("GET /api/v1/root/platform-keys", "platform_keys.list", RateRead, s.listPlatformKeys)
	s.register("POST /api/v1/root/platform-keys/{key_id}/rotate", "platform_keys.rotate", RateNone, s.rotatePlatformKey)
	s.register("POST /api/v1/root/platform-keys/{key_id}/revoke", "platform_keys.revoke", RateNone, s.revokePlatformKey)
	s.register("PUT /api/v1/tenants/{external_tenant_id}", "tenants.provision", RateNone, s.provisionTenant)
	s.register("GET /api/v1/tenants/{external_tenant_id}", "tenants.read", RateRead, s.getTenant)
	s.register("POST /api/v1/tenants/{external_tenant_id}/keys", "tenant_keys.create_initial", RateNone, s.issueInitialTenantKey)
	s.register("POST /api/v1/keys", "tenant_keys.create", RateNone, s.issueDelegatedKey)
	s.register("GET /api/v1/keys", "tenant_keys.list", RateRead, s.listTenantKeys)
	s.register("POST /api/v1/keys/{key_id}/rotate", "tenant_keys.rotate", RateNone, s.rotateTenantKey)
	s.register("POST /api/v1/keys/{key_id}/revoke", "tenant_keys.revoke", RateNone, s.revokeTenantKey)
	s.register("PUT /api/v1/projects/{external_project_id}", "projects.put", RateNone, s.putProject)
	s.register("GET /api/v1/projects", "projects.list", RateRead, s.listProjects)
	s.register("GET /api/v1/projects/{project_id}", "projects.read", RateRead, s.getProject)
	s.register("PATCH /api/v1/projects/{project_id}", "projects.patch", RateNone, s.patchProject)
	s.register("POST /api/v1/projects/{project_id}/crawls", "crawls.start", RateCrawl, s.startCrawl)
	s.register("GET /api/v1/projects/{project_id}/crawls", "crawls.list", RateRead, s.listCrawls)
	s.register("GET /api/v1/projects/{project_id}/crawls/{crawl_id}", "crawls.read", RateRead, s.getCrawl)
	s.register("POST /api/v1/projects/{project_id}/crawls/{crawl_id}/cancel", "crawls.cancel", RateNone, s.cancelCrawl)
	s.register("GET /api/v1/projects/{project_id}/crawls/{crawl_id}/issues", "findings.issues", RateRead, s.listIssues)
	s.register("GET /api/v1/projects/{project_id}/crawls/{crawl_id}/pages", "findings.pages", RateRead, s.listPages)
	s.register("GET /api/v1/projects/{project_id}/crawls/{crawl_id}/links", "findings.links", RateRead, s.listLinks)
	s.register("GET /api/v1/projects/{project_id}/crawls/{crawl_id}/resources", "findings.resources", RateRead, s.listResources)
	s.register("GET /api/v1/projects/{project_id}/crawls/{crawl_id}/exports/issues.csv", "exports.issues_csv", RateExport, s.exportIssuesCSV)
	s.register("GET /api/v1/projects/{project_id}/crawls/{crawl_id}/exports/pages.csv", "exports.pages_csv", RateExport, s.exportPagesCSV)
	s.register("GET /api/v1/projects/{project_id}/crawls/{crawl_id}/exports/resources.csv", "exports.resources_csv", RateExport, s.exportResourcesCSV)
	s.register("GET /api/v1/projects/{project_id}/crawls/{crawl_id}/exports/sitemap.xml", "exports.sitemap", RateExport, s.exportSitemap)
	s.register("GET /api/v1/projects/{project_id}/crawls/{crawl_id}/exports/archive.wacz", "exports.archive", RateExport, s.exportArchive)
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
		if requestID == "" {
			requestID = newRequestID()
		}
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, requestID)))
	})
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
