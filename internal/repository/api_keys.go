package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/stjudewashere/seonaut/internal/api"
)

const findAPIKeySQL = `SELECT public_id, secret_hash, key_kind, COALESCE(tenant_id, ''), COALESCE(project_id, ''), scopes, created_at, expires_at, revoked_at, COALESCE(rotated_from_public_id, '') FROM api_keys WHERE public_id = ? LIMIT 1`

const createAPIKeySQL = `INSERT INTO api_keys (public_id, secret_hash, key_kind, tenant_id, project_id, scopes, created_at, expires_at, rotated_from_public_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

const expireAPIKeySQL = `UPDATE api_keys SET expires_at = ? WHERE public_id = ? AND key_kind = ? AND revoked_at IS NULL`

const listAPIKeysSQL = `SELECT public_id, secret_hash, key_kind, COALESCE(tenant_id, ''), COALESCE(project_id, ''), scopes, created_at, expires_at, revoked_at, COALESCE(rotated_from_public_id, '') FROM api_keys WHERE key_kind = ? ORDER BY created_at, public_id`

const listTenantAPIKeysSQL = `SELECT public_id, secret_hash, key_kind, COALESCE(tenant_id, ''), COALESCE(project_id, ''), scopes, created_at, expires_at, revoked_at, COALESCE(rotated_from_public_id, '') FROM api_keys WHERE tenant_id = ? AND key_kind IN ('tenant', 'read_only') ORDER BY created_at, public_id`

type APIKeyRepository struct {
	DB *sql.DB
}

func (r APIKeyRepository) FindAPIKey(ctx context.Context, publicID string) (api.StoredKey, error) {
	if r.DB == nil {
		return api.StoredKey{}, api.ErrKeyNotFound
	}
	return scanAPIKey(r.DB.QueryRowContext(ctx, findAPIKeySQL, publicID))
}

func (r APIKeyRepository) CreateAPIKey(ctx context.Context, key api.StoredKey) error {
	if r.DB == nil {
		return errors.New("api key repository has no database")
	}
	return createAPIKey(ctx, r.DB, key)
}

func (r APIKeyRepository) ListAPIKeys(ctx context.Context, kind api.KeyKind) ([]api.StoredKey, error) {
	if r.DB == nil {
		return nil, errors.New("api key repository has no database")
	}
	rows, err := r.DB.QueryContext(ctx, listAPIKeysSQL, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []api.StoredKey{}
	for rows.Next() {
		key, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (r APIKeyRepository) RotateAPIKey(ctx context.Context, oldPublicID string, replacement api.StoredKey, overlapUntil time.Time) error {
	if r.DB == nil {
		return errors.New("api key repository has no database")
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, expireAPIKeySQL, overlapUntil, oldPublicID, api.KeyPlatform)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return api.ErrKeyNotFound
	}
	if err := createAPIKey(ctx, tx, replacement); err != nil {
		return err
	}
	return tx.Commit()
}

func (r APIKeyRepository) RevokeAPIKey(ctx context.Context, publicID string, kind api.KeyKind, at time.Time) error {
	if r.DB == nil {
		return errors.New("api key repository has no database")
	}
	result, err := r.DB.ExecContext(ctx, `UPDATE api_keys SET revoked_at = COALESCE(revoked_at, ?) WHERE public_id = ? AND key_kind = ?`, at, publicID, kind)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return api.ErrKeyNotFound
	}
	return nil
}

func (r APIKeyRepository) ListTenantAPIKeys(ctx context.Context, tenantID string) ([]api.StoredKey, error) {
	if r.DB == nil {
		return nil, errors.New("api key repository has no database")
	}
	rows, err := r.DB.QueryContext(ctx, listTenantAPIKeysSQL, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []api.StoredKey{}
	for rows.Next() {
		key, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (r APIKeyRepository) RotateTenantAPIKey(ctx context.Context, tenantID, oldPublicID string, replacement api.StoredKey, overlapUntil time.Time) error {
	if r.DB == nil {
		return errors.New("api key repository has no database")
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE api_keys SET expires_at = ? WHERE public_id = ? AND tenant_id = ? AND key_kind IN ('tenant', 'read_only') AND revoked_at IS NULL`, overlapUntil, oldPublicID, tenantID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return api.ErrKeyNotFound
	}
	if err := createAPIKey(ctx, tx, replacement); err != nil {
		return err
	}
	return tx.Commit()
}

func (r APIKeyRepository) RevokeTenantAPIKey(ctx context.Context, tenantID, publicID string, at time.Time) error {
	if r.DB == nil {
		return errors.New("api key repository has no database")
	}
	result, err := r.DB.ExecContext(ctx, `UPDATE api_keys SET revoked_at = COALESCE(revoked_at, ?) WHERE public_id = ? AND tenant_id = ? AND key_kind IN ('tenant', 'read_only')`, at, publicID, tenantID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return api.ErrKeyNotFound
	}
	return nil
}

func (r APIKeyRepository) ProjectBelongsToTenant(ctx context.Context, tenantID, projectID string) (bool, error) {
	if r.DB == nil || tenantID == "" || projectID == "" {
		return false, nil
	}
	var exists int
	err := r.DB.QueryRowContext(ctx, `SELECT 1 FROM api_projects WHERE tenant_id = ? AND id = ? LIMIT 1`, tenantID, projectID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && exists == 1, err
}

type rowScanner interface {
	Scan(...any) error
}

func scanAPIKey(row rowScanner) (api.StoredKey, error) {
	var key api.StoredKey
	var scopesJSON []byte
	var expiresAt, revokedAt sql.NullTime
	err := row.Scan(
		&key.PublicID,
		&key.SecretHash,
		&key.Kind,
		&key.TenantID,
		&key.ProjectID,
		&scopesJSON,
		&key.CreatedAt,
		&expiresAt,
		&revokedAt,
		&key.RotatedFrom,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return api.StoredKey{}, api.ErrKeyNotFound
	}
	if err != nil {
		return api.StoredKey{}, err
	}
	if err := json.Unmarshal(scopesJSON, &key.Scopes); err != nil {
		return api.StoredKey{}, err
	}
	if expiresAt.Valid {
		key.ExpiresAt = &expiresAt.Time
	}
	if revokedAt.Valid {
		key.RevokedAt = &revokedAt.Time
	}
	return key, nil
}

type execContext interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func createAPIKey(ctx context.Context, executor execContext, key api.StoredKey) error {
	scopes, err := json.Marshal(key.Scopes)
	if err != nil {
		return err
	}
	_, err = executor.ExecContext(
		ctx,
		createAPIKeySQL,
		key.PublicID,
		key.SecretHash,
		key.Kind,
		nullableString(key.TenantID),
		nullableString(key.ProjectID),
		string(scopes),
		key.CreatedAt,
		key.ExpiresAt,
		nullableString(key.RotatedFrom),
	)
	return err
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
