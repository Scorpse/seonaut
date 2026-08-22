package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stjudewashere/seonaut/internal/api"
)

func TestAPIAuditRepositoryStoresOnlyRedactedMetadata(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Unix(100, 0).UTC()
	mock.ExpectExec(regexp.QuoteMeta(insertAPIAuditSQL)).
		WithArgs("req-a", "key-a", "tenant-a", "project-a", "crawl-a", "crawls.read", "success", 200, "192.0.2.1", []byte(`{"key_kind":"tenant","method":"GET"}`), now).
		WillReturnResult(sqlmock.NewResult(1, 1))
	repository := APIAuditRepository{DB: db}
	if err := repository.RecordAudit(context.Background(), api.AuditRecord{
		RequestID: "req-a", KeyPublicID: "key-a", TenantID: "tenant-a", ProjectID: "project-a", CrawlID: "crawl-a",
		Action: "crawls.read", Outcome: "success", HTTPStatus: 200, SourceIP: "192.0.2.1",
		Metadata: map[string]string{"method": "GET", "key_kind": "tenant"}, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
