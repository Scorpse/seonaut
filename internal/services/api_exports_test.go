package services

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stjudewashere/seonaut/internal/api"
	"github.com/stjudewashere/seonaut/internal/models"
	"github.com/stjudewashere/seonaut/internal/repository"
)

type apiExportStoreFixture struct {
	job         repository.APIExportJob
	sourceState api.CrawlState
	expired     []repository.APIExportJob
	deleted     []string
}

func (s *apiExportStoreFixture) ListExpiredArchives(context.Context, time.Time) ([]repository.APIExportJob, error) {
	return s.expired, nil
}

func (s *apiExportStoreFixture) DeleteExpiredArchive(_ context.Context, id string, _ time.Time) error {
	s.deleted = append(s.deleted, id)
	return nil
}

func (s *apiExportStoreFixture) ResolveSource(_ context.Context, principal api.Principal, projectID, crawlID string) (repository.APIExportSource, error) {
	if principal.TenantID != "tenant-a" || projectID != "project-a" || crawlID != "crawl-a" {
		return repository.APIExportSource{}, api.ErrCrawlNotFound
	}
	state := s.sourceState
	if state == "" {
		state = api.CrawlSucceeded
	}
	return repository.APIExportSource{UpstreamCrawlID: 9, UpstreamProjectID: 4, ProjectURL: "https://a.example", State: state}, nil
}

func (s *apiExportStoreFixture) ReserveArchive(_ context.Context, principal api.Principal, projectID, crawlID string) (repository.APIExportJob, error) {
	if s.job.ID == "" {
		s.job = repository.APIExportJob{ID: "job-a", TenantID: principal.TenantID, ProjectID: projectID, CrawlID: crawlID, State: api.ExportPending}
	}
	return s.job, nil
}

func (s *apiExportStoreFixture) CompleteArchive(_ context.Context, _ api.Principal, _, _, path string, size int64) error {
	s.job.State, s.job.ArtifactPath, s.job.ArtifactSize = api.ExportReady, path, size
	return nil
}

func (s *apiExportStoreFixture) FailArchive(_ context.Context, _ api.Principal, _, _, code string) error {
	s.job.State, s.job.FailureCode = api.ExportFailed, code
	return nil
}

type exportFindingsFixture struct{}

func (exportFindingsFixture) ListIssues(context.Context, api.Principal, string, string, api.PageRequest) (api.PageResult[api.IssueFinding], error) {
	return api.PageResult[api.IssueFinding]{Items: []api.IssueFinding{{Code: "ERROR_EMPTY_TITLE", Severity: "alert", PageURL: "https://a.example/one"}}}, nil
}

type failingExportFindings struct{ exportFindingsFixture }

func (failingExportFindings) ListIssues(context.Context, api.Principal, string, string, api.PageRequest) (api.PageResult[api.IssueFinding], error) {
	return api.PageResult[api.IssueFinding]{}, errors.New("query failed")
}
func (exportFindingsFixture) ListPages(context.Context, api.Principal, string, string, api.PageRequest) (api.PageResult[api.PageFinding], error) {
	return api.PageResult[api.PageFinding]{Items: []api.PageFinding{{URL: "https://a.example/one", StatusCode: 200, Title: "One", Words: 7, SitemapEligible: true}}}, nil
}
func (exportFindingsFixture) ListLinks(context.Context, api.Principal, string, string, api.PageRequest) (api.PageResult[api.LinkFinding], error) {
	return api.PageResult[api.LinkFinding]{}, nil
}
func (exportFindingsFixture) ListResources(context.Context, api.Principal, string, string, string, api.PageRequest) (api.PageResult[api.ResourceFinding], error) {
	return api.PageResult[api.ResourceFinding]{Items: []api.ResourceFinding{{Type: "image", OriginURL: "https://a.example/one", URL: "https://a.example/logo.png", Alt: "Logo"}}}, nil
}

type archivePathFixture string

func (p archivePathFixture) GetAPIArchiveFilePath(*models.Project, string) (string, error) {
	return string(p), nil
}

