package services_test

import (
	"testing"
	"time"

	"github.com/stjudewashere/seonaut/internal/config"
	"github.com/stjudewashere/seonaut/internal/models"
	"github.com/stjudewashere/seonaut/internal/services"
)

// crawlerTestRepository is a minimal mock that counts SaveCrawl calls.
type crawlerTestRepository struct {
	saveCrawlCount int
	deleted        chan int64
}

func TestStartCrawlerObservedReturnsUpstreamIDAndCompletes(t *testing.T) {
	repo := &crawlerTestRepository{deleted: make(chan int64, 1)}
	svc := newTestCrawlerService(repo)
	done := make(chan struct {
		crawl    *models.Crawl
		canceled bool
		archived bool
		err      error
	}, 1)
	project := models.Project{Id: 99, URL: "http://localhost:1"}
	crawl, err := svc.StartCrawlerObserved(project, models.BasicAuth{}, "crawl-a", func(crawl *models.Crawl, canceled, archived bool, err error) {
		done <- struct {
			crawl    *models.Crawl
			canceled bool
			archived bool
			err      error
		}{crawl: crawl, canceled: canceled, archived: archived, err: err}
	})
	if err != nil || crawl == nil || crawl.Id != 1 {
		t.Fatalf("crawl=%+v err=%v", crawl, err)
	}
	select {
	case result := <-done:
		if result.crawl == nil || result.crawl.Id != 1 || result.canceled || result.archived || result.err != nil {
			t.Fatalf("completion=%+v", result)
		}
		select {
		case <-repo.deleted:
		case <-time.After(5 * time.Second):
			t.Fatal("API crawl did not delegate guarded historical cleanup")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("crawler completion callback was not called")
	}
}

func TestStartCrawlerRetainsHTMLCleanupBehavior(t *testing.T) {
	repo := &crawlerTestRepository{deleted: make(chan int64, 1)}
	svc := newTestCrawlerService(repo)
	if err := svc.StartCrawler(models.Project{Id: 100, URL: "http://localhost:1"}, models.BasicAuth{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-repo.deleted:
	case <-time.After(5 * time.Second):
		t.Fatal("HTML crawl did not clean up the previous crawl")
	}
}

func TestStartCrawlerObservedReportsClosedCrawlOwnedArchive(t *testing.T) {
	repo := &crawlerTestRepository{}
	svc := newTestCrawlerService(repo)
	svc.ArchiveService = services.NewArchiveService(t.TempDir())
	project := models.Project{Id: 101, URL: "http://localhost:1", Archive: true}
	done := make(chan bool, 1)
	if _, err := svc.StartCrawlerObserved(project, models.BasicAuth{}, "crawl-archive", func(_ *models.Crawl, _, archived bool, _ error) { done <- archived }); err != nil {
		t.Fatal(err)
	}
	select {
	case archived := <-done:
		if !archived {
			t.Fatal("archive was not finalized")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("crawler completion callback was not called")
	}
	if _, err := svc.ArchiveService.GetAPIArchiveFilePath(&project, "crawl-archive"); err != nil {
		t.Fatal(err)
	}
}

func (r *crawlerTestRepository) SaveCrawl(p models.Project) (*models.Crawl, error) {
	r.saveCrawlCount++
	return &models.Crawl{Id: 1, ProjectId: p.Id, URL: p.URL}, nil
}

func (r *crawlerTestRepository) GetLastCrawl(p *models.Project) models.Crawl {
	return models.Crawl{}
}

func (r *crawlerTestRepository) GetLastCrawls(p models.Project, limit int) []models.Crawl {
	return []models.Crawl{}
}

func (r *crawlerTestRepository) DeleteCrawlDataIfUnreferenced(c *models.Crawl) {
	if r.deleted != nil {
		r.deleted <- c.Id
	}
}

func (r *crawlerTestRepository) CountIssuesByPriority(crawlId int64, priority int) int {
	return 0
}

func (r *crawlerTestRepository) UpdateCrawl(c *models.Crawl) error { return nil }

type crawlerHandlerTestRepository struct{}

func (r *crawlerHandlerTestRepository) SavePageReport(pr *models.PageReport, crawlId int64) (*models.PageReport, error) {
	return pr, nil
}

type crawlerReportManagerTestRepository struct{}

func (r *crawlerReportManagerTestRepository) SaveIssues(issues <-chan *models.Issue) {
	for range issues {
	}
}

func newTestCrawlerService(repo *crawlerTestRepository) *services.CrawlerService {
	broker := services.NewPubSubBroker()
	reportManager := services.NewReportManager(&crawlerReportManagerTestRepository{})
	handler := services.NewCrawlerHandler(&crawlerHandlerTestRepository{}, broker, reportManager)

	return services.NewCrawlerService(repo, services.CrawlerServicesContainer{
		Broker:         broker,
		ReportManager:  reportManager,
		CrawlerHandler: handler,
		ArchiveService: services.NewArchiveService(""),
		Config:         &config.CrawlerConfig{Agent: "testbot"},
	})
}

// TestStartCrawlerNoDuplicateDBRecord verifies that when StartCrawler is called
// while a crawl is already in progress, it returns an error and does not write
// a second crawl record to the DB — preventing the orphaned NULL-end-timestamp
// bug that permanently blocks future crawls.
func TestStartCrawlerNoDuplicateDBRecord(t *testing.T) {
	repo := &crawlerTestRepository{}
	svc := newTestCrawlerService(repo)

	// localhost:1 refuses connections immediately, so the background goroutine
	// finishes quickly without making the test slow.
	p := models.Project{Id: 1, URL: "http://localhost:1"}

	if err := svc.StartCrawler(p, models.BasicAuth{}); err != nil {
		t.Fatalf("first StartCrawler: unexpected error: %v", err)
	}

	// Second call while the first goroutine still holds the in-memory lock.
	err := svc.StartCrawler(p, models.BasicAuth{})
	if err == nil {
		t.Fatal("second StartCrawler: expected an error, got nil")
	}

	if repo.saveCrawlCount != 1 {
		t.Errorf("SaveCrawl called %d time(s), want exactly 1", repo.saveCrawlCount)
	}
}
