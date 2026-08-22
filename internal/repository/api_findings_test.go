package repository

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stjudewashere/seonaut/internal/api"
)

func TestAPIFindingsAuthorizeOwnerBeforeReturningCursorPage(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(listAPIIssuesSQL)).WithArgs("tenant-a", "project-a", "crawl-a", "project-a", "project-a", int64(0), 2).WillReturnRows(sqlmock.NewRows([]string{"id", "code", "priority", "url", "status_code", "redirect_url", "canonical"}).
		AddRow(7, "ERROR_EMPTY_TITLE", 2, "https://a.example/one", 200, "", "").
		AddRow(8, "ERROR_40x", 1, "https://a.example/missing", 404, "", ""))

	result, err := (APIFindingRepository{DB: db}).ListIssues(context.Background(), api.Principal{TenantID: "tenant-a", ProjectID: "project-a"}, "project-a", "crawl-a", api.PageRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Code != "ERROR_EMPTY_TITLE" || result.Items[0].Severity != "alert" || result.Items[0].Count != 1 || result.NextAfterID != 7 {
		t.Fatalf("result=%+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAPIFindingsReturnNotFoundWhenOwnerJoinedResultIsEmpty(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(listAPIPagesSQL)).WithArgs("tenant-a", "project-b", "crawl-b", "", "", int64(0), 101).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(regexp.QuoteMeta(authorizeAPICrawlResultsSQL)).WithArgs("tenant-a", "project-b", "crawl-b", "", "").WillReturnError(sql.ErrNoRows)

	_, err = (APIFindingRepository{DB: db}).ListPages(context.Background(), api.Principal{TenantID: "tenant-a"}, "project-b", "crawl-b", api.PageRequest{Limit: 100})
	if !errors.Is(err, api.ErrCrawlNotFound) {
		t.Fatalf("err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAPIPagesProjectNoFollowFromRobots(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	columns := []string{"id", "url", "redirect_url", "status_code", "content_type", "lang", "title", "description", "robots", "canonical", "h1", "h2", "words", "size", "depth", "ttfb", "crawled", "in_sitemap", "noindex", "nofollow", "sitemap_eligible"}
	mock.ExpectQuery(regexp.QuoteMeta(listAPIPagesSQL)).WithArgs("tenant-a", "project-a", "crawl-a", "", "", int64(0), 2).WillReturnRows(sqlmock.NewRows(columns).AddRow(1, "https://a.example", "", 200, "text/html", "en", "A", "", "index,nofollow", "", "", "", 1, 10, 0, 1, true, false, false, true, true))
	result, err := (APIFindingRepository{DB: db}).ListPages(context.Background(), api.Principal{TenantID: "tenant-a"}, "project-a", "crawl-a", api.PageRequest{Limit: 1})
	if err != nil || len(result.Items) != 1 || !result.Items[0].NoFollow {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
