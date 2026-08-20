package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/stjudewashere/seonaut/internal/models"
	"github.com/stjudewashere/seonaut/internal/projectvalidation"
)

var (
	ErrProjectNotFound        = errors.New("api project not found")
	ErrIdempotencyKeyRequired = errors.New("idempotency key required")
	ErrIdempotencyConflict    = errors.New("idempotency key reused with different request")
	ErrInvalidProject         = errors.New("invalid project")
)

type ProjectInput struct {
	URL                string `json:"url"`
	IgnoreRobotsTxt    bool   `json:"ignore_robots_txt,omitempty"`
	FollowNofollow     bool   `json:"follow_nofollow,omitempty"`
	IncludeNoindex     bool   `json:"include_noindex,omitempty"`
	CrawlSitemap       bool   `json:"crawl_sitemap,omitempty"`
	AllowSubdomains    bool   `json:"allow_subdomains,omitempty"`
	BasicAuth          bool   `json:"basic_auth,omitempty"`
	CheckExternalLinks bool   `json:"check_external_links,omitempty"`
	Archive            bool   `json:"archive,omitempty"`
	UserAgent          string `json:"user_agent"`
}

type ProjectPatch struct {
	IgnoreRobotsTxt    *bool   `json:"ignore_robots_txt,omitempty"`
	FollowNofollow     *bool   `json:"follow_nofollow,omitempty"`
	IncludeNoindex     *bool   `json:"include_noindex,omitempty"`
	CrawlSitemap       *bool   `json:"crawl_sitemap,omitempty"`
	AllowSubdomains    *bool   `json:"allow_subdomains,omitempty"`
	BasicAuth          *bool   `json:"basic_auth,omitempty"`
	CheckExternalLinks *bool   `json:"check_external_links,omitempty"`
	Archive            *bool   `json:"archive,omitempty"`
	UserAgent          *string `json:"user_agent,omitempty"`
}