type exportTranslatorFixture struct{}

func (exportTranslatorFixture) Trans(_ string, value string, _ ...interface{}) string {
	if value == "ERROR_EMPTY_TITLE" {
		return "Pages with missing title"
	}
	return value
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestAPIExportManagerAdaptsExistingExporterAndSitemapData(t *testing.T) {
	manager := APIExportManager{
		Store:    &apiExportStoreFixture{},
		Findings: exportFindingsFixture{},
		Exporter: NewExporter(resourceExportFixture{}, exportTranslatorFixture{}),
	}
	principal := api.Principal{TenantID: "tenant-a"}
	issues, err := manager.PrepareExport(context.Background(), principal, "project-a", "crawl-a", api.ExportIssuesCSV)
	if err != nil {
		t.Fatal(err)
	}
	var issuesCSV bytes.Buffer
	if err := issues.WriteTo(&issuesCSV); err != nil || !strings.Contains(issuesCSV.String(), "Pages with missing title") || strings.Contains(issuesCSV.String(), "ERROR_EMPTY_TITLE") {
		t.Fatalf("issues=%q err=%v", issuesCSV.String(), err)
	}

	resources, err := manager.PrepareExport(context.Background(), principal, "project-a", "crawl-a", api.ExportResourcesCSV)
	if err != nil {
		t.Fatal(err)
	}
	var csv bytes.Buffer
	if err := resources.WriteTo(&csv); err != nil || !strings.HasPrefix(csv.String(), "Type,Origin,URL,Alt,Poster\n") {
		t.Fatalf("resources=%q err=%v", csv.String(), err)
	}
	if err := resources.WriteTo(failingWriter{}); err == nil || err.Error() != "write failed" {
		t.Fatalf("writer err=%v", err)
	}

	sitemap, err := manager.PrepareExport(context.Background(), principal, "project-a", "crawl-a", api.ExportSitemapXML)
	if err != nil {
		t.Fatal(err)
	}
	var xml bytes.Buffer
	if err := sitemap.WriteTo(&xml); err != nil || !strings.Contains(xml.String(), "https://a.example/one") {
		t.Fatalf("sitemap=%q err=%v", xml.String(), err)
	}
}

func TestAPIExportManagerPropagatesFindingFailureBeforePreparingResponse(t *testing.T) {
	manager := APIExportManager{Store: &apiExportStoreFixture{}, Findings: failingExportFindings{}, Exporter: NewExporter(resourceExportFixture{}, exportTranslatorFixture{})}
	_, err := manager.PrepareExport(context.Background(), api.Principal{TenantID: "tenant-a"}, "project-a", "crawl-a", api.ExportIssuesCSV)
	if err == nil || err.Error() != "query failed" {
		t.Fatalf("err=%v", err)
	}
}

func TestAPIExportManagerRegistersCrawlOwnedWACZWhenCrawlCompletes(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.wacz")
	if err := os.WriteFile(source, []byte("WACZ"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &apiExportStoreFixture{}
	manager := APIExportManager{
		Store:    store,
		Archives: archivePathFixture(source),
	}
	principal := api.Principal{TenantID: "tenant-a"}
	manager.CrawlCompleted(context.Background(), principal, "project-a", "crawl-a", models.Project{Id: 4, URL: "https://a.example"}, api.CrawlCompletion{State: api.CrawlSucceeded, ArchiveReady: true})
	if store.job.State != api.ExportReady || store.job.ArtifactPath != source {
		t.Fatalf("job=%+v", store.job)
	}
	ready, err := manager.PrepareArchive(context.Background(), principal, "project-a", "crawl-a")
	if err != nil || ready.State != api.ExportReady {
		t.Fatalf("ready=%+v err=%v", ready, err)
	}
	defer ready.Reader.Close()
	content, err := io.ReadAll(ready.Reader)
	if err != nil || string(content) != "WACZ" {
		t.Fatalf("content=%q err=%v", content, err)
	}
}

func TestAPIExportManagerRejectsStaleProjectArchiveWhenCurrentCrawlDidNotArchive(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.wacz")
	if err := os.WriteFile(source, []byte("previous-crawl"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &apiExportStoreFixture{}
	manager := APIExportManager{Store: store, Archives: archivePathFixture(source)}
	manager.CrawlCompleted(context.Background(), api.Principal{TenantID: "tenant-a"}, "project-a", "crawl-a", models.Project{Id: 4, URL: "https://a.example"}, api.CrawlCompletion{State: api.CrawlSucceeded, ArchiveReady: false})
	if store.job.State != api.ExportFailed || store.job.FailureCode != "archive_not_available" {
		t.Fatalf("job=%+v", store.job)
	}
	if matches, err := filepath.Glob(filepath.Join(dir, "api", "crawl-a", "*.wacz")); err != nil || len(matches) != 0 {
		t.Fatalf("matches=%v err=%v", matches, err)
	}
}

func TestAPIExportManagerDoesNotPublishAnArchiveWhileCrawlIsRunning(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.wacz")
	if err := os.WriteFile(source, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &apiExportStoreFixture{sourceState: api.CrawlRunning}
	manager := APIExportManager{Store: store, Archives: archivePathFixture(source)}
	prepared, err := manager.PrepareArchive(context.Background(), api.Principal{TenantID: "tenant-a"}, "project-a", "crawl-a")
	if err != nil || prepared.State != api.ExportPending || store.job.State != api.ExportPending {
		t.Fatalf("prepared=%+v job=%+v err=%v", prepared, store.job, err)
	}
}

func TestAPIExportManagerRecoversPendingTerminalJobFromCrawlOwnedPath(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.wacz")
	if err := os.WriteFile(source, []byte("crawl-a"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &apiExportStoreFixture{sourceState: api.CrawlSucceeded}
	manager := APIExportManager{Store: store, Archives: archivePathFixture(source)}
	prepared, err := manager.PrepareArchive(context.Background(), api.Principal{TenantID: "tenant-a"}, "project-a", "crawl-a")
	if err != nil || prepared.State != api.ExportPending || store.job.State != api.ExportReady || store.job.ArtifactPath != source {
		t.Fatalf("prepared=%+v job=%+v err=%v", prepared, store.job, err)
	}
	ready, err := manager.PrepareArchive(context.Background(), api.Principal{TenantID: "tenant-a"}, "project-a", "crawl-a")
	if err != nil || ready.State != api.ExportReady {
		t.Fatalf("ready=%+v err=%v", ready, err)
	}
	ready.Reader.Close()
}

func TestAPIExportManagerNeverPublishesCrashPartialAsReady(t *testing.T) {
	archives := NewArchiveService(t.TempDir())
	project := &models.Project{Id: 4, URL: "https://a.example"}
	writer, _, err := archives.GetAPIArchiveWriter(project, "crawl-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	store := &apiExportStoreFixture{sourceState: api.CrawlFailed}
	manager := APIExportManager{Store: store, Archives: archives}
	prepared, err := manager.PrepareArchive(context.Background(), api.Principal{TenantID: "tenant-a"}, "project-a", "crawl-a")
	if err != nil || prepared.State != api.ExportPending || store.job.State != api.ExportFailed || store.job.FailureCode != "archive_not_available" {
		t.Fatalf("prepared=%+v job=%+v err=%v", prepared, store.job, err)
	}
	if _, err := archives.GetAPIArchiveFilePath(project, "crawl-a"); err == nil {
		t.Fatal("partial archive became visible")
	}
}

func TestAPIExportManagerPurgesExpiredArtifactAndJob(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "expired.wacz")
	if err := os.WriteFile(artifact, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &apiExportStoreFixture{expired: []repository.APIExportJob{{ID: "job-old", ArtifactPath: artifact}}}
	manager := APIExportManager{Store: store}
	if err := manager.PurgeExpiredArchives(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(artifact); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("artifact still exists: %v", err)
	}
	if len(store.deleted) != 1 || store.deleted[0] != "job-old" {
		t.Fatalf("deleted=%v", store.deleted)
	}
}
