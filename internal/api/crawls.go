package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

type CrawlState string

const (
	CrawlQueued    CrawlState = "queued"
	CrawlRunning   CrawlState = "running"
	CrawlSucceeded CrawlState = "succeeded"
	CrawlFailed    CrawlState = "failed"
	CrawlCanceled  CrawlState = "canceled"
)

var (
	ErrCrawlNotFound      = errors.New("api crawl not found")
	ErrCrawlAlreadyActive = errors.New("crawl already active")
)

type APICrawl struct {
	ID              string     `json:"id"`
	TenantID        string     `json:"-"`
	ProjectID       string     `json:"project_id"`
	State           CrawlState `json:"state"`
	CancelRequested bool       `json:"cancel_requested"`
	QueuedAt        time.Time  `json:"queued_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	FailureCode     string     `json:"failure_code,omitempty"`
	FailureMessage  string     `json:"failure_message,omitempty"`
	TotalURLs       int        `json:"total_urls"`
	TotalIssues     int        `json:"total_issues"`
}

type CrawlService interface {
	StartCrawl(context.Context, Principal, string, string) (APICrawl, bool, error)
	ListCrawls(context.Context, Principal, string) ([]APICrawl, error)
	GetCrawl(context.Context, Principal, string, string) (APICrawl, error)
	CancelCrawl(context.Context, Principal, string, string) (APICrawl, error)
}

func (s *server) startCrawl(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireCrawlScope(w, r, ScopeCrawlsRun, true)
	if !ok {
		return
	}
	crawl, replayed, err := s.deps.Crawls.StartCrawl(r.Context(), principal, r.PathValue("project_id"), strings.TrimSpace(r.Header.Get("Idempotency-Key")))
	if err != nil {
		writeCrawlError(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"data": crawl, "request_id": requestIDFrom(r.Context())})
}

func (s *server) listCrawls(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireCrawlScope(w, r, ScopeCrawlsRead, false)
	if !ok {
		return
	}
	projectID := r.PathValue("project_id")
	if principal.ProjectID != "" && principal.ProjectID != projectID {
		writeProjectError(w, r, ErrProjectNotFound)
		return
	}
	crawls, err := s.deps.Crawls.ListCrawls(r.Context(), principal, projectID)
	if err != nil {
		writeCrawlError(w, r, err)
		return
	}
	sort.Slice(crawls, func(i, j int) bool {
		if crawls[i].QueuedAt.Equal(crawls[j].QueuedAt) {
			return crawls[i].ID < crawls[j].ID
		}
		return crawls[i].QueuedAt.Before(crawls[j].QueuedAt)
	})
	writeJSON(w, http.StatusOK, map[string]any{"data": crawls, "page": map[string]any{"next_cursor": nil, "limit": 100}, "request_id": requestIDFrom(r.Context())})
}

func (s *server) getCrawl(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireCrawlScope(w, r, ScopeCrawlsRead, false)
	if !ok {
		return
	}
	projectID := r.PathValue("project_id")
	if principal.ProjectID != "" && principal.ProjectID != projectID {
		writeCrawlError(w, r, ErrCrawlNotFound)
		return
	}
	crawl, err := s.deps.Crawls.GetCrawl(r.Context(), principal, projectID, r.PathValue("crawl_id"))
	if err != nil {
		writeCrawlError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": crawl, "request_id": requestIDFrom(r.Context())})
}

func (s *server) cancelCrawl(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireCrawlScope(w, r, ScopeCrawlsCancel, true)
	if !ok {
		return
	}
	crawl, err := s.deps.Crawls.CancelCrawl(r.Context(), principal, r.PathValue("project_id"), r.PathValue("crawl_id"))
	if err != nil {
		writeCrawlError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"data": crawl, "request_id": requestIDFrom(r.Context())})
}

func (s *server) requireCrawlScope(w http.ResponseWriter, r *http.Request, scope string, write bool) (Principal, bool) {
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
	if s.deps.Crawls == nil {
		writeError(w, r, http.StatusServiceUnavailable, "service_unavailable", "Crawl management is unavailable")
		return Principal{}, false
	}
	return principal, true
}

func writeCrawlError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrProjectNotFound):
		writeProjectError(w, r, err)
	case errors.Is(err, ErrCrawlNotFound):
		writeError(w, r, http.StatusNotFound, "crawl_not_found", "Crawl was not found")
	case errors.Is(err, ErrIdempotencyKeyRequired):
		writeProjectError(w, r, err)
	case errors.Is(err, ErrIdempotencyConflict):
		writeProjectError(w, r, err)
	case errors.Is(err, ErrCrawlAlreadyActive):
		writeError(w, r, http.StatusConflict, "crawl_already_active", "A crawl is already active for this project")
	default:
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The request could not be completed")
	}
}
