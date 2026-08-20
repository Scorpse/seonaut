package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/stjudewashere/seonaut/internal/api"
)

const apiCrawlSelect = `SELECT ac.id, ac.tenant_id, ac.project_id, ac.state, ac.cancel_requested, ac.queued_at, ac.started_at, ac.finished_at, COALESCE(ac.failure_code, ''), COALESCE(ac.failure_message, ''), ac.total_urls, ac.total_issues FROM api_crawls ac JOIN api_projects ap ON ap.id = ac.project_id AND ap.tenant_id = ac.tenant_id AND ap.upstream_project_id = ac.upstream_project_id`

const (
	findAPIProjectUpstreamIDSQL = `SELECT upstream_project_id FROM api_projects WHERE tenant_id = ? AND id = ? LIMIT 1`
	insertAPICrawlSQL           = `INSERT INTO api_crawls (id, tenant_id, project_id, upstream_project_id, state, active_slot, queued_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	listAPICrawlsSQL            = apiCrawlSelect + ` WHERE ac.tenant_id = ? AND ac.project_id = ? ORDER BY ac.queued_at, ac.id`
	listBoundAPICrawlsSQL       = apiCrawlSelect + ` WHERE ac.tenant_id = ? AND ac.project_id = ? AND ac.project_id = ? ORDER BY ac.queued_at, ac.id`
	getAPICrawlSQL              = apiCrawlSelect + ` WHERE ac.tenant_id = ? AND ac.project_id = ? AND ac.id = ? LIMIT 1`
	getAPICrawlForUpdateSQL     = apiCrawlSelect + ` WHERE ac.tenant_id = ? AND ac.project_id = ? AND ac.id = ? LIMIT 1 FOR UPDATE`
	markAPICrawlRunningSQL      = `UPDATE api_crawls SET upstream_crawl_id = ?, state = 'running', started_at = ?, updated_at = ? WHERE tenant_id = ? AND id = ? AND state = 'queued' AND active_slot = 1`
	completeAPICrawlSQL         = `UPDATE api_crawls SET state = ?, active_slot = ?, failure_code = ?, failure_message = ?, total_urls = ?, total_issues = ?, finished_at = ?, updated_at = ? WHERE tenant_id = ? AND id = ? AND state IN ('queued', 'running') AND active_slot = 1`
	findAPICrawlStateSQL        = `SELECT state FROM api_crawls WHERE tenant_id = ? AND id = ? LIMIT 1`
	requestCancelAPICrawlSQL    = `UPDATE api_crawls SET cancel_requested = 1, updated_at = ? WHERE tenant_id = ? AND project_id = ? AND id = ? AND state IN ('queued', 'running')`
)

type APICrawlRepository struct {
	DB  *sql.DB
	Now func() time.Time
}

func (r APICrawlRepository) ReserveCrawl(ctx context.Context, principal api.Principal, projectID, idempotencyKey, requestHash string) (api.APICrawl, bool, error) {
	if r.DB == nil || principal.KeyID == "" || principal.TenantID == "" {
		return api.APICrawl{}, false, api.ErrCrawlNotFound
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return api.APICrawl{}, false, err
	}
	defer tx.Rollback()
	record, err := findIdempotency(ctx, tx, principal.KeyID, operationStartCrawl, idempotencyKey)
	if err == nil {
		if record.RequestHash != requestHash {
			return api.APICrawl{}, false, api.ErrIdempotencyConflict
		}
		crawl, err := getAPICrawl(ctx, tx, principal, projectID, record.ResourceID, false)
		if err != nil {
			return api.APICrawl{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return api.APICrawl{}, false, err
		}
		return crawl, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return api.APICrawl{}, false, err
	}
	var upstreamProjectID int64
	if err := tx.QueryRowContext(ctx, findAPIProjectUpstreamIDSQL, principal.TenantID, projectID).Scan(&upstreamProjectID); errors.Is(err, sql.ErrNoRows) {
		return api.APICrawl{}, false, api.ErrProjectNotFound
	} else if err != nil {
		return api.APICrawl{}, false, err
	}
	id, err := newOpaqueID()
	if err != nil {
		return api.APICrawl{}, false, err
	}
	now := r.now()
	_, err = tx.ExecContext(ctx, insertAPICrawlSQL, id, principal.TenantID, projectID, upstreamProjectID, api.CrawlQueued, 1, now, now)
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return api.APICrawl{}, false, api.ErrCrawlAlreadyActive
		}
		return api.APICrawl{}, false, err
	}
	if err := saveIdempotency(ctx, tx, principal.KeyID, principal.TenantID, operationStartCrawl, idempotencyKey, requestHash, "crawl", id, now); err != nil {
		return api.APICrawl{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return api.APICrawl{}, false, err
	}
	return api.APICrawl{ID: id, TenantID: principal.TenantID, ProjectID: projectID, State: api.CrawlQueued, QueuedAt: now}, false, nil
}

func (r APICrawlRepository) MarkCrawlRunning(ctx context.Context, principal api.Principal, crawlID string, upstreamID int64, startedAt time.Time) error {
	result, err := r.DB.ExecContext(ctx, markAPICrawlRunningSQL, upstreamID, startedAt, startedAt, principal.TenantID, crawlID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count == 1 {
		return nil
	}
	state, err := r.crawlState(ctx, principal.TenantID, crawlID)
	if err == nil && (state == api.CrawlRunning || terminalCrawlState(state)) {
		return nil
	}
	return api.ErrCrawlNotFound
}

func (r APICrawlRepository) CompleteCrawl(ctx context.Context, tenantID, crawlID string, completion api.CrawlCompletion) error {
	if !terminalCrawlState(completion.State) {
		return errors.New("completion state is not terminal")
	}
	result, err := r.DB.ExecContext(ctx, completeAPICrawlSQL, completion.State, nil, nullableString(completion.FailureCode), nullableString(completion.FailureMessage), completion.TotalURLs, completion.TotalIssues, completion.FinishedAt, completion.FinishedAt, tenantID, crawlID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count == 1 {
		return nil
	}
	state, err := r.crawlState(ctx, tenantID, crawlID)
	if err == nil && terminalCrawlState(state) {
		return nil
	}
	return api.ErrCrawlNotFound
}

func (r APICrawlRepository) ListCrawls(ctx context.Context, principal api.Principal, projectID string) ([]api.APICrawl, error) {
	if r.DB == nil || principal.TenantID == "" || principal.ProjectID != "" && principal.ProjectID != projectID {
		return nil, api.ErrProjectNotFound
	}
	query, args := listAPICrawlsSQL, []any{principal.TenantID, projectID}
	if principal.ProjectID != "" {
		query, args = listBoundAPICrawlsSQL, []any{principal.TenantID, projectID, principal.ProjectID}
	}
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []api.APICrawl{}
	for rows.Next() {
		item, err := scanAPICrawl(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		var upstreamID int64
		if err := r.DB.QueryRowContext(ctx, findAPIProjectUpstreamIDSQL, principal.TenantID, projectID).Scan(&upstreamID); errors.Is(err, sql.ErrNoRows) {
			return nil, api.ErrProjectNotFound
		} else if err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (r APICrawlRepository) GetCrawl(ctx context.Context, principal api.Principal, projectID, crawlID string) (api.APICrawl, error) {
	if r.DB == nil || principal.TenantID == "" || principal.ProjectID != "" && principal.ProjectID != projectID {
		return api.APICrawl{}, api.ErrCrawlNotFound
	}
	return getAPICrawl(ctx, r.DB, principal, projectID, crawlID, false)
}

func (r APICrawlRepository) RequestCancel(ctx context.Context, principal api.Principal, projectID, crawlID string, at time.Time) (api.APICrawl, error) {
	if r.DB == nil || principal.TenantID == "" || principal.ProjectID != "" {
		return api.APICrawl{}, api.ErrCrawlNotFound
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return api.APICrawl{}, err
	}
	defer tx.Rollback()
	crawl, err := getAPICrawl(ctx, tx, principal, projectID, crawlID, true)
	if err != nil {
		return api.APICrawl{}, err
	}
	if terminalCrawlState(crawl.State) {
		if err := tx.Commit(); err != nil {
			return api.APICrawl{}, err
		}
		return crawl, nil
	}
	if crawl.CancelRequested {
		if err := tx.Commit(); err != nil {
			return api.APICrawl{}, err
		}
		return crawl, nil
	}
	result, err := tx.ExecContext(ctx, requestCancelAPICrawlSQL, at, principal.TenantID, projectID, crawlID)
	if err != nil {
		return api.APICrawl{}, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return api.APICrawl{}, api.ErrCrawlNotFound
	}
	if err := tx.Commit(); err != nil {
		return api.APICrawl{}, err
	}
	crawl.CancelRequested = true
	return crawl, nil
}

func (r APICrawlRepository) RecoverInterruptedCrawls(ctx context.Context, at time.Time) (int64, error) {
	result, err := r.DB.ExecContext(ctx, `UPDATE api_crawls SET state = 'failed', active_slot = NULL, finished_at = ?, failure_code = 'worker_restarted', failure_message = 'Crawler worker restarted before completion', updated_at = ? WHERE state IN ('queued', 'running')`, at, at)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r APICrawlRepository) crawlState(ctx context.Context, tenantID, crawlID string) (api.CrawlState, error) {
	var state api.CrawlState
	err := r.DB.QueryRowContext(ctx, findAPICrawlStateSQL, tenantID, crawlID).Scan(&state)
	return state, err
}

type apiCrawlQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
type apiCrawlScanner interface{ Scan(...any) error }

func getAPICrawl(ctx context.Context, queryer apiCrawlQueryer, principal api.Principal, projectID, crawlID string, forUpdate bool) (api.APICrawl, error) {
	if principal.ProjectID != "" && principal.ProjectID != projectID {
		return api.APICrawl{}, api.ErrCrawlNotFound
	}
	query := getAPICrawlSQL
	if forUpdate {
		query = getAPICrawlForUpdateSQL
	}
	return scanAPICrawl(queryer.QueryRowContext(ctx, query, principal.TenantID, projectID, crawlID))
}

func scanAPICrawl(row apiCrawlScanner) (api.APICrawl, error) {
	var crawl api.APICrawl
	var started, finished sql.NullTime
	err := row.Scan(&crawl.ID, &crawl.TenantID, &crawl.ProjectID, &crawl.State, &crawl.CancelRequested, &crawl.QueuedAt, &started, &finished, &crawl.FailureCode, &crawl.FailureMessage, &crawl.TotalURLs, &crawl.TotalIssues)
	if errors.Is(err, sql.ErrNoRows) {
		return api.APICrawl{}, api.ErrCrawlNotFound
	}
	if err != nil {
		return api.APICrawl{}, err
	}
	if started.Valid {
		crawl.StartedAt = &started.Time
	}
	if finished.Valid {
		crawl.FinishedAt = &finished.Time
	}
	return crawl, nil
}

func terminalCrawlState(state api.CrawlState) bool {
	return state == api.CrawlSucceeded || state == api.CrawlFailed || state == api.CrawlCanceled
}
func (r APICrawlRepository) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}
