package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/stjudewashere/seonaut/internal/api"
)

const insertAPIAuditSQL = `INSERT INTO api_audit_log (request_id, key_public_id, tenant_id, project_id, crawl_id, action, outcome, http_status, source_address, metadata, created_at) VALUES (?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, NULLIF(?, ''), ?, ?)`

type APIAuditRepository struct{ DB *sql.DB }

func (r APIAuditRepository) RecordAudit(ctx context.Context, record api.AuditRecord) error {
	metadata, err := json.Marshal(record.Metadata)
	if err != nil {
		return err
	}
	createdAt := record.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err = r.DB.ExecContext(ctx, insertAPIAuditSQL,
		record.RequestID, record.KeyPublicID, record.TenantID, record.ProjectID, record.CrawlID,
		record.Action, record.Outcome, record.HTTPStatus, record.SourceIP, metadata, createdAt,
	)
	return err
}
