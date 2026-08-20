package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stjudewashere/seonaut/internal/api"
)

func TestAPITenantRepositoryGetsBindingByExternalID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	created := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta(findAPITenantSQL)).WithArgs("acme").WillReturnRows(
		sqlmock.NewRows([]string{"id", "external_tenant_id", "state", "service_email", "created_at"}).AddRow("tenant-a", "acme", "active", "tenant-acme@service.invalid", created),
	)
	repo := APITenantRepository{DB: db}

	binding, err := repo.GetTenant(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if binding.ID != "tenant-a" || binding.ExternalID != "acme" || binding.ServiceEmail != "tenant-acme@service.invalid" {
		t.Fatalf("binding=%#v", binding)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAPITenantRepositoryMapsMissingBindingToPublicNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(findAPITenantSQL)).WithArgs("missing").WillReturnRows(sqlmock.NewRows([]string{"id", "external_tenant_id", "state", "service_email", "created_at"}))
	repo := APITenantRepository{DB: db}
	_, err = repo.GetTenant(context.Background(), "missing")
	if !errors.Is(err, api.ErrTenantNotFound) {
		t.Fatalf("error=%v", err)
	}
}
