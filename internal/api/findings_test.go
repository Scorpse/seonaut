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

type memoryFindings struct {
	after int64
}

func (m *memoryFindings) AuthorizeCrawl(_ context.Context, principal Principal, projectID, crawlID string) error {
	if principal.TenantID != "tenant-a" || projectID != "project-a" || crawlID != "crawl-a" || principal.ProjectID != "" && principal.ProjectID != projectID {
		return ErrCrawlNotFound
	}
	return nil
}

func (m *memoryFindings) ListIssues(_ context.Context, principal Principal, projectID, crawlID string, page PageRequest) (PageResult[IssueFinding], error) {
	if principal.TenantID != "tenant-a" || projectID != "project-a" || crawlID != "crawl-a" || principal.ProjectID != "" && principal.ProjectID != projectID {
		return PageResult[IssueFinding]{}, ErrCrawlNotFound
	}
	m.after = page.AfterID
	if page.AfterID == 0 {
		return PageResult[IssueFinding]{Items: []IssueFinding{{Code: "ERROR_EMPTY_TITLE", Severity: "alert", PageURL: "https://a.example/one", Evidence: map[string]any{}, Count: 1}}, NextAfterID: 11}, nil
	}
	return PageResult[IssueFinding]{Items: []IssueFinding{{Code: "ERROR_40x", Severity: "critical", PageURL: "https://a.example/missing", Evidence: map[string]any{"status_code": 404}, Count: 1}}}, nil
}

func (m *memoryFindings) ListPages(_ context.Context, principal Principal, projectID, crawlID string, page PageRequest) (PageResult[PageFinding], error) {
	if principal.TenantID != "tenant-a" || projectID != "project-a" || crawlID != "crawl-a" {
		return PageResult[PageFinding]{}, ErrCrawlNotFound
	}
	return PageResult[PageFinding]{Items: []PageFinding{{URL: "https://a.example/one", StatusCode: 200, Title: "One", Words: 7}}}, nil
}

func (m *memoryFindings) ListLinks(_ context.Context, principal Principal, projectID, crawlID string, page PageRequest) (PageResult[LinkFinding], error) {
	if principal.TenantID != "tenant-a" || projectID != "project-a" || crawlID != "crawl-a" {
		return PageResult[LinkFinding]{}, ErrCrawlNotFound
	}
	return PageResult[LinkFinding]{Items: []LinkFinding{{Kind: "internal", OriginURL: "https://a.example/one", DestinationURL: "https://a.example/two", Text: "Two"}}}, nil
}

func (m *memoryFindings) ListResources(_ context.Context, principal Principal, projectID, crawlID, resourceType string, page PageRequest) (PageResult[ResourceFinding], error) {
	if principal.TenantID != "tenant-a" || projectID != "project-a" || crawlID != "crawl-a" {
		return PageResult[ResourceFinding]{}, ErrCrawlNotFound
	}
	if resourceType == "" {
		resourceType = "image"
	}
	return PageResult[ResourceFinding]{Items: []ResourceFinding{{Type: resourceType, OriginURL: "https://a.example/one", URL: "https://a.example/logo.png", Alt: "Logo"}}}, nil
}

