package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/stjudewashere/seonaut/internal/models"
)

type CrawlCompletion struct {
	State          CrawlState
	FinishedAt     time.Time
	FailureCode    string
	FailureMessage string
	TotalURLs      int
	TotalIssues    int
	ArchiveReady   bool
}

type CrawlStore interface {
	ReserveCrawl(context.Context, Principal, string, string, string) (APICrawl, bool, error)
	MarkCrawlRunning(context.Context, Principal, string, int64, time.Time) error
	CompleteCrawl(context.Context, string, string, CrawlCompletion) error
	ListCrawls(context.Context, Principal, string) ([]APICrawl, error)
	GetCrawl(context.Context, Principal, string, string) (APICrawl, error)
	RequestCancel(context.Context, Principal, string, string, time.Time) (APICrawl, error)
}

type CrawlProjectLookup interface {
	GetUpstreamProject(context.Context, Principal, string) (models.Project, error)
}

type CrawlRunner interface {
	StartCrawlerObserved(models.Project, models.BasicAuth, string, func(*models.Crawl, bool, bool, error)) (*models.Crawl, error)
	StopCrawler(models.Project)
}

type CrawlCompletionObserver interface {
	CrawlCompleted(context.Context, Principal, string, string, models.Project, CrawlCompletion)
}

type CrawlManager struct {
	Store              CrawlStore
	Projects           CrawlProjectLookup
	Runner             CrawlRunner
	CompletionObserver CrawlCompletionObserver
	Slots              *ConcurrencyBudget
	Targets            TargetValidator
	Now                func() time.Time
}

func (m CrawlManager) StartCrawl(ctx context.Context, principal Principal, projectID, idempotencyKey string) (APICrawl, bool, error) {
	projectID, idempotencyKey = strings.TrimSpace(projectID), strings.TrimSpace(idempotencyKey)
	if m.Store == nil || m.Projects == nil || m.Runner == nil || principal.KeyID == "" || principal.TenantID == "" || projectID == "" {
		return APICrawl{}, false, ErrCrawlNotFound
	}
	if idempotencyKey == "" || len(idempotencyKey) > 191 {
		return APICrawl{}, false, ErrIdempotencyKeyRequired
	}
	releaseSlot := func() {}
	if m.Slots != nil {
		var acquired bool
		releaseSlot, acquired = m.Slots.Acquire(principal.TenantID)
		if !acquired {
			return APICrawl{}, false, ErrCrawlQuotaExceeded
		}
	}
	slotOwned := true
	defer func() {
		if slotOwned {
			releaseSlot()
		}
	}()
	project, err := m.Projects.GetUpstreamProject(ctx, principal, projectID)
	if err != nil {
		return APICrawl{}, false, err
	}
	if m.Targets != nil {
		target, parseErr := url.Parse(project.URL)
		if parseErr != nil {
			return APICrawl{}, false, ErrTargetForbidden
		}
		if err := m.Targets.ValidateURL(ctx, target); err != nil {
			return APICrawl{}, false, err
		}
	}
	hashBytes := sha256.Sum256([]byte("project_id=" + projectID))
	crawl, replayed, err := m.Store.ReserveCrawl(ctx, principal, projectID, idempotencyKey, hex.EncodeToString(hashBytes[:]))
	if err != nil || replayed {
		return crawl, replayed, err
	}

	startedAt := m.now()
	completion := func(upstream *models.Crawl, canceled, archiveReady bool, runErr error) {
		defer releaseSlot()
		result := CrawlCompletion{State: CrawlSucceeded, FinishedAt: m.now(), ArchiveReady: archiveReady}
		bindingFailed := false
		if upstream != nil {
			result.TotalURLs = upstream.TotalURLs
			result.TotalIssues = upstream.TotalIssues
			if err := m.Store.MarkCrawlRunning(context.Background(), principal, crawl.ID, upstream.Id, startedAt); err != nil {
				result.State = CrawlFailed
				result.FailureCode = "state_persist_failed"
				result.FailureMessage = "Crawl state could not be persisted"
				bindingFailed = true
				archiveReady = false
				result.ArchiveReady = false
			}
		}
		if !bindingFailed && canceled {
			result.State = CrawlCanceled
		} else if !bindingFailed && runErr != nil {
			result.State = CrawlFailed
			result.FailureCode = "crawl_failed"
			result.FailureMessage = "Crawler execution failed"
		}
		completionCtx := context.Background()
		if m.CompletionObserver != nil {
			m.CompletionObserver.CrawlCompleted(completionCtx, principal, projectID, crawl.ID, project, result)
		}
		if err := m.Store.CompleteCrawl(completionCtx, principal.TenantID, crawl.ID, result); err != nil {
			if retryErr := m.Store.CompleteCrawl(completionCtx, principal.TenantID, crawl.ID, result); retryErr != nil {
				log.Printf("Persist terminal API crawl crawl_id=%s: %v (retry: %v)", crawl.ID, err, retryErr)
			}
		}
	}
	upstream, err := m.Runner.StartCrawlerObserved(project, models.BasicAuth{}, crawl.ID, completion)
	if err != nil {
		_ = m.Store.CompleteCrawl(context.Background(), principal.TenantID, crawl.ID, CrawlCompletion{State: CrawlFailed, FinishedAt: m.now(), FailureCode: "start_failed", FailureMessage: "Crawler could not be started"})
		return APICrawl{}, false, err
	}
	if err := m.Store.MarkCrawlRunning(ctx, principal, crawl.ID, upstream.Id, startedAt); err != nil {
		m.Runner.StopCrawler(project)
		_ = m.Store.CompleteCrawl(context.Background(), principal.TenantID, crawl.ID, CrawlCompletion{State: CrawlFailed, FinishedAt: m.now(), FailureCode: "state_persist_failed", FailureMessage: "Crawl state could not be persisted"})
		return APICrawl{}, false, err
	}
	slotOwned = false
	crawl.State, crawl.StartedAt = CrawlRunning, &startedAt
	return crawl, false, nil
}

func (m CrawlManager) ListCrawls(ctx context.Context, principal Principal, projectID string) ([]APICrawl, error) {
	if m.Store == nil {
		return nil, ErrProjectNotFound
	}
	return m.Store.ListCrawls(ctx, principal, projectID)
}

func (m CrawlManager) GetCrawl(ctx context.Context, principal Principal, projectID, crawlID string) (APICrawl, error) {
	if m.Store == nil {
		return APICrawl{}, ErrCrawlNotFound
	}
	return m.Store.GetCrawl(ctx, principal, projectID, crawlID)
}

func (m CrawlManager) CancelCrawl(ctx context.Context, principal Principal, projectID, crawlID string) (APICrawl, error) {
	if m.Store == nil || m.Projects == nil || m.Runner == nil {
		return APICrawl{}, ErrCrawlNotFound
	}
	crawl, err := m.Store.RequestCancel(ctx, principal, projectID, crawlID, m.now())
	if err != nil {
		return APICrawl{}, err
	}
	if terminalCrawlState(crawl.State) {
		return crawl, nil
	}
	project, err := m.Projects.GetUpstreamProject(ctx, principal, projectID)
	if err != nil {
		return APICrawl{}, err
	}
	m.Runner.StopCrawler(project)
	return crawl, nil
}

func terminalCrawlState(state CrawlState) bool {
	return state == CrawlSucceeded || state == CrawlFailed || state == CrawlCanceled
}

func (m CrawlManager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}
