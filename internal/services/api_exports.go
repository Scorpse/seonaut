package services

import (
	"context"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"io"
	"log"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/stjudewashere/seonaut/internal/api"
	"github.com/stjudewashere/seonaut/internal/models"
	"github.com/stjudewashere/seonaut/internal/repository"
)

type APIExportStore interface {
	ResolveSource(context.Context, api.Principal, string, string) (repository.APIExportSource, error)
	ReserveArchive(context.Context, api.Principal, string, string) (repository.APIExportJob, error)
	CompleteArchive(context.Context, api.Principal, string, string, string, int64) error
	FailArchive(context.Context, api.Principal, string, string, string) error
}

type APIExportCleanupStore interface {
	ListExpiredArchives(context.Context, time.Time) ([]repository.APIExportJob, error)
	DeleteExpiredArchive(context.Context, string, time.Time) error
}

type APIArchiveLocator interface {
	GetAPIArchiveFilePath(*models.Project, string) (string, error)
}

type APIArchiveCleaner interface {
	DeleteAPIArchive(string) error
}

type APIExportManager struct {
	Store    APIExportStore
	Findings api.FindingService
	Exporter *Exporter
	Archives APIArchiveLocator
	inflight sync.Map
}

// CrawlCompleted snapshots the project-scoped archive before another crawl can
// reuse that path. The crawler keeps the project locked until this returns.
func (m *APIExportManager) CrawlCompleted(ctx context.Context, principal api.Principal, projectID, crawlID string, project models.Project, completion api.CrawlCompletion) {
	if m == nil || m.Store == nil || m.Archives == nil {
		return
	}
	defer func() {
		if err := m.PurgeExpiredArchives(context.Background(), time.Now().UTC()); err != nil {
			log.Printf("Purge expired API archives after crawl_id=%s: %v", crawlID, err)
		}
	}()
	job, err := m.Store.ReserveArchive(ctx, principal, projectID, crawlID)
	if err != nil {
		log.Printf("Reserve API archive snapshot crawl_id=%s: %v", crawlID, err)
		return
	}
	if job.State != api.ExportPending {
		return
	}
	if !completion.ArchiveReady {
		m.failArchive(ctx, principal, projectID, crawlID, "archive_not_available")
		return
	}
	if _, loaded := m.inflight.LoadOrStore(job.ID, struct{}{}); loaded {
		return
	}
	defer m.inflight.Delete(job.ID)
	m.finalizeArchive(ctx, principal, projectID, crawlID, project, completion.State)
}

