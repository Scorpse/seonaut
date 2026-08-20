CREATE TABLE api_keys (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    public_id VARCHAR(64) NOT NULL,
    secret_hash VARCHAR(255) NOT NULL,
    key_kind VARCHAR(24) NOT NULL,
    tenant_id CHAR(36) NULL,
    project_id CHAR(36) NULL,
    scopes JSON NOT NULL,
    created_at DATETIME(6) NOT NULL,
    expires_at DATETIME(6) NULL,
    revoked_at DATETIME(6) NULL,
    rotated_from_public_id VARCHAR(64) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY api_keys_public_id_uq (public_id),
    KEY api_keys_tenant_kind_idx (tenant_id, key_kind),
    KEY api_keys_project_kind_idx (project_id, key_kind),
    CONSTRAINT api_keys_kind_chk CHECK (key_kind IN ('platform', 'tenant', 'read_only'))
);

CREATE TABLE api_audit_log (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    request_id VARCHAR(80) NOT NULL,
    key_public_id VARCHAR(64) NULL,
    tenant_id CHAR(36) NULL,
    project_id CHAR(36) NULL,
    crawl_id CHAR(36) NULL,
    action VARCHAR(120) NOT NULL,
    outcome VARCHAR(32) NOT NULL,
    http_status SMALLINT UNSIGNED NOT NULL,
    source_address VARCHAR(64) NULL,
    metadata JSON NOT NULL,
    created_at DATETIME(6) NOT NULL,
    PRIMARY KEY (id),
    KEY api_audit_request_idx (request_id),
    KEY api_audit_tenant_created_idx (tenant_id, created_at),
    KEY api_audit_key_created_idx (key_public_id, created_at)
);
