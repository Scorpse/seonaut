package repository_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stjudewashere/seonaut/internal/api"
	"github.com/stjudewashere/seonaut/internal/models"
	"github.com/stjudewashere/seonaut/internal/repository"
	"github.com/stjudewashere/seonaut/internal/services"
)

type identityExportTranslator struct{}

func (identityExportTranslator) Trans(_ string, value string, _ ...interface{}) string { return value }

func TestAPIFindingsMySQLControlledFixtureMatchesUpstreamExports(t *testing.T) {
	dsn := os.Getenv("SEONAUT_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("SEONAUT_TEST_MYSQL_DSN is not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 21, 0, 0, 0, time.UTC)
	tenantID := "00000000-0000-4000-8000-00000000000c"
	cleanup := func() {
		var staleUserID int64
		_ = db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = 'fixture-c@service.invalid' LIMIT 1`).Scan(&staleUserID)
		_, _ = db.ExecContext(ctx, `DELETE FROM api_export_jobs WHERE tenant_id = ?`, tenantID)
		_, _ = db.ExecContext(ctx, `DELETE FROM api_crawls WHERE tenant_id = ?`, tenantID)
		_, _ = db.ExecContext(ctx, `DELETE FROM api_idempotency WHERE tenant_id = ?`, tenantID)
		_, _ = db.ExecContext(ctx, `DELETE FROM api_keys WHERE tenant_id = ?`, tenantID)
		_, _ = db.ExecContext(ctx, `DELETE FROM api_projects WHERE tenant_id = ?`, tenantID)
		_, _ = db.ExecContext(ctx, `DELETE FROM api_tenants WHERE id = ?`, tenantID)
		if staleUserID != 0 {
			_, _ = db.ExecContext(ctx, `DELETE FROM projects WHERE user_id = ?`, staleUserID)
			_, _ = db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, staleUserID)
		}
	}
	cleanup()
	defer db.Close()
	defer cleanup()

	userResult, err := db.ExecContext(ctx, `INSERT INTO users (email, password, lang, theme, api_only) VALUES ('fixture-c@service.invalid', 'unusable', 'en', 'light', 1)`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := userResult.LastInsertId()
	if _, err := db.ExecContext(ctx, `INSERT INTO api_tenants (id, external_tenant_id, upstream_user_id, service_email, state, created_at, updated_at) VALUES (?, 'fixture-c', ?, 'fixture-c@service.invalid', 'active', ?, ?)`, tenantID, userID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO api_keys (public_id, secret_hash, key_kind, tenant_id, scopes, created_at) VALUES ('fixture-key-c', 'unused', 'tenant', ?, '["projects:read","projects:write"]', ?)`, tenantID, now); err != nil {
		t.Fatal(err)
	}
	principal := api.Principal{KeyID: "fixture-key-c", TenantID: tenantID}
	projectRepository := repository.APIProjectRepository{DB: db, Now: func() time.Time { return now }}
	project, _, err := projectRepository.PutProject(ctx, principal, "fixture-site", "fixture-project", "fixture-project-hash", models.Project{URL: "https://fixture.example", UserAgent: "SEOnaut"})
	if err != nil {
		t.Fatal(err)
	}
	crawlRepository := repository.APICrawlRepository{DB: db, Now: func() time.Time { return now }}
	crawl, _, err := crawlRepository.ReserveCrawl(ctx, principal, project.ID, "fixture-crawl", "fixture-crawl-hash")
	if err != nil {
		t.Fatal(err)
	}
	var upstreamProjectID int64
	if err := db.QueryRowContext(ctx, `SELECT upstream_project_id FROM api_projects WHERE id = ?`, project.ID).Scan(&upstreamProjectID); err != nil {
		t.Fatal(err)
	}
	upstreamCrawlResult, err := db.ExecContext(ctx, `INSERT INTO crawls (project_id) VALUES (?)`, upstreamProjectID)
	if err != nil {
		t.Fatal(err)
	}
	upstreamCrawlID, _ := upstreamCrawlResult.LastInsertId()
	if err := crawlRepository.MarkCrawlRunning(ctx, principal, crawl.ID, upstreamCrawlID, now); err != nil {
		t.Fatal(err)
	}
	pageOneResult, err := db.ExecContext(ctx, `INSERT INTO pagereports (crawl_id, url, status_code, content_type, media_type, lang, title, words, size, url_hash, body_hash, crawled, in_sitemap, ttfb) VALUES (?, 'https://fixture.example/one', 200, 'text/html', 'text/html', 'en', '', 7, 1024, 'fixture-one', 'fixture-body-one', 1, 1, 12)`, upstreamCrawlID)
	if err != nil {
		t.Fatal(err)
	}
	pageOneID, _ := pageOneResult.LastInsertId()
	pageTwoResult, err := db.ExecContext(ctx, `INSERT INTO pagereports (crawl_id, url, status_code, content_type, media_type, url_hash, body_hash, crawled) VALUES (?, 'https://fixture.example/missing', 404, 'text/html', 'text/html', 'fixture-missing', 'fixture-body-missing', 1)`, upstreamCrawlID)
	if err != nil {
		t.Fatal(err)
	}
	pageTwoID, _ := pageTwoResult.LastInsertId()
	if _, err := db.ExecContext(ctx, `UPDATE pagereports SET redirect_url = '', refresh = '', lang = COALESCE(lang, ''), title = COALESCE(title, ''), description = '', robots = '', canonical = '', h1 = '', h2 = '', words = COALESCE(words, 0), size = COALESCE(size, 0) WHERE crawl_id = ?`, upstreamCrawlID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO issues (pagereport_id, crawl_id, issue_type_id) VALUES (?, ?, 6)`, pageOneID, upstreamCrawlID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO links (pagereport_id, crawl_id, url, scheme, text, url_hash) VALUES (?, ?, 'https://fixture.example/missing', 'https', 'Missing', 'fixture-missing')`, pageOneID, upstreamCrawlID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO external_links (pagereport_id, crawl_id, url, text) VALUES (?, ?, 'https://outside.example', 'Outside')`, pageOneID, upstreamCrawlID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO images (pagereport_id, crawl_id, url, alt) VALUES (?, ?, 'https://fixture.example/logo.png', 'Logo')`, pageOneID, upstreamCrawlID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO scripts (pagereport_id, crawl_id, url) VALUES (?, ?, 'https://fixture.example/app.js')`, pageOneID, upstreamCrawlID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO videos (pagereport_id, crawl_id, url, poster) VALUES (?, ?, 'https://fixture.example/clip.mp4', 'https://fixture.example/poster.jpg')`, pageOneID, upstreamCrawlID); err != nil {
		t.Fatal(err)
	}
	if err := crawlRepository.CompleteCrawl(ctx, tenantID, crawl.ID, api.CrawlCompletion{State: api.CrawlSucceeded, FinishedAt: now, TotalURLs: 2, TotalIssues: 1}); err != nil {
		t.Fatal(err)
	}

	findings := repository.APIFindingRepository{DB: db}
	issues, err := findings.ListIssues(ctx, principal, project.ID, crawl.ID, api.PageRequest{Limit: 100})
	if err != nil || len(issues.Items) != 1 || issues.Items[0].Code != "ERROR_EMPTY_TITLE" || issues.Items[0].PageURL != "https://fixture.example/one" {
		t.Fatalf("issues=%+v err=%v", issues, err)
	}
	pages, err := findings.ListPages(ctx, principal, project.ID, crawl.ID, api.PageRequest{Limit: 100})
	if err != nil || len(pages.Items) != 2 || pages.Items[0].StatusCode != 200 || pages.Items[1].StatusCode != 404 {
		t.Fatalf("pages=%+v err=%v", pages, err)
	}
	links, err := findings.ListLinks(ctx, principal, project.ID, crawl.ID, api.PageRequest{Limit: 100})
	if err != nil || len(links.Items) != 2 || links.Items[0].Kind != "internal" || links.Items[1].Kind != "external" {
		t.Fatalf("links=%+v err=%v", links, err)
	}
	images, err := findings.ListResources(ctx, principal, project.ID, crawl.ID, "image", api.PageRequest{Limit: 100})
	if err != nil || len(images.Items) != 1 || images.Items[0].Alt != "Logo" {
		t.Fatalf("images=%+v err=%v", images, err)
	}
	allResources, err := findings.ListResources(ctx, principal, project.ID, crawl.ID, "", api.PageRequest{Limit: 2})
	if err != nil || len(allResources.Items) != 2 || allResources.NextAfterID == 0 {
		t.Fatalf("all resources first page=%+v err=%v", allResources, err)
	}
	remainingResources, err := findings.ListResources(ctx, principal, project.ID, crawl.ID, "", api.PageRequest{AfterID: allResources.NextAfterID, Limit: 2})
	if err != nil || len(remainingResources.Items) != 1 {
		t.Fatalf("all resources second page=%+v err=%v", remainingResources, err)
	}
	resourceTypes := map[string]bool{}
	for _, resource := range append(allResources.Items, remainingResources.Items...) {
		resourceTypes[resource.Type] = true
	}
	if !resourceTypes["image"] || !resourceTypes["script"] || !resourceTypes["video"] {
		t.Fatalf("resource types=%v", resourceTypes)
	}

	upstreamExporter := services.NewExporter(&repository.ExportRepository{DB: db}, identityExportTranslator{})
	exportManager := services.APIExportManager{Store: &repository.APIExportRepository{DB: db}, Exporter: upstreamExporter, Reports: &repository.PageReportRepository{DB: db}}
	for _, check := range []struct {
		kind   api.ExportKind
		header []string
	}{
		{kind: api.ExportIssuesCSV, header: []string{"URL", "Issue Type", "Priority"}},
		{kind: api.ExportPagesCSV, header: []string{"Status Code", "URL", "Redirect URL", "Content Type", "Canonical", "Lang", "Title", "Title Length", "Description", "Description Length", "Robots", "Header 1", "Header 2", "Size", "Nº of words", "Depth", "TTFB"}},
		{kind: api.ExportResourcesCSV, header: []string{"Type", "Origin", "URL", "Alt", "Poster"}},
	} {
		prepared, err := exportManager.PrepareExport(ctx, principal, project.ID, crawl.ID, check.kind)
		if err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		if err := prepared.WriteTo(&output); err != nil {
			t.Fatal(err)
		}
		rows, err := csv.NewReader(strings.NewReader(output.String())).ReadAll()
		if err != nil || len(rows) < 2 || strings.Join(rows[0], "|") != strings.Join(check.header, "|") {
			t.Fatalf("%s rows=%+v err=%v", check.kind, rows, err)
		}
	}
	if pageTwoID <= pageOneID {
		t.Fatal("fixture page order is not deterministic")
	}
}
