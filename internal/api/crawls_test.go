package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type memoryCrawls struct {
	crawls  map[string]APICrawl
	replays map[string]string
	active  map[string]string
}

func (m *memoryCrawls) StartCrawl(_ context.Context, principal Principal, projectID, idempotencyKey string) (APICrawl, bool, error) {
	if idempotencyKey == "" {
		return APICrawl{}, false, ErrIdempotencyKeyRequired
	}
	replayKey := principal.KeyID + ":" + idempotencyKey
	if crawlID, ok := m.replays[replayKey]; ok {
		return m.crawls[crawlID], true, nil
	}
	if _, ok := m.active[principal.TenantID+":"+projectID]; ok {
		return APICrawl{}, false, ErrCrawlAlreadyActive
	}
	crawl := APICrawl{ID: "crawl-" + projectID, TenantID: principal.TenantID, ProjectID: projectID, State: CrawlQueued, QueuedAt: time.Unix(1, 0).UTC()}
	m.crawls[crawl.ID] = crawl
	m.replays[replayKey] = crawl.ID
	m.active[principal.TenantID+":"+projectID] = crawl.ID
	return crawl, false, nil
}

func (m *memoryCrawls) ListCrawls(_ context.Context, principal Principal, projectID string) ([]APICrawl, error) {
	items := []APICrawl{}
	for _, crawl := range m.crawls {
		if crawl.TenantID == principal.TenantID && crawl.ProjectID == projectID && (principal.ProjectID == "" || principal.ProjectID == projectID) {
			items = append(items, crawl)
		}
	}
	if len(items) == 0 {
		return nil, ErrProjectNotFound
	}
	return items, nil
}

func (m *memoryCrawls) GetCrawl(_ context.Context, principal Principal, projectID, crawlID string) (APICrawl, error) {
	crawl, ok := m.crawls[crawlID]
	if !ok || crawl.TenantID != principal.TenantID || crawl.ProjectID != projectID || principal.ProjectID != "" && principal.ProjectID != projectID {
		return APICrawl{}, ErrCrawlNotFound
	}
	return crawl, nil
}

func (m *memoryCrawls) CancelCrawl(ctx context.Context, principal Principal, projectID, crawlID string) (APICrawl, error) {
	crawl, err := m.GetCrawl(ctx, principal, projectID, crawlID)
	if err != nil {
		return APICrawl{}, err
	}
	if crawl.State == CrawlSucceeded || crawl.State == CrawlFailed || crawl.State == CrawlCanceled {
		return crawl, nil
	}
	crawl.CancelRequested = true
	m.crawls[crawlID] = crawl
	return crawl, nil
}

func TestCrawlStartRequiresIdempotencyReplaysAndRejectsAnotherActiveCrawl(t *testing.T) {
	crawls := &memoryCrawls{crawls: map[string]APICrawl{}, replays: map[string]string{}, active: map[string]string{}}
	h := NewHandler(Dependencies{Authenticate: crawlAuthenticator, Crawls: crawls})
	start := func(key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project-a/crawls", nil)
		req.Header.Set("Authorization", "Bearer tenant-a")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		return res
	}
	if res := start(""); res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "idempotency_key_required") {
		t.Fatalf("missing key=%d %s", res.Code, res.Body.String())
	}
	if res := start("one"); res.Code != http.StatusAccepted || !strings.Contains(res.Body.String(), "crawl-project-a") {
		t.Fatalf("start=%d %s", res.Code, res.Body.String())
	}
	if res := start("one"); res.Code != http.StatusAccepted || res.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay=%d %s", res.Code, res.Body.String())
	}
	if res := start("two"); res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "crawl_already_active") {
		t.Fatalf("active=%d %s", res.Code, res.Body.String())
	}
}

