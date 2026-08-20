package services

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/stjudewashere/seonaut/internal/api"
	"github.com/stjudewashere/seonaut/internal/models"
	"github.com/stjudewashere/seonaut/internal/repository"
	"github.com/turk/go-sitemap"
)

type APIExportStore interface {
	ResolveSource(context.Context, api.Principal, string, string) (repository.APIExportSource, error)
	ReserveArchive(context.Context, api.Principal, string, string) (repository.APIExportJob, error)
	CompleteArchive(context.Context, api.Principal, string, string, string, int64) error
	FailArchive(context.Context, api.Principal, string, string, string) error
}

type APIExportReports interface {
	FindAllPageReportsByCrawlId(int64) <-chan *models.PageReport
	FindSitemapPageReports(int64) <-chan *models.PageReport
}

type APIArchiveLocator interface {
	GetArchiveFilePath(*models.Project) (string, error)
}

type APIExportManager struct {
	Store       APIExportStore
	Exporter    *Exporter
	Reports     APIExportReports
	Archives    APIArchiveLocator
	ArtifactDir string
	RunAsync    func(func())
	inflight    sync.Map
}

func (m *APIExportManager) PrepareExport(ctx context.Context, principal api.Principal, projectID, crawlID string, kind api.ExportKind) (api.PreparedExport, error) {
	if m == nil || m.Store == nil || m.Exporter == nil || m.Reports == nil {
		return api.PreparedExport{}, errors.New("export service unavailable")
	}
	source, err := m.Store.ResolveSource(ctx, principal, projectID, crawlID)
	if err != nil {
		return api.PreparedExport{}, err
	}
	crawl := &models.Crawl{Id: source.UpstreamCrawlID, ProjectId: source.UpstreamProjectID}
	host := exportHost(source.ProjectURL)
	switch kind {
	case api.ExportIssuesCSV:
		return api.PreparedExport{Filename: host + "-issues.csv", ContentType: "text/csv; charset=utf-8", WriteTo: func(w io.Writer) error {
			m.Exporter.ExportAllIssues("en", w, crawl)
			return nil
		}}, nil
	case api.ExportPagesCSV:
		return api.PreparedExport{Filename: host + "-pages.csv", ContentType: "text/csv; charset=utf-8", WriteTo: func(w io.Writer) error {
			m.Exporter.ExportPageReports(w, m.Reports.FindAllPageReportsByCrawlId(source.UpstreamCrawlID))
			return nil
		}}, nil
	case api.ExportResourcesCSV:
		return api.PreparedExport{Filename: host + "-resources.csv", ContentType: "text/csv; charset=utf-8", WriteTo: func(w io.Writer) error {
			m.Exporter.ExportAllResources(w, crawl)
			return nil
		}}, nil
	case api.ExportSitemapXML:
		return api.PreparedExport{Filename: "sitemap.xml", ContentType: "application/xml", WriteTo: func(w io.Writer) error {
			document := sitemap.NewSitemap(w, true)
			for page := range m.Reports.FindSitemapPageReports(source.UpstreamCrawlID) {
				document.Add(page.URL, "")
			}
			document.Write()
			return nil
		}}, nil
	default:
		return api.PreparedExport{}, errors.New("unsupported export kind")
	}
}

func (m *APIExportManager) PrepareArchive(ctx context.Context, principal api.Principal, projectID, crawlID string) (api.PreparedArchive, error) {
	if m == nil || m.Store == nil || m.Archives == nil {
		return api.PreparedArchive{}, errors.New("archive export unavailable")
	}
	source, err := m.Store.ResolveSource(ctx, principal, projectID, crawlID)
	if err != nil {
		return api.PreparedArchive{}, err
	}
	job, err := m.Store.ReserveArchive(ctx, principal, projectID, crawlID)
	if err != nil {
		return api.PreparedArchive{}, err
	}
	if job.State == api.ExportReady {
		file, err := os.Open(job.ArtifactPath)
		if err != nil {
			return api.PreparedArchive{}, err
		}
		return api.PreparedArchive{State: api.ExportReady, Filename: exportHost(source.ProjectURL) + ".wacz", Size: job.ArtifactSize, Reader: file}, nil
	}
	if job.State == api.ExportFailed {
		return api.PreparedArchive{State: api.ExportFailed}, nil
	}
	if source.State == api.CrawlQueued || source.State == api.CrawlRunning {
		return api.PreparedArchive{State: api.ExportPending}, nil
	}
	if _, loaded := m.inflight.LoadOrStore(job.ID, struct{}{}); !loaded {
		work := func() {
			defer m.inflight.Delete(job.ID)
			m.finalizeArchive(context.Background(), principal, projectID, crawlID, source)
		}
		if m.RunAsync != nil {
			m.RunAsync(work)
		} else {
			go work()
		}
	}
	return api.PreparedArchive{State: api.ExportPending}, nil
}

func (m *APIExportManager) finalizeArchive(ctx context.Context, principal api.Principal, projectID, crawlID string, source repository.APIExportSource) {
	project := &models.Project{Id: source.UpstreamProjectID, Host: exportHost(source.ProjectURL), URL: source.ProjectURL}
	sourcePath, err := m.Archives.GetArchiveFilePath(project)
	if err != nil {
		if source.State == api.CrawlSucceeded || source.State == api.CrawlFailed || source.State == api.CrawlCanceled {
			_ = m.Store.FailArchive(ctx, principal, projectID, crawlID, "archive_not_available")
		}
		return
	}
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		_ = m.Store.FailArchive(ctx, principal, projectID, crawlID, "archive_read_failed")
		return
	}
	defer sourceFile.Close()
	dir := m.ArtifactDir
	if dir == "" {
		dir = filepath.Join("archive", "api")
	}
	destinationDir := filepath.Join(dir, crawlID)
	if err := os.MkdirAll(destinationDir, 0o750); err != nil {
		_ = m.Store.FailArchive(ctx, principal, projectID, crawlID, "archive_copy_failed")
		return
	}
	destination := filepath.Join(destinationDir, project.Host+".wacz")
	temporary, err := os.CreateTemp(destinationDir, "archive-*.tmp")
	if err != nil {
		_ = m.Store.FailArchive(ctx, principal, projectID, crawlID, "archive_copy_failed")
		return
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := io.Copy(temporary, sourceFile); err != nil {
		temporary.Close()
		_ = m.Store.FailArchive(ctx, principal, projectID, crawlID, "archive_copy_failed")
		return
	}
	if err := temporary.Close(); err != nil {
		_ = m.Store.FailArchive(ctx, principal, projectID, crawlID, "archive_copy_failed")
		return
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		_ = m.Store.FailArchive(ctx, principal, projectID, crawlID, "archive_copy_failed")
		return
	}
	info, err := os.Stat(destination)
	if err != nil {
		_ = m.Store.FailArchive(ctx, principal, projectID, crawlID, "archive_copy_failed")
		return
	}
	_ = m.Store.CompleteArchive(ctx, principal, projectID, crawlID, destination, info.Size())
}

func exportHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err == nil && parsed.Hostname() != "" {
		return parsed.Hostname()
	}
	return "crawl"
}
