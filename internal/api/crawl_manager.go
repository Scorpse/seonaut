package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	StartCrawlerObserved(models.Project, models.BasicAuth, func(*models.Crawl, bool, error)) (*models.Crawl, error)
	StopCrawler(models.Project)
}

type CrawlManager struct {
	Store    CrawlStore
	Projects CrawlProjectLookup
	Runner   CrawlRunner
	Now      func() time.Time
}

func (m CrawlManager) StartCrawl(ctx context.Context, principal Principal, projectID, idempotencyKey string) (APICrawl, bool, error) {
	projectID, idempotencyKey = strings.TrimSpace(projectID), strings.TrimSpace(idempotencyKey)
	if m.Store == nil || m.Projects == nil || m.Runner == nil || principal.KeyID == "" || principal.TenantID == "" || projectID == "" {
		return APICrawl{}, false, ErrCrawlNotFound
	}
	if idempotencyKey == "" || len(idempotencyKey) > 191 {
		return APICrawl{}, false, ErrIdempotencyKeyRequired
	}
	project, err := m.Projects.GetUpstreamProject(ctx, principal, projectID)
	if err != nil {
		return APICrawl{}, false, err
	}
	hashBytes := sha256.Sum256([]byte("project_id=" + projectID))
	crawl, replayed, err := m.Store.ReserveCrawl(ctx, principal, projectID, idempotencyKey, hex.EncodeToString(hashBytes[:]))
	if err != nil || replayed {
		return crawl, replayed, err
	}

	completion := func(upstream *models.Crawl, canceled bool, runErr error) {
		result := CrawlCompletion{State: CrawlSucceeded, FinishedAt: m.now()}
		if upstream != nil {
			result.TotalURLs = upstream.TotalURLs
			result.TotalIssues = upstream.TotalIssues
		}
		if canceled {
			result.State = CrawlCanceled
		} else if runErr != nil {
			result.State = CrawlFailed
			result.FailureCode = "crawl_failed"
			result.FailureMessage = "Crawler execution failed"
		}
		_ = m.Store.CompleteCrawl(context.Background(), principal.TenantID, crawl.ID, result)
	}
	upstream, err := m.Runner.StartCrawlerObserved(project, models.BasicAuth{}, completion)
	if err != nil {
		_ = m.Store.CompleteCrawl(context.Background(), principal.TenantID, crawl.ID, CrawlCompletion{State: CrawlFailed, FinishedAt: m.now(), FailureCode: "start_failed", FailureMessage: "Crawler could not be started"})
		return APICrawl{}, false, err
	}
	startedAt := m.now()
	if err := m.Store.MarkCrawlRunning(ctx, principal, crawl.ID, upstream.Id, startedAt); err != nil {
		m.Runner.StopCrawler(project)
		_ = m.Store.CompleteCrawl(context.Background(), principal.TenantID, crawl.ID, CrawlCompletion{State: CrawlFailed, FinishedAt: m.now(), FailureCode: "state_persist_failed", FailureMessage: "Crawl state could not be persisted"})
		return APICrawl{}, false, err
	}
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
