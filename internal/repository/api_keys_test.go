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

func TestAPIKeyRepositoryFindsCompleteCredentialRecord(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	created := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	expires := created.Add(time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta(findAPIKeySQL)).WithArgs("platform01").WillReturnRows(
		sqlmock.NewRows([]string{"public_id", "secret_hash", "key_kind", "tenant_id", "project_id", "scopes", "created_at", "expires_at", "revoked_at", "rotated_from_public_id"}).
			AddRow("platform01", "argon-hash", "platform", "", "", `["tenants:provision","meta:read"]`, created, expires, nil, ""),
	)
	repo := APIKeyRepository{DB: db}

	key, err := repo.FindAPIKey(context.Background(), "platform01")
	if err != nil {
		t.Fatal(err)
	}
	if key.PublicID != "platform01" || key.Kind != api.KeyPlatform || key.SecretHash != "argon-hash" || key.ExpiresAt == nil || len(key.Scopes) != 2 || key.Scopes[0] != api.ScopeTenantsProvision {
		t.Fatalf("key = %#v", key)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAPIKeyRepositoryMapsMissingKeyToPublicNotFoundError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(findAPIKeySQL)).WithArgs("missing").WillReturnRows(
		sqlmock.NewRows([]string{"public_id", "secret_hash", "key_kind", "tenant_id", "project_id", "scopes", "created_at", "expires_at", "revoked_at", "rotated_from_public_id"}),
	)
	repo := APIKeyRepository{DB: db}

	_, err = repo.FindAPIKey(context.Background(), "missing")
	if !errors.Is(err, api.ErrKeyNotFound) {
		t.Fatalf("error = %v, want ErrKeyNotFound", err)
	}
}

func TestAPIKeyRepositoryRotatesAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	created := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	overlap := created.Add(5 * time.Minute)
	replacement := api.StoredKey{PublicID: "new", SecretHash: "hash", Kind: api.KeyPlatform, Scopes: []string{api.ScopeMetaRead}, CreatedAt: created, RotatedFrom: "old"}
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(expireAPIKeySQL)).WithArgs(overlap, "old", api.KeyPlatform).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(createAPIKeySQL)).WithArgs("new", "hash", api.KeyPlatform, nil, nil, `["meta:read"]`, created, nil, "old").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	repo := APIKeyRepository{DB: db}

	if err := repo.RotateAPIKey(context.Background(), "old", replacement, overlap); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
