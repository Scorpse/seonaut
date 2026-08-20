package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/stjudewashere/seonaut/internal/api"
)

const resolveAPIExportSourceSQL = `SELECT ac.upstream_crawl_id, ac.upstream_project_id, p.url, ac.state FROM api_crawls ac JOIN api_projects ap ON ap.id = ac.project_id AND ap.tenant_id = ac.tenant_id AND ap.upstream_project_id = ac.upstream_project_id JOIN projects p ON p.id = ap.upstream_project_id AND p.user_id = ap.upstream_user_id WHERE ac.tenant_id = ? AND ac.project_id = ? AND ac.id = ? AND (? = '' OR ac.project_id = ?) AND ac.upstream_crawl_id IS NOT NULL LIMIT 1`

const reserveAPIArchiveJobSQL = `INSERT INTO api_export_jobs (id, tenant_id, project_id, crawl_id, kind, state, created_at, updated_at, expires_at) VALUES (?, ?, ?, ?, 'archive.wacz', 'pending', ?, ?, ?) ON DUPLICATE KEY UPDATE updated_at = updated_at`

const getAPIArchiveJobSQL = `SELECT id, tenant_id, project_id, crawl_id, kind, state, COALESCE(artifact_path, ''), artifact_size, COALESCE(failure_code, ''), created_at, updated_at, expires_at, finished_at FROM api_export_jobs WHERE tenant_id = ? AND project_id = ? AND crawl_id = ? AND kind = 'archive.wacz' LIMIT 1`

const listExpiredAPIArchiveJobsSQL = `SELECT id, tenant_id, project_id, crawl_id, kind, state, COALESCE(artifact_path, ''), artifact_size, COALESCE(failure_code, ''), created_at, updated_at, expires_at, finished_at FROM api_export_jobs WHERE kind = 'archive.wacz' AND expires_at <= ? ORDER BY expires_at, id LIMIT 500`

const deleteExpiredAPIArchiveJobSQL = `DELETE FROM api_export_jobs WHERE id = ? AND expires_at <= ?`

const completeAPIArchiveJobSQL = `UPDATE api_export_jobs SET state = 'ready', artifact_path = ?, artifact_size = ?, failure_code = NULL, updated_at = ?, finished_at = ? WHERE tenant_id = ? AND project_id = ? AND crawl_id = ? AND kind = 'archive.wacz' AND state = 'pending'`

const failAPIArchiveJobSQL = `UPDATE api_export_jobs SET state = 'failed', artifact_path = NULL, artifact_size = 0, failure_code = ?, updated_at = ?, finished_at = ? WHERE tenant_id = ? AND project_id = ? AND crawl_id = ? AND kind = 'archive.wacz' AND state = 'pending'`

type APIExportSource struct {
	UpstreamCrawlID   int64
	UpstreamProjectID int64
	ProjectURL        string
	State             api.CrawlState
}

type APIExportJob struct {
	ID           string
	TenantID     string
	ProjectID    string
	CrawlID      string
	Kind         string
	State        api.ExportState
	ArtifactPath string
	ArtifactSize int64
	FailureCode  string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ExpiresAt    time.Time
	FinishedAt   *time.Time
}

type APIExportRepository struct {
	DB  *sql.DB
	Now func() time.Time
}

func (r APIExportRepository) ResolveSource(ctx context.Context, principal api.Principal, projectID, crawlID string) (APIExportSource, error) {
	if r.DB == nil || principal.TenantID == "" || principal.ProjectID != "" && principal.ProjectID != projectID {
		return APIExportSource{}, api.ErrCrawlNotFound
	}
	var source APIExportSource
	err := r.DB.QueryRowContext(ctx, resolveAPIExportSourceSQL, principal.TenantID, projectID, crawlID, principal.ProjectID, principal.ProjectID).Scan(&source.UpstreamCrawlID, &source.UpstreamProjectID, &source.ProjectURL, &source.State)
	if errors.Is(err, sql.ErrNoRows) {
		return APIExportSource{}, api.ErrCrawlNotFound
	}
	return source, err
}

func (r APIExportRepository) ReserveArchive(ctx context.Context, principal api.Principal, projectID, crawlID string) (APIExportJob, error) {
	if r.DB == nil || principal.TenantID == "" || principal.ProjectID != "" && principal.ProjectID != projectID {
		return APIExportJob{}, api.ErrCrawlNotFound
	}
	id, err := newOpaqueID()
	if err != nil {
		return APIExportJob{}, err
	}
	now := r.now()
	if _, err := r.DB.ExecContext(ctx, reserveAPIArchiveJobSQL, id, principal.TenantID, projectID, crawlID, now, now, now.Add(7*24*time.Hour)); err != nil {
		return APIExportJob{}, err
	}
	return scanAPIExportJob(r.DB.QueryRowContext(ctx, getAPIArchiveJobSQL, principal.TenantID, projectID, crawlID))
}

func (r APIExportRepository) ListExpiredArchives(ctx context.Context, before time.Time) ([]APIExportJob, error) {
	if r.DB == nil {
		return nil, nil
	}
	rows, err := r.DB.QueryContext(ctx, listExpiredAPIArchiveJobsSQL, before.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]APIExportJob, 0)
	for rows.Next() {
		job, err := scanAPIExportJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (r APIExportRepository) DeleteExpiredArchive(ctx context.Context, id string, before time.Time) error {
	if r.DB == nil {
		return nil
	}
	_, err := r.DB.ExecContext(ctx, deleteExpiredAPIArchiveJobSQL, id, before.UTC())
	return err
}

func (r APIExportRepository) CompleteArchive(ctx context.Context, principal api.Principal, projectID, crawlID, path string, size int64) error {
	now := r.now()
	result, err := r.DB.ExecContext(ctx, completeAPIArchiveJobSQL, path, size, now, now, principal.TenantID, projectID, crawlID)
	return requireOneAPIExportJob(result, err)
}

func (r APIExportRepository) FailArchive(ctx context.Context, principal api.Principal, projectID, crawlID, code string) error {
	now := r.now()
	result, err := r.DB.ExecContext(ctx, failAPIArchiveJobSQL, code, now, now, principal.TenantID, projectID, crawlID)
	return requireOneAPIExportJob(result, err)
}

func requireOneAPIExportJob(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("archive export job state conflict")
	}
	return nil
}

type apiExportJobScanner interface{ Scan(...any) error }

func scanAPIExportJob(row apiExportJobScanner) (APIExportJob, error) {
	var job APIExportJob
	var finished sql.NullTime
	err := row.Scan(&job.ID, &job.TenantID, &job.ProjectID, &job.CrawlID, &job.Kind, &job.State, &job.ArtifactPath, &job.ArtifactSize, &job.FailureCode, &job.CreatedAt, &job.UpdatedAt, &job.ExpiresAt, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return APIExportJob{}, api.ErrCrawlNotFound
	}
	if err != nil {
		return APIExportJob{}, err
	}
	if finished.Valid {
		job.FinishedAt = &finished.Time
	}
	return job, nil
}

func (r APIExportRepository) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}