func (m *APIExportManager) PurgeExpiredArchives(ctx context.Context, before time.Time) error {
	store, ok := m.Store.(APIExportCleanupStore)
	if !ok {
		return nil
	}
	jobs, err := store.ListExpiredArchives(ctx, before)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.ArtifactPath != "" {
			if err := os.Remove(job.ArtifactPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if cleaner, ok := m.Archives.(APIArchiveCleaner); ok {
			if err := cleaner.DeleteAPIArchive(job.CrawlID); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := store.DeleteExpiredArchive(ctx, job.ID, before); err != nil {
			return err
		}
	}
	return nil
}

func (m *APIExportManager) PrepareExport(ctx context.Context, principal api.Principal, projectID, crawlID string, kind api.ExportKind) (api.PreparedExport, error) {
	if m == nil || m.Store == nil || m.Findings == nil || m.Exporter == nil {
		return api.PreparedExport{}, errors.New("export service unavailable")
	}
	source, err := m.Store.ResolveSource(ctx, principal, projectID, crawlID)
	if err != nil {
		return api.PreparedExport{}, err
	}
	host := exportHost(source.ProjectURL)
	switch kind {
	case api.ExportIssuesCSV:
		first, err := m.Findings.ListIssues(ctx, principal, projectID, crawlID, api.PageRequest{Limit: 500})
		return preparedStream(host+"-issues.csv", "text/csv; charset=utf-8", err, func(w io.Writer) error { return m.streamIssuesCSV(ctx, w, principal, projectID, crawlID, first) })
	case api.ExportPagesCSV:
		first, err := m.Findings.ListPages(ctx, principal, projectID, crawlID, api.PageRequest{Limit: 500})
		return preparedStream(host+"-pages.csv", "text/csv; charset=utf-8", err, func(w io.Writer) error { return m.streamPagesCSV(ctx, w, principal, projectID, crawlID, first) })
	case api.ExportResourcesCSV:
		first, err := m.Findings.ListResources(ctx, principal, projectID, crawlID, "", api.PageRequest{Limit: 500})
		return preparedStream(host+"-resources.csv", "text/csv; charset=utf-8", err, func(w io.Writer) error { return m.streamResourcesCSV(ctx, w, principal, projectID, crawlID, first) })
	case api.ExportSitemapXML:
		first, err := m.Findings.ListPages(ctx, principal, projectID, crawlID, api.PageRequest{Limit: 500})
		return preparedStream("sitemap.xml", "application/xml", err, func(w io.Writer) error { return m.streamSitemapXML(ctx, w, principal, projectID, crawlID, first) })
	default:
		return api.PreparedExport{}, errors.New("unsupported export kind")
	}
}

func preparedStream(filename, contentType string, err error, write func(io.Writer) error) (api.PreparedExport, error) {
	if err != nil {
		return api.PreparedExport{}, err
	}
	return api.PreparedExport{Filename: filename, ContentType: contentType, WriteTo: write}, nil
}

func (m *APIExportManager) streamIssuesCSV(ctx context.Context, output io.Writer, principal api.Principal, projectID, crawlID string, result api.PageResult[api.IssueFinding]) error {
	writer := csv.NewWriter(output)
	_ = writer.Write(m.Exporter.issueCSVHeader())
	for {
		for _, item := range result.Items {
			priority := Warning
			if item.Severity == "critical" {
				priority = Critical
			} else if item.Severity == "alert" {
				priority = Alert
			}
			_ = writer.Write(m.Exporter.issueCSVRow("en", &models.ExportIssue{Url: item.PageURL, Type: item.Code, Priority: priority}))
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			return err
		}
		if result.NextAfterID == 0 {
			return nil
		}
		var err error
		result, err = m.Findings.ListIssues(ctx, principal, projectID, crawlID, api.PageRequest{AfterID: result.NextAfterID, Limit: 500})
		if err != nil {
			return err
		}
	}
}

func (m *APIExportManager) streamPagesCSV(ctx context.Context, output io.Writer, principal api.Principal, projectID, crawlID string, result api.PageResult[api.PageFinding]) error {
	writer := csv.NewWriter(output)
	_ = writer.Write(m.Exporter.pageReportCSVHeader())
	for {
		for _, page := range result.Items {
			report := &models.PageReport{StatusCode: page.StatusCode, URL: page.URL, RedirectURL: page.RedirectURL, ContentType: page.ContentType, Canonical: page.Canonical, Lang: page.Language, Title: page.Title, Description: page.Description, Robots: page.Robots, H1: page.Heading1, H2: page.Heading2, Size: page.SizeBytes, Words: page.Words, Depth: page.Depth, TTFB: page.TTFBMillis}
			_ = writer.Write(m.Exporter.pageReportCSVRow(report))
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			return err
		}
		if result.NextAfterID == 0 {
			return nil
		}
		var err error
		result, err = m.Findings.ListPages(ctx, principal, projectID, crawlID, api.PageRequest{AfterID: result.NextAfterID, Limit: 500})
		if err != nil {
			return err
		}
	}
}

func (m *APIExportManager) streamResourcesCSV(ctx context.Context, output io.Writer, principal api.Principal, projectID, crawlID string, result api.PageResult[api.ResourceFinding]) error {
	writer := csv.NewWriter(output)
	_ = writer.Write([]string{"Type", "Origin", "URL", "Alt", "Poster"})
	for {
		for _, item := range result.Items {
			_ = writer.Write([]string{item.Type, item.OriginURL, item.URL, item.Alt, item.Poster})
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			return err
		}
		if result.NextAfterID == 0 {
			return nil
		}
		var err error
		result, err = m.Findings.ListResources(ctx, principal, projectID, crawlID, "", api.PageRequest{AfterID: result.NextAfterID, Limit: 500})
		if err != nil {
			return err
		}
	}
}

func (m *APIExportManager) streamSitemapXML(ctx context.Context, output io.Writer, principal api.Principal, projectID, crawlID string, result api.PageResult[api.PageFinding]) error {
	if _, err := io.WriteString(output, xml.Header); err != nil {
		return err
	}
	encoder := xml.NewEncoder(output)
	root := xml.StartElement{Name: xml.Name{Local: "urlset"}, Attr: []xml.Attr{{Name: xml.Name{Local: "xmlns"}, Value: "http://www.sitemaps.org/schemas/sitemap/0.9"}}}
	if err := encoder.EncodeToken(root); err != nil {
		return err
	}
	for {
		for _, page := range result.Items {
			if !page.SitemapEligible {
				continue
			}
			if err := encoder.Encode(struct {
				XMLName xml.Name `xml:"url"`
				Loc     string   `xml:"loc"`
			}{Loc: page.URL}); err != nil {
				return err
			}
		}
		if result.NextAfterID == 0 {
			break
		}
		var err error
		result, err = m.Findings.ListPages(ctx, principal, projectID, crawlID, api.PageRequest{AfterID: result.NextAfterID, Limit: 500})
		if err != nil {
			return err
		}
	}
	if err := encoder.EncodeToken(root.End()); err != nil {
		return err
	}
	return encoder.Flush()
}

func (m *APIExportManager) PrepareArchive(ctx context.Context, principal api.Principal, projectID, crawlID string) (api.PreparedArchive, error) {
	if m == nil || m.Store == nil || m.Archives == nil {
		return api.PreparedArchive{}, errors.New("archive export unavailable")
	}
	if err := m.PurgeExpiredArchives(ctx, time.Now().UTC()); err != nil {
		return api.PreparedArchive{}, err
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
	if _, active := m.inflight.Load(job.ID); active {
		return api.PreparedArchive{State: api.ExportPending}, nil
	}
	if _, loaded := m.inflight.LoadOrStore(job.ID, struct{}{}); !loaded {
		defer m.inflight.Delete(job.ID)
		m.finalizeArchive(ctx, principal, projectID, crawlID, models.Project{Id: source.UpstreamProjectID, URL: source.ProjectURL}, source.State)
	}
	return api.PreparedArchive{State: api.ExportPending}, nil
}

func (m *APIExportManager) finalizeArchive(ctx context.Context, principal api.Principal, projectID, crawlID string, project models.Project, state api.CrawlState) {
	project.Host = exportHost(project.URL)
	sourcePath, err := m.Archives.GetAPIArchiveFilePath(&project, crawlID)
	if err != nil {
		if state == api.CrawlSucceeded || state == api.CrawlFailed || state == api.CrawlCanceled {
			m.failArchive(ctx, principal, projectID, crawlID, "archive_not_available")
		}
		return
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		m.failArchive(ctx, principal, projectID, crawlID, "archive_read_failed")
		return
	}
	if err := m.Store.CompleteArchive(ctx, principal, projectID, crawlID, sourcePath, info.Size()); err != nil {
		m.failArchive(ctx, principal, projectID, crawlID, "archive_state_failed")
		log.Printf("Complete API archive snapshot crawl_id=%s: %v", crawlID, err)
	}
}

func (m *APIExportManager) failArchive(ctx context.Context, principal api.Principal, projectID, crawlID, code string) {
	if err := m.Store.FailArchive(ctx, principal, projectID, crawlID, code); err != nil {
		log.Printf("Fail API archive snapshot crawl_id=%s code=%s: %v", crawlID, code, err)
	}
}

func exportHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err == nil && parsed.Hostname() != "" {
		return parsed.Hostname()
	}
	return "crawl"
}
