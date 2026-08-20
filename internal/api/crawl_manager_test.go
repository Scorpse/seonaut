package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stjudewashere/seonaut/internal/models"
)

type managerCrawlStore struct {
	crawl      APICrawl
	replayed   bool
	reserveErr error
	completed  []CrawlCompletion
	runningID  int64
	events     *[]string
}

func (s *managerCrawlStore) ReserveCrawl(context.Context, Principal, string, string, string) (APICrawl, bool, error) {
	return s.crawl, s.replayed, s.reserveErr
}
func (s *managerCrawlStore) MarkCrawlRunning(_ context.Context, _ Principal, _ string, upstreamID int64, _ time.Time) error {
	s.runningID = upstreamID
	return nil
}
func (s *managerCrawlStore) CompleteCrawl(_ context.Context, _ string, _ string, completion CrawlCompletion) error {
	if s.events != nil {
		*s.events = append(*s.events, "terminal")
	}
	s.completed = append(s.completed, completion)
	return nil
}
func (s *managerCrawlStore) ListCrawls(context.Context, Principal, string) ([]APICrawl, error) {
	return []APICrawl{s.crawl}, nil
}
func (s *managerCrawlStore) GetCrawl(context.Context, Principal, string, string) (APICrawl, error) {
	return s.crawl, nil
}
func (s *managerCrawlStore) RequestCancel(context.Context, Principal, string, string, time.Time) (APICrawl, error) {
	s.crawl.CancelRequested = true
	return s.crawl, nil
}

type managerProjectLookup struct {
	project models.Project
	err     error
}

func (l managerProjectLookup) GetUpstreamProject(context.Context, Principal, string) (models.Project, error) {
	return l.project, l.err
}

type managerCrawlRunner struct {
	started    int
	stopped    int
	completion func(*models.Crawl, bool, bool, error)
	startErr   error
	immediate  bool
}

type managerCompletionObserver struct {
	projectID  string
	crawlID    string
	project    models.Project
	completion CrawlCompletion
	events     *[]string
}

func (o *managerCompletionObserver) CrawlCompleted(_ context.Context, _ Principal, projectID, crawlID string, project models.Project, completion CrawlCompletion) {
	if o.events != nil {
		*o.events = append(*o.events, "archive")
	}
	o.projectID, o.crawlID, o.project, o.completion = projectID, crawlID, project, completion
}

func (r *managerCrawlRunner) StartCrawlerObserved(_ models.Project, _ models.BasicAuth, _ string, completion func(*models.Crawl, bool, bool, error)) (*models.Crawl, error) {
	r.started++
	r.completion = completion
	if r.startErr != nil {
		return nil, r.startErr
	}
	upstream := &models.Crawl{Id: 44}
	if r.immediate {
		completion(upstream, false, false, nil)
	}
	return upstream, nil
}

func TestCrawlManagerBindsUpstreamIDBeforeImmediateCompletion(t *testing.T) {
	store := &managerCrawlStore{crawl: APICrawl{ID: "crawl-fast", TenantID: "tenant-a", ProjectID: "project-a", State: CrawlQueued}}
	runner := &managerCrawlRunner{immediate: true}
	manager := CrawlManager{Store: store, Projects: managerProjectLookup{project: models.Project{Id: 7}}, Runner: runner}
	if _, _, err := manager.StartCrawl(context.Background(), Principal{KeyID: "key-a", TenantID: "tenant-a"}, "project-a", "idem-fast"); err != nil {
		t.Fatal(err)
	}
	if store.runningID != 44 || len(store.completed) != 1 || store.completed[0].State != CrawlSucceeded {
		t.Fatalf("runningID=%d completed=%+v", store.runningID, store.completed)
	}
}
func (r *managerCrawlRunner) StopCrawler(models.Project) { r.stopped++ }

func TestCrawlManagerPersistsRunningAndTerminalStates(t *testing.T) {
	events := []string{}
	store := &managerCrawlStore{crawl: APICrawl{ID: "crawl-a", TenantID: "tenant-a", ProjectID: "project-a", State: CrawlQueued}, events: &events}
	runner := &managerCrawlRunner{}
	observer := &managerCompletionObserver{events: &events}
	manager := CrawlManager{Store: store, Projects: managerProjectLookup{project: models.Project{Id: 7, URL: "https://example.com"}}, Runner: runner, CompletionObserver: observer, Now: func() time.Time { return time.Unix(2, 0).UTC() }}
	principal := Principal{KeyID: "key-a", TenantID: "tenant-a"}
	crawl, replayed, err := manager.StartCrawl(context.Background(), principal, "project-a", "idem-a")
	if err != nil || replayed || crawl.ID != "crawl-a" {
		t.Fatalf("crawl=%+v replayed=%v err=%v", crawl, replayed, err)
	}
	if store.runningID != 44 || runner.completion == nil {
		t.Fatalf("runningID=%d completion=%v", store.runningID, runner.completion != nil)
	}
	runner.completion(&models.Crawl{Id: 44, TotalURLs: 8, TotalIssues: 3}, false, true, nil)
	if len(store.completed) != 1 || store.completed[0].State != CrawlSucceeded || store.completed[0].TotalURLs != 8 {
		t.Fatalf("completed=%+v", store.completed)
	}
	if observer.projectID != "project-a" || observer.crawlID != "crawl-a" || observer.project.Id != 7 || observer.completion.State != CrawlSucceeded || !observer.completion.ArchiveReady {
		t.Fatalf("observer=%+v", observer)
	}
	if len(events) != 2 || events[0] != "archive" || events[1] != "terminal" {
		t.Fatalf("events=%v", events)
	}
}

