package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
)

type ExportKind string

const (
	ExportIssuesCSV    ExportKind = "issues.csv"
	ExportPagesCSV     ExportKind = "pages.csv"
	ExportResourcesCSV ExportKind = "resources.csv"
	ExportSitemapXML   ExportKind = "sitemap.xml"
)

type ExportState string

const (
	ExportPending ExportState = "pending"
	ExportReady   ExportState = "ready"
	ExportFailed  ExportState = "failed"
)

type PreparedExport struct {
	Filename    string
	ContentType string
	WriteTo     func(io.Writer) error
}

type PreparedArchive struct {
	State    ExportState
	Filename string
	Size     int64
	Reader   io.ReadCloser
}

type ExportService interface {
	PrepareExport(context.Context, Principal, string, string, ExportKind) (PreparedExport, error)
	PrepareArchive(context.Context, Principal, string, string) (PreparedArchive, error)
}

func (s *server) exportIssuesCSV(w http.ResponseWriter, r *http.Request) {
	s.streamExport(w, r, ExportIssuesCSV)
}

func (s *server) exportPagesCSV(w http.ResponseWriter, r *http.Request) {
	s.streamExport(w, r, ExportPagesCSV)
}

func (s *server) exportResourcesCSV(w http.ResponseWriter, r *http.Request) {
	s.streamExport(w, r, ExportResourcesCSV)
}

func (s *server) exportSitemap(w http.ResponseWriter, r *http.Request) {
	s.streamExport(w, r, ExportSitemapXML)
}

func (s *server) streamExport(w http.ResponseWriter, r *http.Request, kind ExportKind) {
	principal, ok := s.requireExportScope(w, r)
	if !ok {
		return
	}
	projectID := r.PathValue("project_id")
	if principal.ProjectID != "" && principal.ProjectID != projectID {
		writeCrawlError(w, r, ErrCrawlNotFound)
		return
	}
	prepared, err := s.deps.Exports.PrepareExport(r.Context(), principal, projectID, r.PathValue("crawl_id"), kind)
	if err != nil {
		writeCrawlError(w, r, err)
		return
	}
	if prepared.WriteTo == nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "The request could not be completed")
		return
	}
	w.Header().Set("Content-Type", prepared.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", safeExportFilename(prepared.Filename)))
	w.WriteHeader(http.StatusOK)
	_ = prepared.WriteTo(w)
}

func (s *server) exportArchive(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireExportScope(w, r)
	if !ok {
		return
	}
	projectID := r.PathValue("project_id")
	if principal.ProjectID != "" && principal.ProjectID != projectID {
		writeCrawlError(w, r, ErrCrawlNotFound)
		return
	}
	prepared, err := s.deps.Exports.PrepareArchive(r.Context(), principal, projectID, r.PathValue("crawl_id"))
	if err != nil {
		writeCrawlError(w, r, err)
		return
	}
	if prepared.State == ExportPending {
		w.Header().Set("Location", r.URL.Path)
		w.Header().Set("Retry-After", "2")
		writeJSON(w, http.StatusAccepted, map[string]any{"data": map[string]any{"state": ExportPending}, "request_id": requestIDFrom(r.Context())})
		return
	}
	if prepared.State != ExportReady || prepared.Reader == nil {
		writeError(w, r, http.StatusInternalServerError, "archive_unavailable", "Archive export is unavailable")
		return
	}
	defer prepared.Reader.Close()
	w.Header().Set("Content-Type", "application/wacz")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", safeExportFilename(prepared.Filename)))
	if prepared.Size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(prepared.Size, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, prepared.Reader)
}

func (s *server) requireExportScope(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	principal, err := s.authenticate(r)
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "Authentication is required")
		return Principal{}, false
	}
	if (principal.Kind != KeyTenant && principal.Kind != KeyReadOnly) || principal.TenantID == "" || !principal.Scopes.Has(ScopeExportsRead) {
		writeError(w, r, http.StatusForbidden, "scope_forbidden", "The credential cannot access this resource")
		return Principal{}, false
	}
	if s.deps.Exports == nil {
		writeError(w, r, http.StatusServiceUnavailable, "service_unavailable", "Exports are unavailable")
		return Principal{}, false
	}
	return principal, true
}

func safeExportFilename(value string) string {
	value = filepath.Base(strings.ReplaceAll(value, "\\", "/"))
	value = strings.ReplaceAll(value, "\"", "")
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	if value == "." || value == "" {
		return "export"
	}
	return value
}
