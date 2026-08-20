package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stjudewashere/seonaut/internal/api"
)

var apiExportJobColumns = []string{"id", "tenant_id", "project_id", "crawl_id", "kind", "state", "artifact_path", "artifact_size", "failure_code", "created_at", "updated_at", "expires_at", "finished_at"}

func TestAPIExportRepositoryResolvesOwnedSourceAndDurableArchiveJob(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 20, 20, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta(resolveAPIExportSourceSQL)).WithArgs("tenant-a", "project-a", "crawl-a", "project-a", "project-a").WillReturnRows(sqlmock.NewRows([]string{"upstream_crawl_id", "upstream_project_id", "url", "state"}).AddRow(9, 4, "https://a.example", api.CrawlSucceeded))

	repository := APIExportRepository{DB: db, Now: func() time.Time { return now }}
	source, err := repository.ResolveSource(context.Background(), api.Principal{TenantID: "tenant-a", ProjectID: "project-a"}, "project-a", "crawl-a")
	if err != nil || source.UpstreamCrawlID != 9 || source.ProjectURL != "https://a.example" || source.State != api.CrawlSucceeded {
		t.Fatalf("source=%+v err=%v", source, err)
	}

	expires := now.Add(7 * 24 * time.Hour)
	mock.ExpectExec(regexp.QuoteMeta(reserveAPIArchiveJobSQL)).WithArgs(sqlmock.AnyArg(), "tenant-a", "project-a", "crawl-a", now, now, expires).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(regexp.QuoteMeta(getAPIArchiveJobSQL)).WithArgs("tenant-a", "project-a", "crawl-a").WillReturnRows(sqlmock.NewRows(apiExportJobColumns).AddRow("job-a", "tenant-a", "project-a", "crawl-a", "archive.wacz", api.ExportPending, "", 0, "", now, now, expires, nil))
	job, err := repository.ReserveArchive(context.Background(), api.Principal{TenantID: "tenant-a"}, "project-a", "crawl-a")
	if err != nil || job.ID != "job-a" || job.State != api.ExportPending || !job.ExpiresAt.Equal(expires) {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	mock.ExpectExec(regexp.QuoteMeta(completeAPIArchiveJobSQL)).WithArgs("archive/api/crawl-a/a.wacz", int64(4), now, now, "tenant-a", "project-a", "crawl-a").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repository.CompleteArchive(context.Background(), api.Principal{TenantID: "tenant-a"}, "project-a", "crawl-a", "archive/api/crawl-a/a.wacz", 4); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAPIExportRepositoryListsAndDeletesExpiredArchives(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before := time.Date(2026, 8, 20, 20, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta(listExpiredAPIArchiveJobsSQL)).WithArgs(before).WillReturnRows(sqlmock.NewRows(apiExportJobColumns).AddRow("job-a", "tenant-a", "project-a", "crawl-a", "archive.wacz", api.ExportReady, "archive/api/a.wacz", 4, "", before.Add(-8*24*time.Hour), before.Add(-8*24*time.Hour), before.Add(-time.Hour), before.Add(-time.Hour)))
	repository := APIExportRepository{DB: db}
	jobs, err := repository.ListExpiredArchives(context.Background(), before)
	if err != nil || len(jobs) != 1 || jobs[0].ID != "job-a" {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	mock.ExpectExec(regexp.QuoteMeta(deleteExpiredAPIArchiveJobSQL)).WithArgs("job-a", before).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repository.DeleteExpiredArchive(context.Background(), "job-a", before); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
