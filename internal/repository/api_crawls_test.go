package repository

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/stjudewashere/seonaut/internal/api"
)

var apiCrawlColumns = []string{"id", "tenant_id", "project_id", "state", "cancel_requested", "queued_at", "started_at", "finished_at", "failure_code", "failure_message", "total_urls", "total_issues"}

func TestListAPICrawlsAppliesTenantProjectAndKeyBinding(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Unix(1, 0).UTC()
	mock.ExpectQuery(regexp.QuoteMeta(listBoundAPICrawlsSQL)).WithArgs("tenant-a", "project-a", "project-a").WillReturnRows(sqlmock.NewRows(apiCrawlColumns).AddRow("crawl-a", "tenant-a", "project-a", api.CrawlRunning, false, now, now, nil, "", "", 0, 0))
	items, err := (APICrawlRepository{DB: db}).ListCrawls(context.Background(), api.Principal{TenantID: "tenant-a", ProjectID: "project-a"}, "project-a")
	if err != nil || len(items) != 1 || items[0].ID != "crawl-a" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReserveAPICrawlRejectsASecondActiveCrawl(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(findIdempotencySQL)).WithArgs("key-a", operationStartCrawl, "idem-b").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta(findAPIProjectUpstreamIDSQL)).WithArgs("tenant-a", "project-a").WillReturnRows(sqlmock.NewRows([]string{"upstream_project_id"}).AddRow(7))
	mock.ExpectExec(regexp.QuoteMeta(insertAPICrawlSQL)).WithArgs(sqlmock.AnyArg(), "tenant-a", "project-a", int64(7), api.CrawlQueued, 1, sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnError(&mysql.MySQLError{Number: 1062, Message: "duplicate active slot"})
	mock.ExpectRollback()
	_, _, err = (APICrawlRepository{DB: db}).ReserveCrawl(context.Background(), api.Principal{KeyID: "key-a", TenantID: "tenant-a"}, "project-a", "idem-b", "hash")
	if !errors.Is(err, api.ErrCrawlAlreadyActive) {
		t.Fatalf("err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteAPICrawlDoesNotRegressATerminalState(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	finished := time.Unix(2, 0).UTC()
	mock.ExpectExec(regexp.QuoteMeta(completeAPICrawlSQL)).WithArgs(api.CrawlSucceeded, nil, nil, nil, 3, 1, finished, finished, "tenant-a", "crawl-a").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(findAPICrawlStateSQL)).WithArgs("tenant-a", "crawl-a").WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(api.CrawlCanceled))
	err = (APICrawlRepository{DB: db}).CompleteCrawl(context.Background(), "tenant-a", "crawl-a", api.CrawlCompletion{State: api.CrawlSucceeded, FinishedAt: finished, TotalURLs: 3, TotalIssues: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
