package repository

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stjudewashere/seonaut/internal/api"
	"github.com/stjudewashere/seonaut/internal/models"
)

func TestAPIProjectRepositoryMySQLTenantIsolationAndIdempotency(t *testing.T) {
	dsn := os.Getenv("SEONAUT_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("SEONAUT_TEST_MYSQL_DSN is not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

	for _, tenant := range []struct{ id, external, email, key string }{
		{id: "00000000-0000-4000-8000-00000000000a", external: "tenant-a", email: "tenant-a@service.invalid", key: "integration-key-a"},
		{id: "00000000-0000-4000-8000-00000000000b", external: "tenant-b", email: "tenant-b@service.invalid", key: "integration-key-b"},
	} {
		result, err := db.ExecContext(ctx, `INSERT INTO users (email, password, lang, theme, api_only) VALUES (?, 'unusable', 'en', 'light', 1)`, tenant.email)
		if err != nil {
			t.Fatal(err)
		}
		userID, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO api_tenants (id, external_tenant_id, upstream_user_id, service_email, state, created_at, updated_at) VALUES (?, ?, ?, ?, 'active', ?, ?)`, tenant.id, tenant.external, userID, tenant.email, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO api_keys (public_id, secret_hash, key_kind, tenant_id, scopes, created_at) VALUES (?, 'unused', 'tenant', ?, '["projects:read","projects:write"]', ?)`, tenant.key, tenant.id, now); err != nil {
			t.Fatal(err)
		}
	}

	repository := APIProjectRepository{DB: db, Now: func() time.Time { return now }}
	principalA := api.Principal{KeyID: "integration-key-a", TenantID: "00000000-0000-4000-8000-00000000000a"}
	principalB := api.Principal{KeyID: "integration-key-b", TenantID: "00000000-0000-4000-8000-00000000000b"}
	inputA := models.Project{URL: "https://a.example", UserAgent: "SEOnaut"}
	inputB := models.Project{URL: "https://b.example", UserAgent: "SEOnaut"}
	projectA, replayed, err := repository.PutProject(ctx, principalA, "site", "idem-a", "hash-a", inputA)
	if err != nil || replayed {
		t.Fatalf("create A project=%+v replayed=%v err=%v", projectA, replayed, err)
	}
	projectB, _, err := repository.PutProject(ctx, principalB, "site", "idem-b", "hash-b", inputB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO api_keys (public_id, secret_hash, key_kind, tenant_id, project_id, scopes, created_at) VALUES ('cross-tenant-project-key', 'unused', 'read_only', ?, ?, '["projects:read"]', ?)`, principalA.TenantID, projectB.ID, now); err == nil {
		t.Fatal("database accepted a project-bound key whose tenant does not own the project")
	}
	var upstreamProjectB, upstreamUserB int64
	if err := db.QueryRowContext(ctx, `SELECT upstream_project_id, upstream_user_id FROM api_projects WHERE id = ?`, projectB.ID).Scan(&upstreamProjectB, &upstreamUserB); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO api_projects (id, tenant_id, external_project_id, upstream_project_id, upstream_user_id, created_at, updated_at) VALUES ('00000000-0000-4000-8000-0000000000ff', ?, 'cross-tenant', ?, ?, ?, ?)`, principalA.TenantID, upstreamProjectB, upstreamUserB, now, now); err == nil {
		t.Fatal("database accepted a project binding whose upstream owner belongs to another tenant")
	}

	listA, err := repository.ListProjects(ctx, principalA)
	if err != nil {
		t.Fatal(err)
	}
	if len(listA) != 1 || listA[0].ID != projectA.ID {
		t.Fatalf("tenant A list=%+v", listA)
	}
	if _, err := repository.GetProject(ctx, principalA, projectB.ID); !errors.Is(err, api.ErrProjectNotFound) {
		t.Fatalf("foreign direct err=%v", err)
	}
	bound, err := repository.ListProjects(ctx, api.Principal{TenantID: principalA.TenantID, ProjectID: projectA.ID})
	if err != nil || len(bound) != 1 || bound[0].ID != projectA.ID {
		t.Fatalf("bound list=%+v err=%v", bound, err)
	}
	replay, replayed, err := repository.PutProject(ctx, principalA, "site", "idem-a", "hash-a", inputA)
	if err != nil || !replayed || replay.ID != projectA.ID {
		t.Fatalf("replay=%+v replayed=%v err=%v", replay, replayed, err)
	}
	if _, _, err := repository.PutProject(ctx, principalA, "site", "idem-a", "different", inputA); !errors.Is(err, api.ErrIdempotencyConflict) {
		t.Fatalf("conflict err=%v", err)
	}
}
