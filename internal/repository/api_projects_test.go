package repository

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stjudewashere/seonaut/internal/api"
	"github.com/stjudewashere/seonaut/internal/models"
)

var apiProjectColumns = []string{
	"id", "external_project_id", "tenant_id", "url", "ignore_robotstxt", "follow_nofollow", "include_noindex",
	"crawl_sitemap", "allow_subdomains", "basic_auth", "check_external_links", "archive", "user_agent", "created_at", "updated_at",
}

func TestListAPIProjectsAppliesTenantAndOptionalProjectPredicates(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Unix(1, 0).UTC()
	mock.ExpectQuery(regexp.QuoteMeta(listBoundAPIProjectsSQL)).
		WithArgs("tenant-a", "project-a").
		WillReturnRows(sqlmock.NewRows(apiProjectColumns).AddRow("project-a", "site-a", "tenant-a", "https://a.example", false, false, false, false, false, false, false, false, "SEOnaut", now, now))

	projects, err := (APIProjectRepository{DB: db}).ListProjects(context.Background(), api.Principal{TenantID: "tenant-a", ProjectID: "project-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].TenantID != "tenant-a" || projects[0].ID != "project-a" {
		t.Fatalf("projects=%+v", projects)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetAPIProjectReturnsNotFoundThroughOwnerPredicate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(getAPIProjectSQL)).
		WithArgs("tenant-a", "project-b").
		WillReturnRows(sqlmock.NewRows(apiProjectColumns))

	_, err = (APIProjectRepository{DB: db}).GetProject(context.Background(), api.Principal{TenantID: "tenant-a"}, "project-b")
	if !errors.Is(err, api.ErrProjectNotFound) {
		t.Fatalf("err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPutAPIProjectReplaysSameHashWithoutCreatingData(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Unix(1, 0).UTC()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(findIdempotencySQL)).
		WithArgs("key-a", operationPutProject, "idem-a").
		WillReturnRows(sqlmock.NewRows([]string{"request_hash", "resource_id"}).AddRow("same", "project-a"))
	mock.ExpectQuery(regexp.QuoteMeta(getAPIProjectSQL)).
		WithArgs("tenant-a", "project-a").
		WillReturnRows(sqlmock.NewRows(apiProjectColumns).AddRow("project-a", "site-a", "tenant-a", "https://a.example", false, false, false, false, false, false, false, false, "SEOnaut", now, now))
	mock.ExpectCommit()

	project, replayed, err := (APIProjectRepository{DB: db}).PutProject(context.Background(), api.Principal{KeyID: "key-a", TenantID: "tenant-a"}, "site-a", "idem-a", "same", models.Project{URL: "https://a.example", UserAgent: "SEOnaut"})
	if err != nil {
		t.Fatal(err)
	}
	if !replayed || project.ID != "project-a" {
		t.Fatalf("project=%+v replayed=%v", project, replayed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPutAPIProjectRejectsConflictingReplay(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(findIdempotencySQL)).
		WithArgs("key-a", operationPutProject, "idem-a").
		WillReturnRows(sqlmock.NewRows([]string{"request_hash", "resource_id"}).AddRow("old", "project-a"))
	mock.ExpectRollback()

	_, _, err = (APIProjectRepository{DB: db}).PutProject(context.Background(), api.Principal{KeyID: "key-a", TenantID: "tenant-a"}, "site-a", "idem-a", "new", models.Project{URL: "https://a.example", UserAgent: "SEOnaut"})
	if !errors.Is(err, api.ErrIdempotencyConflict) {
		t.Fatalf("err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPutAPIProjectCreatesUpstreamProjectAndOpaqueBindingAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	project := models.Project{URL: "https://a.example", UserAgent: "SEOnaut", CrawlSitemap: true}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(findIdempotencySQL)).
		WithArgs("key-a", operationPutProject, "idem-a").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta(findAPITenantOwnerSQL)).
		WithArgs("tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"upstream_user_id"}).AddRow(7))
	mock.ExpectQuery(regexp.QuoteMeta(findAPIProjectByExternalSQL)).
		WithArgs("tenant-a", "site-a").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(regexp.QuoteMeta(insertUpstreamAPIProjectSQL)).
		WithArgs(project.URL, project.IgnoreRobotsTxt, project.FollowNofollow, project.IncludeNoindex, project.CrawlSitemap, project.AllowSubdomains, project.BasicAuth, 7, project.CheckExternalLinks, project.Archive, project.UserAgent).
		WillReturnResult(sqlmock.NewResult(17, 1))
	mock.ExpectExec(regexp.QuoteMeta(insertAPIProjectBindingSQL)).
		WithArgs(sqlmock.AnyArg(), "tenant-a", "site-a", int64(17), 7, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta(insertIdempotencySQL)).
		WithArgs("key-a", "tenant-a", operationPutProject, "idem-a", "hash", "project", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	created, replayed, err := (APIProjectRepository{DB: db, Now: func() time.Time { return time.Unix(2, 0).UTC() }}).PutProject(context.Background(), api.Principal{KeyID: "key-a", TenantID: "tenant-a"}, "site-a", "idem-a", "hash", project)
	if err != nil {
		t.Fatal(err)
	}
	if replayed || created.ID == "" || created.ExternalID != "site-a" || created.URL != project.URL {
		t.Fatalf("created=%+v replayed=%v", created, replayed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