func TestFindingRoutesUseSignedOpaqueCursorsAndEnforceLimits(t *testing.T) {
	findings := &memoryFindings{}
	h := NewHandler(Dependencies{Authenticate: findingsAuthenticator, Findings: findings, CursorSecret: []byte("0123456789abcdef0123456789abcdef")})

	first := findingRequest(h, "/api/v1/projects/project-a/crawls/crawl-a/issues?limit=1", "tenant-a")
	if first.Code != http.StatusOK {
		t.Fatalf("first page status/body=%d %s", first.Code, first.Body.String())
	}
	var body struct {
		Data []IssueFinding `json:"data"`
		Page struct {
			NextCursor *string `json:"next_cursor"`
			Limit      int     `json:"limit"`
		} `json:"page"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.Data[0].Code != "ERROR_EMPTY_TITLE" || body.Page.NextCursor == nil || *body.Page.NextCursor == "" || strings.Contains(*body.Page.NextCursor, "11") {
		t.Fatalf("unexpected first page: %#v", body)
	}

	second := findingRequest(h, "/api/v1/projects/project-a/crawls/crawl-a/issues?limit=1&cursor="+*body.Page.NextCursor, "tenant-a")
	if second.Code != http.StatusOK || findings.after != 11 || !strings.Contains(second.Body.String(), "ERROR_40x") {
		t.Fatalf("second page status/body/after=%d %s %d", second.Code, second.Body.String(), findings.after)
	}

	tampered := *body.Page.NextCursor
	if tampered[len(tampered)-1] == 'a' {
		tampered = tampered[:len(tampered)-1] + "b"
	} else {
		tampered = tampered[:len(tampered)-1] + "a"
	}
	if res := findingRequest(h, "/api/v1/projects/project-a/crawls/crawl-a/issues?cursor="+tampered, "tenant-a"); res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "cursor_invalid") {
		t.Fatalf("tampered cursor status/body=%d %s", res.Code, res.Body.String())
	}
	if res := findingRequest(h, "/api/v1/projects/project-a/crawls/crawl-a/issues?limit=501", "tenant-a"); res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "limit_invalid") {
		t.Fatalf("oversize limit status/body=%d %s", res.Code, res.Body.String())
	}
}

func TestFindingRoutesExposeEachResultKindAndHideForeignCrawls(t *testing.T) {
	h := NewHandler(Dependencies{Authenticate: findingsAuthenticator, Findings: &memoryFindings{}, CursorSecret: []byte("0123456789abcdef0123456789abcdef")})
	tests := []struct {
		path, token, contains string
		status                int
	}{
		{path: "/api/v1/projects/project-a/crawls/crawl-a/pages", token: "tenant-a", status: http.StatusOK, contains: `"status_code":200`},
		{path: "/api/v1/projects/project-a/crawls/crawl-a/links", token: "read-a", status: http.StatusOK, contains: `"kind":"internal"`},
		{path: "/api/v1/projects/project-a/crawls/crawl-a/resources?type=image", token: "tenant-a", status: http.StatusOK, contains: `"type":"image"`},
		{path: "/api/v1/projects/project-a/crawls/crawl-a/resources", token: "tenant-a", status: http.StatusOK, contains: `"type":"image"`},
		{path: "/api/v1/projects/project-b/crawls/crawl-b/issues", token: "tenant-a", status: http.StatusNotFound, contains: "crawl_not_found"},
		{path: "/api/v1/projects/project-b/crawls/crawl-b/pages?cursor=invalid", token: "tenant-a", status: http.StatusNotFound, contains: "crawl_not_found"},
		{path: "/api/v1/projects/project-a/crawls/crawl-a/issues", token: "platform", status: http.StatusForbidden, contains: "scope_forbidden"},
		{path: "/api/v1/projects/project-a/crawls/crawl-a/resources?type=font", token: "tenant-a", status: http.StatusBadRequest, contains: "resource_type_invalid"},
	}
	for _, tt := range tests {
		res := findingRequest(h, tt.path, tt.token)
		if res.Code != tt.status || !strings.Contains(res.Body.String(), tt.contains) {
			t.Fatalf("%s status/body=%d %s", tt.path, res.Code, res.Body.String())
		}
	}
}

func findingRequest(h http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	return res
}

func findingsAuthenticator(_ context.Context, authorization string) (Principal, error) {
	switch authorization {
	case "Bearer tenant-a":
		return Principal{Kind: KeyTenant, KeyID: "key-a", TenantID: "tenant-a", Scopes: NewScopeSet(ScopeFindingsRead)}, nil
	case "Bearer read-a":
		return Principal{Kind: KeyReadOnly, KeyID: "read-a", TenantID: "tenant-a", ProjectID: "project-a", Scopes: NewScopeSet(ScopeFindingsRead)}, nil
	case "Bearer platform":
		return Principal{Kind: KeyPlatform, KeyID: "platform", Scopes: NewScopeSet(ScopeFindingsRead)}, nil
	default:
		return Principal{}, errors.New("unauthenticated")
	}
}