func TestCrawlRoutesHideForeignResourcesAndConstrainProjectKeys(t *testing.T) {
	crawls := &memoryCrawls{crawls: map[string]APICrawl{
		"crawl-a": {ID: "crawl-a", TenantID: "tenant-a", ProjectID: "project-a", State: CrawlRunning},
		"crawl-b": {ID: "crawl-b", TenantID: "tenant-b", ProjectID: "project-b", State: CrawlSucceeded},
	}, replays: map[string]string{}, active: map[string]string{}}
	h := NewHandler(Dependencies{Authenticate: crawlAuthenticator, Crawls: crawls})
	tests := []struct {
		name, method, token, path string
		status                    int
		contains, absent          string
	}{
		{name: "tenant list", method: http.MethodGet, token: "tenant-a", path: "/api/v1/projects/project-a/crawls", status: http.StatusOK, contains: "crawl-a", absent: "crawl-b"},
		{name: "foreign list", method: http.MethodGet, token: "tenant-a", path: "/api/v1/projects/project-b/crawls", status: http.StatusNotFound, contains: "project_not_found"},
		{name: "foreign direct", method: http.MethodGet, token: "tenant-a", path: "/api/v1/projects/project-b/crawls/crawl-b", status: http.StatusNotFound, contains: "crawl_not_found"},
		{name: "bound read", method: http.MethodGet, token: "read-a", path: "/api/v1/projects/project-a/crawls/crawl-a", status: http.StatusOK, contains: "crawl-a"},
		{name: "bound foreign", method: http.MethodGet, token: "read-a", path: "/api/v1/projects/project-b/crawls/crawl-b", status: http.StatusNotFound, contains: "crawl_not_found"},
		{name: "platform denied", method: http.MethodGet, token: "platform", path: "/api/v1/projects/project-a/crawls", status: http.StatusForbidden, contains: "scope_forbidden"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set("Authorization", "Bearer "+tt.token)
			res := httptest.NewRecorder()
			h.ServeHTTP(res, req)
			if res.Code != tt.status || !strings.Contains(res.Body.String(), tt.contains) || tt.absent != "" && strings.Contains(res.Body.String(), tt.absent) {
				t.Fatalf("status/body=%d %s", res.Code, res.Body.String())
			}
		})
	}
}

func TestCrawlCancelIsIdempotentAndTerminalStateIsMonotonic(t *testing.T) {
	crawls := &memoryCrawls{crawls: map[string]APICrawl{
		"crawl-a":    {ID: "crawl-a", TenantID: "tenant-a", ProjectID: "project-a", State: CrawlRunning},
		"crawl-done": {ID: "crawl-done", TenantID: "tenant-a", ProjectID: "project-a", State: CrawlSucceeded},
	}, replays: map[string]string{}, active: map[string]string{}}
	h := NewHandler(Dependencies{Authenticate: crawlAuthenticator, Crawls: crawls})
	for _, crawlID := range []string{"crawl-a", "crawl-a", "crawl-done"} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project-a/crawls/"+crawlID+"/cancel", nil)
		req.Header.Set("Authorization", "Bearer tenant-a")
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != http.StatusAccepted {
			t.Fatalf("cancel %s=%d %s", crawlID, res.Code, res.Body.String())
		}
	}
	if crawls.crawls["crawl-done"].State != CrawlSucceeded {
		t.Fatal("terminal crawl state regressed")
	}
}

func crawlAuthenticator(_ context.Context, authorization string) (Principal, error) {
	switch authorization {
	case "Bearer tenant-a":
		return Principal{Kind: KeyTenant, KeyID: "key-a", TenantID: "tenant-a", Scopes: NewScopeSet(ScopeCrawlsRead, ScopeCrawlsRun, ScopeCrawlsCancel)}, nil
	case "Bearer read-a":
		return Principal{Kind: KeyReadOnly, KeyID: "read-a", TenantID: "tenant-a", ProjectID: "project-a", Scopes: NewScopeSet(ScopeCrawlsRead)}, nil
	case "Bearer platform":
		return Principal{Kind: KeyPlatform, KeyID: "platform", Scopes: NewScopeSet(ScopeCrawlsRead)}, nil
	default:
		return Principal{}, errors.New("unauthenticated")
	}
}
