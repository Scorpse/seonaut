package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stjudewashere/seonaut/internal/api"
	"github.com/stjudewashere/seonaut/internal/passwordhash"
)

const findAPITenantSQL = `SELECT id, external_tenant_id, state, service_email, created_at FROM api_tenants WHERE external_tenant_id = ? LIMIT 1`

type APITenantRepository struct{ DB *sql.DB }

func (r APITenantRepository) GetTenant(ctx context.Context, externalID string) (api.TenantBinding, error) {
	if r.DB == nil {
		return api.TenantBinding{}, api.ErrTenantNotFound
	}
	return scanAPITenant(r.DB.QueryRowContext(ctx, findAPITenantSQL, externalID))
}

func (r APITenantRepository) ProvisionTenant(ctx context.Context, externalID string) (api.TenantBinding, error) {
	externalID = strings.TrimSpace(externalID)
	if r.DB == nil || externalID == "" || len(externalID) > 191 {
		return api.TenantBinding{}, api.ErrTenantNotFound
	}
	if binding, err := r.GetTenant(ctx, externalID); err == nil {
		return binding, nil
	} else if !errors.Is(err, api.ErrTenantNotFound) {
		return api.TenantBinding{}, err
	}

	tenantID, err := newOpaqueID()
	if err != nil {
		return api.TenantBinding{}, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return api.TenantBinding{}, err
	}
	passwordHash, err := passwordhash.Hash(secret)
	if err != nil {
		return api.TenantBinding{}, err
	}
	emailHash := sha256.Sum256([]byte(externalID))
	serviceEmail := fmt.Sprintf("tenant-%x@service.invalid", emailHash[:12])
	now := time.Now().UTC()

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return api.TenantBinding{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO users (email, password, lang, theme, created, api_only) VALUES (?, ?, 'en', 'light', ?, 1)`, serviceEmail, passwordHash, now)
	if err != nil {
		return api.TenantBinding{}, err
	}
	userID, err := result.LastInsertId()
	if err != nil {
		return api.TenantBinding{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO api_tenants (id, external_tenant_id, upstream_user_id, service_email, state, created_at, updated_at) VALUES (?, ?, ?, ?, 'active', ?, ?)`, tenantID, externalID, userID, serviceEmail, now, now)
	if err != nil {
		_ = tx.Rollback()
		if existing, findErr := r.GetTenant(ctx, externalID); findErr == nil {
			return existing, nil
		}
		return api.TenantBinding{}, err
	}
	if err := tx.Commit(); err != nil {
		return api.TenantBinding{}, err
	}
	return api.TenantBinding{ID: tenantID, ExternalID: externalID, State: "active", ServiceEmail: serviceEmail, CreatedAt: now}, nil
}

type tenantScanner interface{ Scan(...any) error }

func scanAPITenant(row tenantScanner) (api.TenantBinding, error) {
	var binding api.TenantBinding
	if err := row.Scan(&binding.ID, &binding.ExternalID, &binding.State, &binding.ServiceEmail, &binding.CreatedAt); errors.Is(err, sql.ErrNoRows) {
		return api.TenantBinding{}, api.ErrTenantNotFound
	} else if err != nil {
		return api.TenantBinding{}, err
	}
	return binding, nil
}

func newOpaqueID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}
