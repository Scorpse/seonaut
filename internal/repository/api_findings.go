package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/stjudewashere/seonaut/internal/api"
)

const authorizeAPICrawlResultsSQL = `SELECT ac.upstream_crawl_id FROM api_crawls ac JOIN api_projects ap ON ap.id = ac.project_id AND ap.tenant_id = ac.tenant_id AND ap.upstream_project_id = ac.upstream_project_id WHERE ac.tenant_id = ? AND ac.project_id = ? AND ac.id = ? AND (? = '' OR ac.project_id = ?) AND ac.upstream_crawl_id IS NOT NULL LIMIT 1`

const ownedAPICrawlCTE = `WITH owned AS (SELECT ac.upstream_crawl_id FROM api_crawls ac JOIN api_projects ap ON ap.id = ac.project_id AND ap.tenant_id = ac.tenant_id AND ap.upstream_project_id = ac.upstream_project_id WHERE ac.tenant_id = ? AND ac.project_id = ? AND ac.id = ? AND (? = '' OR ac.project_id = ?) AND ac.upstream_crawl_id IS NOT NULL)`

const listAPIIssuesSQL = ownedAPICrawlCTE + ` SELECT i.id, it.type, it.priority, pr.url, pr.status_code, COALESCE(pr.redirect_url, ''), COALESCE(pr.canonical, '') FROM owned JOIN issues i ON i.crawl_id = owned.upstream_crawl_id JOIN issue_types it ON it.id = i.issue_type_id JOIN pagereports pr ON pr.id = i.pagereport_id AND pr.crawl_id = i.crawl_id WHERE i.id > ? ORDER BY i.id LIMIT ?`

const listAPIPagesSQL = ownedAPICrawlCTE + ` SELECT pr.id, pr.url, COALESCE(pr.redirect_url, ''), pr.status_code, COALESCE(pr.content_type, ''), COALESCE(pr.lang, ''), COALESCE(pr.title, ''), COALESCE(pr.description, ''), COALESCE(pr.robots, ''), COALESCE(pr.canonical, ''), COALESCE(pr.h1, ''), COALESCE(pr.h2, ''), COALESCE(pr.words, 0), COALESCE(pr.size, 0), COALESCE(pr.depth, 0), COALESCE(pr.ttfb, 0), pr.crawled, pr.in_sitemap, pr.noindex, CASE WHEN COALESCE(pr.robots, '') LIKE '%nofollow%' OR COALESCE(pr.robots, '') LIKE '%none%' THEN 1 ELSE 0 END AS nofollow, CASE WHEN COALESCE(pr.media_type, '') = 'text/html' AND pr.status_code >= 200 AND pr.status_code < 300 AND (COALESCE(pr.canonical, '') = '' OR pr.canonical = pr.url) AND pr.crawled = 1 THEN 1 ELSE 0 END AS sitemap_eligible FROM owned JOIN pagereports pr ON pr.crawl_id = owned.upstream_crawl_id WHERE pr.id > ? ORDER BY pr.id LIMIT ?`

const listAPILinksSQL = ownedAPICrawlCTE + ` SELECT result.cursor_id, result.kind, result.origin_url, result.destination_url, result.text, result.rel, result.nofollow FROM (SELECT l.id * 2 AS cursor_id, 'internal' AS kind, pr.url AS origin_url, l.url AS destination_url, COALESCE(l.text, '') AS text, COALESCE(l.rel, '') AS rel, l.nofollow AS nofollow FROM owned JOIN links l ON l.crawl_id = owned.upstream_crawl_id JOIN pagereports pr ON pr.id = l.pagereport_id AND pr.crawl_id = l.crawl_id UNION ALL SELECT el.id * 2 + 1 AS cursor_id, 'external' AS kind, pr.url AS origin_url, el.url AS destination_url, COALESCE(el.text, '') AS text, COALESCE(el.rel, '') AS rel, el.nofollow AS nofollow FROM owned JOIN external_links el ON el.crawl_id = owned.upstream_crawl_id JOIN pagereports pr ON pr.id = el.pagereport_id AND pr.crawl_id = el.crawl_id) result WHERE result.cursor_id > ? ORDER BY result.cursor_id LIMIT ?`

