package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/stjudewashere/seonaut/internal/api"
	"github.com/stjudewashere/seonaut/internal/models"
)

const apiProjectSelect = `SELECT ap.id, ap.external_project_id, ap.tenant_id, p.url, p.ignore_robotstxt, p.follow_nofollow, p.include_noindex, p.crawl_sitemap, p.allow_subdomains, p.basic_auth, p.check_external_links, p.archive, p.user_agent, ap.created_at, ap.updated_at FROM api_projects ap JOIN api_tenants atn ON atn.id = ap.tenant_id AND atn.upstream_user_id = ap.upstream_user_id JOIN projects p ON p.id = ap.upstream_project_id AND p.user_id = ap.upstream_user_id`

const (
	listAPIProjectsSQL          = apiProjectSelect + ` WHERE ap.tenant_id = ? ORDER BY ap.created_at, ap.id`
	listBoundAPIProjectsSQL     = apiProjectSelect + ` WHERE ap.tenant_id = ? AND ap.id = ? ORDER BY ap.created_at, ap.id`
	getAPIProjectSQL            = apiProjectSelect + ` WHERE ap.tenant_id = ? AND ap.id = ? LIMIT 1`
	getAPIProjectForUpdateSQL   = apiProjectSelect + ` WHERE ap.tenant_id = ? AND ap.id = ? LIMIT 1 FOR UPDATE`
	findAPITenantOwnerSQL       = `SELECT upstream_user_id FROM api_tenants WHERE id = ? AND state = 'active' LIMIT 1`
	findAPIProjectByExternalSQL = `SELECT id, upstream_project_id FROM api_projects WHERE tenant_id = ? AND external_project_id = ? LIMIT 1 FOR UPDATE`
	insertUpstreamAPIProjectSQL = `INSERT INTO projects (url, ignore_robotstxt, follow_nofollow, include_noindex, crawl_sitemap, allow_subdomains, basic_auth, user_id, check_external_links, archive, user_agent) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	insertAPIProjectBindingSQL  = `INSERT INTO api_projects (id, tenant_id, external_project_id, upstream_project_id, upstream_user_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`
	updateUpstreamAPIProjectSQL = `UPDATE projects p JOIN api_projects ap ON ap.upstream_project_id = p.id JOIN api_tenants atn ON atn.id = ap.tenant_id AND atn.upstream_user_id = p.user_id SET p.url = ?, p.ignore_robotstxt = ?, p.follow_nofollow = ?, p.include_noindex = ?, p.crawl_sitemap = ?, p.allow_subdomains = ?, p.basic_auth = ?, p.check_external_links = ?, p.archive = ?, p.user_agent = ?, ap.updated_at = ? WHERE ap.tenant_id = ? AND ap.id = ?`
	patchUpstreamAPIProjectSQL  = `UPDATE projects p JOIN api_projects ap ON ap.upstream_project_id = p.id JOIN api_tenants atn ON atn.id = ap.tenant_id AND atn.upstream_user_id = p.user_id SET p.ignore_robotstxt = ?, p.follow_nofollow = ?, p.include_noindex = ?, p.crawl_sitemap = ?, p.allow_subdomains = ?, p.basic_auth = ?, p.check_external_links = ?, p.archive = ?, p.user_agent = ?, ap.updated_at = ? WHERE ap.tenant_id = ? AND ap.id = ?`
)

type APIProjectRepository struct {
	DB  *sql.DB
	Now func() time.Time
}

func (r APIProjectRepository) PutProject(ctx context.Context, principal api.Principal, externalID, idempotencyKey, requestHash string, project models.Project) (api.Project, bool, error) {
	if r.DB == nil || principal.KeyID == "" || principal.TenantID == "" {
		return api.Project{}, false, api.ErrInvalidProject
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return api.Project{}, false, err
	}
	defer tx.Rollback()

	record, err := findIdempotency(ctx, tx, principal.KeyID, operationPutProject, idempotencyKey)
	if err == nil {
		if record.RequestHash != requestHash {
			return api.Project{}, false, api.ErrIdempotencyConflict
		}
		stored, err := getAPIProject(ctx, tx, principal, record.ResourceID, false)
		if err != nil {
			return api.Project{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return api.Project{}, false, err
		}
		return stored, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return api.Project{}, false, err
	}

	var ownerID int
	if err := tx.QueryRowContext(ctx, findAPITenantOwnerSQL, principal.TenantID).Scan(&ownerID); errors.Is(err, sql.ErrNoRows) {
		return api.Project{}, false, api.ErrProjectNotFound
	} else if err != nil {
		return api.Project{}, false, err
	}

	now := r.now()
	var projectID string
	var upstreamID int64
	err = tx.QueryRowContext(ctx, findAPIProjectByExternalSQL, principal.TenantID, externalID).Scan(&projectID, &upstreamID)
	if errors.Is(err, sql.ErrNoRows) {
		result, err := tx.ExecContext(ctx, insertUpstreamAPIProjectSQL, project.URL, project.IgnoreRobotsTxt, project.FollowNofollow, project.IncludeNoindex, project.CrawlSitemap, project.AllowSubdomains, project.BasicAuth, ownerID, project.CheckExternalLinks, project.Archive, project.UserAgent)
		if err != nil {
			return api.Project{}, false, err
		}
		upstreamID, err = result.LastInsertId()
		if err != nil {
			return api.Project{}, false, err
		}
		projectID, err = newOpaqueID()
		if err != nil {
			return api.Project{}, false, err
		}
		if _, err := tx.ExecContext(ctx, insertAPIProjectBindingSQL, projectID, principal.TenantID, externalID, upstreamID, ownerID, now, now); err != nil {
			return api.Project{}, false, err
		}
	} else if err != nil {
		return api.Project{}, false, err
	} else {
		result, err := tx.ExecContext(ctx, updateUpstreamAPIProjectSQL, project.URL, project.IgnoreRobotsTxt, project.FollowNofollow, project.IncludeNoindex, project.CrawlSitemap, project.AllowSubdomains, project.BasicAuth, project.CheckExternalLinks, project.Archive, project.UserAgent, now, principal.TenantID, projectID)
		if err != nil {
			return api.Project{}, false, err
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return api.Project{}, false, api.ErrProjectNotFound
		}
	}
	if err := saveIdempotency(ctx, tx, principal.KeyID, principal.TenantID, operationPutProject, idempotencyKey, requestHash, "project", projectID, now); err != nil {
		return api.Project{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return api.Project{}, false, err
	}
	return api.Project{ID: projectID, ExternalID: externalID, TenantID: principal.TenantID, ProjectInput: apiProjectInput(project), CreatedAt: now, UpdatedAt: now}, false, nil
}

func (r APIProjectRepository) ListProjects(ctx context.Context, principal api.Principal) ([]api.Project, error) {
	if r.DB == nil || principal.TenantID == "" {
		return nil, api.ErrProjectNotFound
	}
	query, args := listAPIProjectsSQL, []any{principal.TenantID}
	if principal.ProjectID != "" {
		query, args = listBoundAPIProjectsSQL, []any{principal.TenantID, principal.ProjectID}
	}
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := []api.Project{}
	for rows.Next() {
		project, err := scanAPIProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

func (r APIProjectRepository) GetProject(ctx context.Context, principal api.Principal, projectID string) (api.Project, error) {
	if r.DB == nil || principal.TenantID == "" || principal.ProjectID != "" && principal.ProjectID != projectID {
		return api.Project{}, api.ErrProjectNotFound
	}
	return getAPIProject(ctx, r.DB, principal, projectID, false)
}

func (r APIProjectRepository) PatchProject(ctx context.Context, principal api.Principal, projectID string, project models.Project) (api.Project, error) {
	if r.DB == nil || principal.TenantID == "" || principal.ProjectID != "" {
		return api.Project{}, api.ErrProjectNotFound
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return api.Project{}, err
	}
	defer tx.Rollback()
	current, err := getAPIProject(ctx, tx, principal, projectID, true)
	if err != nil {
		return api.Project{}, err
	}
	now := r.now()
	result, err := tx.ExecContext(ctx, patchUpstreamAPIProjectSQL, project.IgnoreRobotsTxt, project.FollowNofollow, project.IncludeNoindex, project.CrawlSitemap, project.AllowSubdomains, project.BasicAuth, project.CheckExternalLinks, project.Archive, project.UserAgent, now, principal.TenantID, projectID)
	if err != nil {
		return api.Project{}, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return api.Project{}, api.ErrProjectNotFound
	}
	if err := tx.Commit(); err != nil {
		return api.Project{}, err
	}
	current.ProjectInput = apiProjectInput(project)
	current.UpdatedAt = now
	return current, nil
}

type apiProjectScanner interface{ Scan(...any) error }
type apiProjectQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getAPIProject(ctx context.Context, queryer apiProjectQueryer, principal api.Principal, projectID string, forUpdate bool) (api.Project, error) {
	if principal.ProjectID != "" && principal.ProjectID != projectID {
		return api.Project{}, api.ErrProjectNotFound
	}
	query := getAPIProjectSQL
	if forUpdate {
		query = getAPIProjectForUpdateSQL
	}
	return scanAPIProject(queryer.QueryRowContext(ctx, query, principal.TenantID, projectID))
}

func scanAPIProject(row apiProjectScanner) (api.Project, error) {
	var project api.Project
	err := row.Scan(&project.ID, &project.ExternalID, &project.TenantID, &project.URL, &project.IgnoreRobotsTxt, &project.FollowNofollow, &project.IncludeNoindex, &project.CrawlSitemap, &project.AllowSubdomains, &project.BasicAuth, &project.CheckExternalLinks, &project.Archive, &project.UserAgent, &project.CreatedAt, &project.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Project{}, api.ErrProjectNotFound
	}
	return project, err
}

func apiProjectInput(project models.Project) api.ProjectInput {
	return api.ProjectInput{URL: project.URL, IgnoreRobotsTxt: project.IgnoreRobotsTxt, FollowNofollow: project.FollowNofollow, IncludeNoindex: project.IncludeNoindex, CrawlSitemap: project.CrawlSitemap, AllowSubdomains: project.AllowSubdomains, BasicAuth: project.BasicAuth, CheckExternalLinks: project.CheckExternalLinks, Archive: project.Archive, UserAgent: project.UserAgent}
}

func (r APIProjectRepository) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}
