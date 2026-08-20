CREATE TABLE api_export_jobs (
    id CHAR(36) NOT NULL,
    tenant_id CHAR(36) NOT NULL,
    project_id CHAR(36) NOT NULL,
    crawl_id CHAR(36) NOT NULL,
    kind VARCHAR(40) NOT NULL,
    state VARCHAR(16) NOT NULL,
    artifact_path VARCHAR(1024) NULL,
    artifact_size BIGINT NOT NULL DEFAULT 0,
    failure_code VARCHAR(80) NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    finished_at DATETIME(6) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY api_export_jobs_crawl_kind_uq (crawl_id, kind),
    KEY api_export_jobs_tenant_project_idx (tenant_id, project_id, created_at, id),
    CONSTRAINT api_export_jobs_state_chk CHECK (state IN ('pending', 'ready', 'failed')),
    CONSTRAINT api_export_jobs_crawl_owner_fk FOREIGN KEY (crawl_id, tenant_id, project_id) REFERENCES api_crawls (id, tenant_id, project_id) ON DELETE CASCADE
);
