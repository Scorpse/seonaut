package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	operationPutProject  = "projects.put"
	operationStartCrawl  = "crawls.start"
	findIdempotencySQL   = `SELECT request_hash, resource_id FROM api_idempotency WHERE key_public_id = ? AND operation = ? AND idempotency_key = ? LIMIT 1 FOR UPDATE`
	insertIdempotencySQL = `INSERT INTO api_idempotency (key_public_id, tenant_id, operation, idempotency_key, request_hash, resource_type, resource_id, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
)

type idempotencyRecord struct {
	RequestHash string
	ResourceID  string
}

func findIdempotency(ctx context.Context, tx *sql.Tx, keyID, operation, idempotencyKey string) (idempotencyRecord, error) {
	var record idempotencyRecord
	err := tx.QueryRowContext(ctx, findIdempotencySQL, keyID, operation, idempotencyKey).Scan(&record.RequestHash, &record.ResourceID)
	return record, err
}

func saveIdempotency(ctx context.Context, tx *sql.Tx, keyID, tenantID, operation, idempotencyKey, requestHash, resourceType, resourceID string, now time.Time) error {
	if keyID == "" || tenantID == "" || idempotencyKey == "" || requestHash == "" || resourceID == "" {
		return errors.New("invalid idempotency record")
	}
	_, err := tx.ExecContext(ctx, insertIdempotencySQL, keyID, tenantID, operation, idempotencyKey, requestHash, resourceType, resourceID, now, now.Add(24*time.Hour))
	return err
}
