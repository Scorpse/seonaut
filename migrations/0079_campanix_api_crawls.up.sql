ALTER TABLE api_projects
    ADD UNIQUE KEY api_projects_id_upstream_uq (id, upstream_project_id);

ALTER TABLE crawls
    ADD UNIQUE KEY crawls_id_project_uq (id, project_id);

CREATE TABLE api_crawls (
    id CHAR(36) NOT NULL,
    tenant_id CHAR(36) NOT NULL,
    project_id CHAR(36) NOT NULL,
    upstream_project_id INT UNSIGNED NOT NULL,
    upstream_crawl_id INT UNSIGNED NULL,
    state VARCHAR(24) NOT NULL,
    active_slot TINYINT UNSIGNED NULL,
    cancel_requested TINYINT(1) NOT NULL DEFAULT 0,
    queued_at DATETIME(6) NOT NULL,
    started_at DATETIME(6) NULL,
    finished_at DATETIME(6) NULL,
    failure_code VARCHAR(80) NULL,
    failure_message VARCHAR(255) NULL,
    total_urls INT NOT NULL DEFAULT 0,
    total_issues INT NOT NULL DEFAULT 0,
    updated_at DATETIME(6) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY api_crawls_project_active_uq (project_id, active_slot),
    UNIQUE KEY api_crawls_id_tenant_project_uq (id, tenant_id, project_id),
    KEY api_crawls_project_queued_idx (project_id, queued_at, id),
    CONSTRAINT api_crawls_state_chk CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'canceled')),
    CONSTRAINT api_crawls_active_chk CHECK ((state IN ('queued', 'running') AND active_slot = 1) OR (state IN ('succeeded', 'failed', 'canceled') AND active_slot IS NULL)),
    CONSTRAINT api_crawls_project_tenant_fk FOREIGN KEY (project_id, tenant_id) REFERENCES api_projects (id, tenant_id) ON DELETE CASCADE,
    CONSTRAINT api_crawls_project_upstream_fk FOREIGN KEY (project_id, upstream_project_id) REFERENCES api_projects (id, upstream_project_id) ON DELETE CASCADE,
    CONSTRAINT api_crawls_upstream_fk FOREIGN KEY (upstream_crawl_id, upstream_project_id) REFERENCES crawls (id, project_id) ON DELETE RESTRICT
);