const listAllAPIResourcesSQL = ownedAPICrawlCTE + ` SELECT result.cursor_id, result.resource_type, result.origin_url, result.resource_url, result.alt, result.poster FROM (SELECT r.id * 8 AS cursor_id, 'image' AS resource_type, pr.url AS origin_url, r.url AS resource_url, COALESCE(r.alt, '') AS alt, '' AS poster FROM owned JOIN images r ON r.crawl_id = owned.upstream_crawl_id JOIN pagereports pr ON pr.id = r.pagereport_id AND pr.crawl_id = r.crawl_id UNION ALL SELECT r.id * 8 + 1, 'script', pr.url, r.url, '', '' FROM owned JOIN scripts r ON r.crawl_id = owned.upstream_crawl_id JOIN pagereports pr ON pr.id = r.pagereport_id AND pr.crawl_id = r.crawl_id UNION ALL SELECT r.id * 8 + 2, 'style', pr.url, r.url, '', '' FROM owned JOIN styles r ON r.crawl_id = owned.upstream_crawl_id JOIN pagereports pr ON pr.id = r.pagereport_id AND pr.crawl_id = r.crawl_id UNION ALL SELECT r.id * 8 + 3, 'iframe', pr.url, r.url, '', '' FROM owned JOIN iframes r ON r.crawl_id = owned.upstream_crawl_id JOIN pagereports pr ON pr.id = r.pagereport_id AND pr.crawl_id = r.crawl_id UNION ALL SELECT r.id * 8 + 4, 'audio', pr.url, r.url, '', '' FROM owned JOIN audios r ON r.crawl_id = owned.upstream_crawl_id JOIN pagereports pr ON pr.id = r.pagereport_id AND pr.crawl_id = r.crawl_id UNION ALL SELECT r.id * 8 + 5, 'video', pr.url, r.url, '', COALESCE(r.poster, '') FROM owned JOIN videos r ON r.crawl_id = owned.upstream_crawl_id JOIN pagereports pr ON pr.id = r.pagereport_id AND pr.crawl_id = r.crawl_id) result WHERE result.cursor_id > ? ORDER BY result.cursor_id LIMIT ?`

type APIFindingRepository struct {
	DB *sql.DB
}