type Project struct {
	ID         string `json:"id"`
	ExternalID string `json:"external_id"`
	TenantID   string `json:"-"`
	ProjectInput
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ProjectService interface {
	PutProject(context.Context, Principal, string, string, ProjectInput) (Project, bool, error)
	ListProjects(context.Context, Principal) ([]Project, error)
	GetProject(context.Context, Principal, string) (Project, error)
	PatchProject(context.Context, Principal, string, ProjectPatch) (Project, error)
}

type ProjectStore interface {
	PutProject(context.Context, Principal, string, string, string, models.Project) (Project, bool, error)
	ListProjects(context.Context, Principal) ([]Project, error)
	GetProject(context.Context, Principal, string) (Project, error)
	PatchProject(context.Context, Principal, string, models.Project) (Project, error)
}

type ProjectManager struct{ Store ProjectStore }

func (m ProjectManager) PutProject(ctx context.Context, principal Principal, externalID, idempotencyKey string, input ProjectInput) (Project, bool, error) {
	externalID, idempotencyKey = strings.TrimSpace(externalID), strings.TrimSpace(idempotencyKey)
	if m.Store == nil || externalID == "" || len(externalID) > 191 {
		return Project{}, false, ErrInvalidProject
	}
	if idempotencyKey == "" || len(idempotencyKey) > 191 {
		return Project{}, false, ErrIdempotencyKeyRequired
	}
	model := input.model()
	if err := projectvalidation.Prepare(&model); err != nil {
		return Project{}, false, ErrInvalidProject
	}
	input = inputFromModel(model)
	hash, err := HashProjectPut(externalID, input)
	if err != nil {
		return Project{}, false, err
	}
	return m.Store.PutProject(ctx, principal, externalID, idempotencyKey, hash, model)
}

func (m ProjectManager) ListProjects(ctx context.Context, principal Principal) ([]Project, error) {
	if m.Store == nil {
		return nil, ErrProjectNotFound
	}
	return m.Store.ListProjects(ctx, principal)
}

func (m ProjectManager) GetProject(ctx context.Context, principal Principal, projectID string) (Project, error) {
	if m.Store == nil {
		return Project{}, ErrProjectNotFound
	}
	return m.Store.GetProject(ctx, principal, projectID)
}

func (m ProjectManager) PatchProject(ctx context.Context, principal Principal, projectID string, patch ProjectPatch) (Project, error) {
	if m.Store == nil {
		return Project{}, ErrProjectNotFound
	}
	current, err := m.Store.GetProject(ctx, principal, projectID)
	if err != nil {
		return Project{}, err
	}
	input := current.ProjectInput
	patch.apply(&input)
	model := input.model()
	if err := projectvalidation.Prepare(&model); err != nil {
		return Project{}, ErrInvalidProject
	}
	return m.Store.PatchProject(ctx, principal, projectID, model)
}

func HashProjectPut(externalID string, input ProjectInput) (string, error) {
	model := input.model()
	if err := projectvalidation.Prepare(&model); err != nil {
		return "", ErrInvalidProject
	}
	normalized, err := json.Marshal(struct {
		ExternalID string       `json:"external_id"`
		Input      ProjectInput `json:"input"`
	}{ExternalID: strings.TrimSpace(externalID), Input: inputFromModel(model)})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(normalized)
	return hex.EncodeToString(hash[:]), nil
}

func (input ProjectInput) model() models.Project {
	return models.Project{
		URL: input.URL, IgnoreRobotsTxt: input.IgnoreRobotsTxt, FollowNofollow: input.FollowNofollow,
		IncludeNoindex: input.IncludeNoindex, CrawlSitemap: input.CrawlSitemap, AllowSubdomains: input.AllowSubdomains,
		BasicAuth: input.BasicAuth, CheckExternalLinks: input.CheckExternalLinks, Archive: input.Archive, UserAgent: input.UserAgent,
	}
}

func inputFromModel(project models.Project) ProjectInput {
	return ProjectInput{
		URL: project.URL, IgnoreRobotsTxt: project.IgnoreRobotsTxt, FollowNofollow: project.FollowNofollow,
		IncludeNoindex: project.IncludeNoindex, CrawlSitemap: project.CrawlSitemap, AllowSubdomains: project.AllowSubdomains,
		BasicAuth: project.BasicAuth, CheckExternalLinks: project.CheckExternalLinks, Archive: project.Archive, UserAgent: project.UserAgent,
	}
}

func (patch ProjectPatch) apply(input *ProjectInput) {
	if patch.IgnoreRobotsTxt != nil {
		input.IgnoreRobotsTxt = *patch.IgnoreRobotsTxt
	}
	if patch.FollowNofollow != nil {
		input.FollowNofollow = *patch.FollowNofollow
	}
	if patch.IncludeNoindex != nil {
		input.IncludeNoindex = *patch.IncludeNoindex
	}
	if patch.CrawlSitemap != nil {
		input.CrawlSitemap = *patch.CrawlSitemap
	}
	if patch.AllowSubdomains != nil {
		input.AllowSubdomains = *patch.AllowSubdomains
	}
	if patch.BasicAuth != nil {
		input.BasicAuth = *patch.BasicAuth
	}
	if patch.CheckExternalLinks != nil {
		input.CheckExternalLinks = *patch.CheckExternalLinks
	}
	if patch.Archive != nil {
		input.Archive = *patch.Archive
	}
	if patch.UserAgent != nil {
		input.UserAgent = *patch.UserAgent
	}
}

func (s *server) putProject(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireProjectScope(w, r, ScopeProjectsWrite, true)
	if !ok {
		return
	}
	var input ProjectInput
	if !decodeStrictJSON(w, r, &input) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The project request is invalid")
		return
	}
	project, replayed, err := s.deps.Projects.PutProject(r.Context(), principal, r.PathValue("external_project_id"), r.Header.Get("Idempotency-Key"), input)
	if err != nil {
		writeProjectError(w, r, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeJSON(w, status, map[string]any{"data": project, "request_id": requestIDFrom(r.Context())})
}

func (s *server) listProjects(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireProjectScope(w, r, ScopeProjectsRead, false)
	if !ok {
		return
	}
	projects, err := s.deps.Projects.ListProjects(r.Context(), principal)
	if err != nil {
		writeProjectError(w, r, err)
		return
	}
	sort.Slice(projects, func(i, j int) bool {
		if projects[i].CreatedAt.Equal(projects[j].CreatedAt) {
			return projects[i].ID < projects[j].ID
		}
		return projects[i].CreatedAt.Before(projects[j].CreatedAt)
	})
	writeJSON(w, http.StatusOK, map[string]any{"data": projects, "page": map[string]any{"next_cursor": nil, "limit": 100}, "request_id": requestIDFrom(r.Context())})
}

func (s *server) getProject(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireProjectScope(w, r, ScopeProjectsRead, false)
	if !ok {
		return
	}
	project, err := s.deps.Projects.GetProject(r.Context(), principal, r.PathValue("project_id"))
	if err != nil {
		writeProjectError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": project, "request_id": requestIDFrom(r.Context())})
}

func (s *server) patchProject(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireProjectScope(w, r, ScopeProjectsWrite, true)
	if !ok {
		return
	}
	var patch ProjectPatch
	if !decodeStrictJSON(w, r, &patch) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The project request is invalid")
		return
	}
	project, err := s.deps.Projects.PatchProject(r.Context(), principal, r.PathValue("project_id"), patch)
	if err != nil {
		writeProjectError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": project, "request_id": requestIDFrom(r.Context())})
}

func (s *server) requireProjectScope(w http.ResponseWriter, r *http.Request, scope string, write bool) (Principal, bool) {
	principal, err := s.authenticate(r)
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "Authentication is required")
		return Principal{}, false
	}
	allowedKind := principal.Kind == KeyTenant || !write && principal.Kind == KeyReadOnly
	if !allowedKind || principal.TenantID == "" || !principal.Scopes.Has(scope) {
		writeError(w, r, http.StatusForbidden, "scope_forbidden", "The credential cannot access this resource")
		return Principal{}, false
	}
	if s.deps.Projects == nil {
		writeError(w, r, http.StatusServiceUnavailable, "service_unavailable", "Project management is unavailable")
		return Principal{}, false
	}
	return principal, true
}

func writeProjectError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrProjectNotFound):
		writeError(w, r, http.StatusNotFound, "project_not_found", "Project was not found")
	case errors.Is(err, ErrIdempotencyKeyRequired):
		writeError(w, r, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key is required")
	case errors.Is(err, ErrIdempotencyConflict):
		writeError(w, r, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was reused with a different request")
	case errors.Is(err, ErrInvalidProject), errors.Is(err, projectvalidation.ErrProtocolNotSupported), errors.Is(err, projectvalidation.ErrUserAgent):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The project request is invalid")
	default:
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The request could not be completed")
	}
}