func TestCrawlManagerReplaysWithoutStartingAnotherWorker(t *testing.T) {
	store := &managerCrawlStore{crawl: APICrawl{ID: "crawl-a", TenantID: "tenant-a", ProjectID: "project-a", State: CrawlRunning}, replayed: true}
	runner := &managerCrawlRunner{}
	manager := CrawlManager{Store: store, Projects: managerProjectLookup{project: models.Project{Id: 7}}, Runner: runner}
	_, replayed, err := manager.StartCrawl(context.Background(), Principal{KeyID: "key-a", TenantID: "tenant-a"}, "project-a", "idem-a")
	if err != nil || !replayed || runner.started != 0 {
		t.Fatalf("replayed=%v started=%d err=%v", replayed, runner.started, err)
	}
}

func TestCrawlManagerMarksSynchronousStartFailure(t *testing.T) {
	store := &managerCrawlStore{crawl: APICrawl{ID: "crawl-a", TenantID: "tenant-a", ProjectID: "project-a", State: CrawlQueued}}
	runner := &managerCrawlRunner{startErr: errors.New("start failed")}
	manager := CrawlManager{Store: store, Projects: managerProjectLookup{project: models.Project{Id: 7}}, Runner: runner}
	_, _, err := manager.StartCrawl(context.Background(), Principal{KeyID: "key-a", TenantID: "tenant-a"}, "project-a", "idem-a")
	if err == nil {
		t.Fatal("expected start error")
	}
	if len(store.completed) != 1 || store.completed[0].State != CrawlFailed || store.completed[0].FailureCode != "start_failed" {
		t.Fatalf("completed=%+v", store.completed)
	}
}

func TestCrawlManagerHoldsTenantSlotUntilCompletion(t *testing.T) {
	store := &managerCrawlStore{crawl: APICrawl{ID: "crawl-a", TenantID: "tenant-a", ProjectID: "project-a", State: CrawlQueued}}
	runner := &managerCrawlRunner{}
	manager := CrawlManager{Store: store, Projects: managerProjectLookup{project: models.Project{Id: 7}}, Runner: runner, Slots: NewConcurrencyBudget(1, 2)}
	principal := Principal{KeyID: "key-a", TenantID: "tenant-a"}
	if _, _, err := manager.StartCrawl(context.Background(), principal, "project-a", "idem-a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.StartCrawl(context.Background(), principal, "project-b", "idem-b"); !errors.Is(err, ErrCrawlQuotaExceeded) {
		t.Fatalf("second crawl error = %v, want ErrCrawlQuotaExceeded", err)
	}
	runner.completion(&models.Crawl{Id: 44}, false, false, nil)
	store.crawl.ID = "crawl-b"
	if _, _, err := manager.StartCrawl(context.Background(), principal, "project-b", "idem-c"); err != nil {
		t.Fatalf("slot not released after completion: %v", err)
	}
}

func TestCrawlManagerCancelSignalsWorkerButPreservesTerminalCrawl(t *testing.T) {
	store := &managerCrawlStore{crawl: APICrawl{ID: "crawl-a", TenantID: "tenant-a", ProjectID: "project-a", State: CrawlRunning}}
	runner := &managerCrawlRunner{}
	manager := CrawlManager{Store: store, Projects: managerProjectLookup{project: models.Project{Id: 7}}, Runner: runner}
	principal := Principal{TenantID: "tenant-a"}
	if _, err := manager.CancelCrawl(context.Background(), principal, "project-a", "crawl-a"); err != nil {
		t.Fatal(err)
	}
	if runner.stopped != 1 {
		t.Fatalf("stopped=%d", runner.stopped)
	}
	store.crawl.State = CrawlSucceeded
	if _, err := manager.CancelCrawl(context.Background(), principal, "project-a", "crawl-a"); err != nil {
		t.Fatal(err)
	}
	if runner.stopped != 1 {
		t.Fatalf("terminal crawl signaled stop: %d", runner.stopped)
	}
}