func (r APIFindingRepository) ListIssues(ctx context.Context, principal api.Principal, projectID, crawlID string, page api.PageRequest) (api.PageResult[api.IssueFinding], error) {
	if !r.validPrincipal(principal, projectID) {
		return api.PageResult[api.IssueFinding]{}, api.ErrCrawlNotFound
	}
	args := append(r.ownerArgs(principal, projectID, crawlID), page.AfterID, queryLimit(page.Limit))
	rows, err := r.DB.QueryContext(ctx, listAPIIssuesSQL, args...)
	if err != nil {
		return api.PageResult[api.IssueFinding]{}, err
	}
	defer rows.Close()
	type row struct {
		id      int64
		finding api.IssueFinding
	}
	items := make([]row, 0, page.Limit+1)
	for rows.Next() {
		var item row
		var priority, statusCode int
		var redirectURL, canonical string
		if err := rows.Scan(&item.id, &item.finding.Code, &priority, &item.finding.PageURL, &statusCode, &redirectURL, &canonical); err != nil {
			return api.PageResult[api.IssueFinding]{}, err
		}
		item.finding.Severity = issueSeverity(priority)
		item.finding.Count = 1
		item.finding.Evidence = map[string]any{"status_code": statusCode}
		if redirectURL != "" {
			item.finding.Evidence["redirect_url"] = redirectURL
		}
		if canonical != "" {
			item.finding.Evidence["canonical"] = canonical
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return api.PageResult[api.IssueFinding]{}, err
	}
	if len(items) == 0 {
		if _, err := r.authorizeCrawl(ctx, principal, projectID, crawlID); err != nil {
			return api.PageResult[api.IssueFinding]{}, err
		}
	}
	result := api.PageResult[api.IssueFinding]{Items: make([]api.IssueFinding, 0, min(len(items), page.Limit))}
	for _, item := range items[:min(len(items), page.Limit)] {
		result.Items = append(result.Items, item.finding)
	}
	if len(items) > page.Limit {
		result.NextAfterID = items[page.Limit-1].id
	}
	return result, nil
}

func (r APIFindingRepository) ListPages(ctx context.Context, principal api.Principal, projectID, crawlID string, page api.PageRequest) (api.PageResult[api.PageFinding], error) {
	if !r.validPrincipal(principal, projectID) {
		return api.PageResult[api.PageFinding]{}, api.ErrCrawlNotFound
	}
	args := append(r.ownerArgs(principal, projectID, crawlID), page.AfterID, queryLimit(page.Limit))
	rows, err := r.DB.QueryContext(ctx, listAPIPagesSQL, args...)
	if err != nil {
		return api.PageResult[api.PageFinding]{}, err
	}
	defer rows.Close()
	type row struct {
		id   int64
		page api.PageFinding
	}
	items := make([]row, 0, page.Limit+1)
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.id, &item.page.URL, &item.page.RedirectURL, &item.page.StatusCode, &item.page.ContentType, &item.page.Language, &item.page.Title, &item.page.Description, &item.page.Robots, &item.page.Canonical, &item.page.Heading1, &item.page.Heading2, &item.page.Words, &item.page.SizeBytes, &item.page.Depth, &item.page.TTFBMillis, &item.page.Crawled, &item.page.InSitemap, &item.page.NoIndex, &item.page.NoFollow, &item.page.SitemapEligible); err != nil {
			return api.PageResult[api.PageFinding]{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return api.PageResult[api.PageFinding]{}, err
	}
	if len(items) == 0 {
		if _, err := r.authorizeCrawl(ctx, principal, projectID, crawlID); err != nil {
			return api.PageResult[api.PageFinding]{}, err
		}
	}
	result := api.PageResult[api.PageFinding]{Items: make([]api.PageFinding, 0, min(len(items), page.Limit))}
	for _, item := range items[:min(len(items), page.Limit)] {
		result.Items = append(result.Items, item.page)
	}
	if len(items) > page.Limit {
		result.NextAfterID = items[page.Limit-1].id
	}
	return result, nil
}

func (r APIFindingRepository) ListLinks(ctx context.Context, principal api.Principal, projectID, crawlID string, page api.PageRequest) (api.PageResult[api.LinkFinding], error) {
	if !r.validPrincipal(principal, projectID) {
		return api.PageResult[api.LinkFinding]{}, api.ErrCrawlNotFound
	}
	args := append(r.ownerArgs(principal, projectID, crawlID), page.AfterID, queryLimit(page.Limit))
	rows, err := r.DB.QueryContext(ctx, listAPILinksSQL, args...)
	if err != nil {
		return api.PageResult[api.LinkFinding]{}, err
	}
	defer rows.Close()
	type row struct {
		id   int64
		link api.LinkFinding
	}
	items := make([]row, 0, page.Limit+1)
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.id, &item.link.Kind, &item.link.OriginURL, &item.link.DestinationURL, &item.link.Text, &item.link.Rel, &item.link.NoFollow); err != nil {
			return api.PageResult[api.LinkFinding]{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return api.PageResult[api.LinkFinding]{}, err
	}
	if len(items) == 0 {
		if _, err := r.authorizeCrawl(ctx, principal, projectID, crawlID); err != nil {
			return api.PageResult[api.LinkFinding]{}, err
		}
	}
	result := api.PageResult[api.LinkFinding]{Items: make([]api.LinkFinding, 0, min(len(items), page.Limit))}
	for _, item := range items[:min(len(items), page.Limit)] {
		result.Items = append(result.Items, item.link)
	}
	if len(items) > page.Limit {
		result.NextAfterID = items[page.Limit-1].id
	}
	return result, nil
}

func (r APIFindingRepository) ListResources(ctx context.Context, principal api.Principal, projectID, crawlID, resourceType string, page api.PageRequest) (api.PageResult[api.ResourceFinding], error) {
	if !r.validPrincipal(principal, projectID) {
		return api.PageResult[api.ResourceFinding]{}, api.ErrCrawlNotFound
	}
	args := append(r.ownerArgs(principal, projectID, crawlID), page.AfterID, queryLimit(page.Limit))
	var rows *sql.Rows
	var err error
	if resourceType == "" {
		rows, err = r.DB.QueryContext(ctx, listAllAPIResourcesSQL, args...)
	} else {
		query, ok := resourceQuery(resourceType)
		if !ok {
			return api.PageResult[api.ResourceFinding]{}, errors.New("invalid resource type")
		}
		rows, err = r.DB.QueryContext(ctx, query, args...)
	}
	if err != nil {
		return api.PageResult[api.ResourceFinding]{}, err
	}
	defer rows.Close()
	type row struct {
		id       int64
		resource api.ResourceFinding
	}
	items := make([]row, 0, page.Limit+1)
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.id, &item.resource.Type, &item.resource.OriginURL, &item.resource.URL, &item.resource.Alt, &item.resource.Poster); err != nil {
			return api.PageResult[api.ResourceFinding]{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return api.PageResult[api.ResourceFinding]{}, err
	}
	if len(items) == 0 {
		if _, err := r.authorizeCrawl(ctx, principal, projectID, crawlID); err != nil {
			return api.PageResult[api.ResourceFinding]{}, err
		}
	}
	result := api.PageResult[api.ResourceFinding]{Items: make([]api.ResourceFinding, 0, min(len(items), page.Limit))}
	for _, item := range items[:min(len(items), page.Limit)] {
		result.Items = append(result.Items, item.resource)
	}
	if len(items) > page.Limit {
		result.NextAfterID = items[page.Limit-1].id
	}
	return result, nil
}

func (r APIFindingRepository) authorizeCrawl(ctx context.Context, principal api.Principal, projectID, crawlID string) (int64, error) {
	if !r.validPrincipal(principal, projectID) {
		return 0, api.ErrCrawlNotFound
	}
	var upstreamID int64
	err := r.DB.QueryRowContext(ctx, authorizeAPICrawlResultsSQL, principal.TenantID, projectID, crawlID, principal.ProjectID, principal.ProjectID).Scan(&upstreamID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, api.ErrCrawlNotFound
	}
	return upstreamID, err
}

func (r APIFindingRepository) validPrincipal(principal api.Principal, projectID string) bool {
	return r.DB != nil && principal.TenantID != "" && (principal.ProjectID == "" || principal.ProjectID == projectID)
}

func (r APIFindingRepository) ownerArgs(principal api.Principal, projectID, crawlID string) []any {
	return []any{principal.TenantID, projectID, crawlID, principal.ProjectID, principal.ProjectID}
}

func resourceQuery(resourceType string) (string, bool) {
	var table, alt, poster string
	switch resourceType {
	case "image":
		table, alt, poster = "images", "COALESCE(r.alt, '')", "''"
	case "script":
		table, alt, poster = "scripts", "''", "''"
	case "style":
		table, alt, poster = "styles", "''", "''"
	case "iframe":
		table, alt, poster = "iframes", "''", "''"
	case "audio":
		table, alt, poster = "audios", "''", "''"
	case "video":
		table, alt, poster = "videos", "''", "COALESCE(r.poster, '')"
	default:
		return "", false
	}
	return fmt.Sprintf("%s SELECT r.id, '%s', pr.url, r.url, %s, %s FROM owned JOIN %s r ON r.crawl_id = owned.upstream_crawl_id JOIN pagereports pr ON pr.id = r.pagereport_id AND pr.crawl_id = r.crawl_id WHERE r.id > ? ORDER BY r.id LIMIT ?", ownedAPICrawlCTE, resourceType, alt, poster, table), true
}

func queryLimit(limit int) int {
	if limit < 1 {
		limit = 100
	}
	return limit + 1
}

func issueSeverity(priority int) string {
	switch priority {
	case 1:
		return "critical"
	case 2:
		return "alert"
	default:
		return "warning"
	}
}
