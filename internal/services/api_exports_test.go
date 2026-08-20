package services

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stjudewashere/seonaut/internal/api"
	"github.com/stjudewashere/seonaut/internal/models"
	"github.com/stjudewashere/seonaut/internal/repository"
)

type apiExportStoreFixture struct {
	job         repository.APIExportJob
	sourceState api.CrawlState
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

type reportExportFixture struct{}

func (reportExportFixture) FindAllPageReportsByCrawlId(int64) <-chan *models.PageReport {
	return valueChannel(&models.PageReport{URL: "https://a.example/one", StatusCode: 200, Title: "One", Words: 7})
}
func (reportExportFixture) FindSitemapPageReports(int64) <-chan *models.PageReport {
	return valueChannel(&models.PageReport{URL: "https://a.example/one"})
}

type archivePathFixture string

func (p archivePathFixture) GetArchiveFilePath(*models.Project) (string, error) {
	return string(p), nil
}

type exportTranslatorFixture struct{}

func (exportTranslatorFixture) Trans(_ string, value string, _ ...interface{}) string { return value }

func TestAPIExportManagerAdaptsExistingExporterAndSitemapData(t *testing.T) {
	manager := APIExportManager{
		Store:    &apiExportStoreFixture{},
		Exporter: NewExporter(resourceExportFixture{}, exportTranslatorFixture{}),
		Reports:  reportExportFixture{},
	}
	principal := api.Principal{TenantID: "tenant-a"}

	resources, err := manager.PrepareExport(context.Background(), principal, "project-a", "crawl-a", api.ExportResourcesCSV)
	if err != nil {
		t.Fatal(err)
	}
	var csv bytes.Buffer
	if err := resources.WriteTo(&csv); err != nil || !strings.HasPrefix(csv.String(), "Type,Origin,URL,Alt,Poster\n") {
		t.Fatalf("resources=%q err=%v", csv.String(), err)
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

func TestAPIExportManagerCopiesWACZIntoImmutableCrawlArtifact(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.wacz")
	if err := os.WriteFile(source, []byte("WACZ"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &apiExportStoreFixture{}
	manager := APIExportManager{
		Store:       store,
		Archives:    archivePathFixture(source),
		ArtifactDir: filepath.Join(dir, "api"),
		RunAsync:    func(work func()) { work() },
	}
	principal := api.Principal{TenantID: "tenant-a"}
	pending, err := manager.PrepareArchive(context.Background(), principal, "project-a", "crawl-a")
	if err != nil || pending.State != api.ExportPending || store.job.State != api.ExportReady || store.job.ArtifactPath == source {
		t.Fatalf("pending=%+v job=%+v err=%v", pending, store.job, err)
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

func TestAPIExportManagerDoesNotPublishAnArchiveWhileCrawlIsRunning(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.wacz")
	if err := os.WriteFile(source, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &apiExportStoreFixture{sourceState: api.CrawlRunning}
	manager := APIExportManager{Store: store, Archives: archivePathFixture(source), ArtifactDir: filepath.Join(dir, "api"), RunAsync: func(work func()) { work() }}
	prepared, err := manager.PrepareArchive(context.Background(), api.Principal{TenantID: "tenant-a"}, "project-a", "crawl-a")
	if err != nil || prepared.State != api.ExportPending || store.job.State != api.ExportPending {
		t.Fatalf("prepared=%+v job=%+v err=%v", prepared, store.job, err)
	}
}
