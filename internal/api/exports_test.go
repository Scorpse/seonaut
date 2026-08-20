package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type memoryExports struct {
	archiveReady bool
}

func (m *memoryExports) PrepareExport(_ context.Context, principal Principal, projectID, crawlID string, kind ExportKind) (PreparedExport, error) {
	if principal.TenantID != "tenant-a" || projectID != "project-a" || crawlID != "crawl-a" || principal.ProjectID != "" && principal.ProjectID != projectID {
		return PreparedExport{}, ErrCrawlNotFound
	}
	content := map[ExportKind]string{
		ExportIssuesCSV:    "URL,Issue Type,Priority\nhttps://a.example/one,ERROR_EMPTY_TITLE,Alert\n",
		ExportPagesCSV:     "Status Code,URL,Redirect URL,Content Type,Canonical,Lang,Title,Title Length,Description,Description Length,Robots,Header 1,Header 2,Size,Nº of words,Depth,TTFB\n200,https://a.example/one,,text/html,,en,One,3,,,index,One,,1.0 KB,7,0,12 ms\n",
		ExportResourcesCSV: "Type,Origin,URL,Alt,Poster\nimage,https://a.example/one,https://a.example/logo.png,Logo,\n",
		ExportSitemapXML:   "<?xml version=\"1.0\" encoding=\"UTF-8\"?><urlset><url><loc>https://a.example/one</loc></url></urlset>",
	}[kind]
	if content == "" {
		return PreparedExport{}, errors.New("unknown export")
	}
	contentType := "text/csv; charset=utf-8"
	filename := string(kind)
	if kind == ExportSitemapXML {
		contentType = "application/xml"
		filename = "sitemap.xml"
	}
	return PreparedExport{Filename: filename, ContentType: contentType, WriteTo: func(w io.Writer) error { _, err := io.WriteString(w, content); return err }}, nil
}

func (m *memoryExports) PrepareArchive(_ context.Context, principal Principal, projectID, crawlID string) (PreparedArchive, error) {
	if principal.TenantID != "tenant-a" || projectID != "project-a" || crawlID != "crawl-a" {
		return PreparedArchive{}, ErrCrawlNotFound
	}
	if !m.archiveReady {
		m.archiveReady = true
		return PreparedArchive{State: ExportPending}, nil
	}
	return PreparedArchive{State: ExportReady, Filename: "a.example.wacz", Size: 4, Reader: io.NopCloser(bytes.NewBufferString("WACZ"))}, nil
}

func TestCSVAndSitemapExportsPreservePublicColumnContracts(t *testing.T) {
	h := NewHandler(Dependencies{Authenticate: exportAuthenticator, Exports: &memoryExports{}})
	tests := []struct {
		path, contentType, header string
	}{
		{path: "/api/v1/projects/project-a/crawls/crawl-a/exports/issues.csv", contentType: "text/csv; charset=utf-8", header: "URL,Issue Type,Priority\n"},
		{path: "/api/v1/projects/project-a/crawls/crawl-a/exports/pages.csv", contentType: "text/csv; charset=utf-8", header: "Status Code,URL,Redirect URL,Content Type,Canonical,Lang,Title,Title Length,Description,Description Length,Robots,Header 1,Header 2,Size,Nº of words,Depth,TTFB\n"},
		{path: "/api/v1/projects/project-a/crawls/crawl-a/exports/resources.csv", contentType: "text/csv; charset=utf-8", header: "Type,Origin,URL,Alt,Poster\n"},
		{path: "/api/v1/projects/project-a/crawls/crawl-a/exports/sitemap.xml", contentType: "application/xml", header: `<?xml version="1.0" encoding="UTF-8"?>`},
	}
	for _, tt := range tests {
		res := exportRequest(h, tt.path, "tenant-a")
		if res.Code != http.StatusOK || res.Header().Get("Content-Type") != tt.contentType || !strings.HasPrefix(res.Body.String(), tt.header) {
			t.Fatalf("%s status/type/body=%d %s %q", tt.path, res.Code, res.Header().Get("Content-Type"), res.Body.String())
		}
	}
}

func TestExportsHideForeignCrawlsAndRequireExportScope(t *testing.T) {
	h := NewHandler(Dependencies{Authenticate: exportAuthenticator, Exports: &memoryExports{}})
	if res := exportRequest(h, "/api/v1/projects/project-b/crawls/crawl-b/exports/issues.csv", "tenant-a"); res.Code != http.StatusNotFound || !strings.Contains(res.Body.String(), "crawl_not_found") {
		t.Fatalf("foreign status/body=%d %s", res.Code, res.Body.String())
	}
	if res := exportRequest(h, "/api/v1/projects/project-a/crawls/crawl-a/exports/issues.csv", "findings-only"); res.Code != http.StatusForbidden || !strings.Contains(res.Body.String(), "scope_forbidden") {
		t.Fatalf("scope status/body=%d %s", res.Code, res.Body.String())
	}
}

func TestWACZExportReturnsPollLocationUntilArtifactIsReady(t *testing.T) {
	exports := &memoryExports{}
	h := NewHandler(Dependencies{Authenticate: exportAuthenticator, Exports: exports})
	path := "/api/v1/projects/project-a/crawls/crawl-a/exports/archive.wacz"
	pending := exportRequest(h, path, "tenant-a")
	if pending.Code != http.StatusAccepted || pending.Header().Get("Location") != path || pending.Header().Get("Retry-After") == "" || !strings.Contains(pending.Body.String(), `"state":"pending"`) {
		t.Fatalf("pending status/headers/body=%d %#v %s", pending.Code, pending.Header(), pending.Body.String())
	}
	ready := exportRequest(h, path, "tenant-a")
	if ready.Code != http.StatusOK || ready.Header().Get("Content-Type") != "application/wacz" || ready.Header().Get("Content-Length") != "4" || ready.Body.String() != "WACZ" {
		t.Fatalf("ready status/headers/body=%d %#v %s", ready.Code, ready.Header(), ready.Body.String())
	}
}

func exportRequest(h http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	return res
}

func exportAuthenticator(_ context.Context, authorization string) (Principal, error) {
	switch authorization {
	case "Bearer tenant-a":
		return Principal{Kind: KeyTenant, KeyID: "key-a", TenantID: "tenant-a", Scopes: NewScopeSet(ScopeExportsRead)}, nil
	case "Bearer read-a":
		return Principal{Kind: KeyReadOnly, KeyID: "read-a", TenantID: "tenant-a", ProjectID: "project-a", Scopes: NewScopeSet(ScopeExportsRead)}, nil
	case "Bearer findings-only":
		return Principal{Kind: KeyReadOnly, KeyID: "read-a", TenantID: "tenant-a", ProjectID: "project-a", Scopes: NewScopeSet(ScopeFindingsRead)}, nil
	default:
		return Principal{}, errors.New("unauthenticated")
	}
}
