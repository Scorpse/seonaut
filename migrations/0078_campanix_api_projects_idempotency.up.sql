ALTER TABLE api_tenants
    ADD UNIQUE KEY api_tenants_id_user_uq (id, upstream_user_id);

ALTER TABLE projects
    ADD UNIQUE KEY projects_id_user_uq (id, user_id);

ALTER TABLE api_keys
    ADD UNIQUE KEY api_keys_public_tenant_uq (public_id, tenant_id);

CREATE TABLE api_projects (
    id CHAR(36) NOT NULL,
    tenant_id CHAR(36) NOT NULL,
    external_project_id VARCHAR(191) NOT NULL,
    upstream_project_id INT UNSIGNED NOT NULL,
    upstream_user_id INT UNSIGNED NOT NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY api_projects_tenant_external_uq (tenant_id, external_project_id),
    UNIQUE KEY api_projects_upstream_uq (upstream_project_id),
    UNIQUE KEY api_projects_id_tenant_uq (id, tenant_id),
    KEY api_projects_tenant_created_idx (tenant_id, created_at, id),
    CONSTRAINT api_projects_tenant_owner_fk FOREIGN KEY (tenant_id, upstream_user_id) REFERENCES api_tenants (id, upstream_user_id) ON DELETE RESTRICT,
    CONSTRAINT api_projects_upstream_owner_fk FOREIGN KEY (upstream_project_id, upstream_user_id) REFERENCES projects (id, user_id) ON DELETE CASCADE
);

CREATE TABLE api_idempotency (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    key_public_id VARCHAR(64) NOT NULL,
    tenant_id CHAR(36) NOT NULL,
    operation VARCHAR(80) NOT NULL,
    idempotency_key VARCHAR(191) NOT NULL,
    request_hash CHAR(64) NOT NULL,
    resource_type VARCHAR(40) NOT NULL,
    resource_id CHAR(36) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    expires_at DATETIME(6) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY api_idempotency_principal_operation_key_uq (key_public_id, operation, idempotency_key),
    KEY api_idempotency_tenant_created_idx (tenant_id, created_at),
    KEY api_idempotency_expiry_idx (expires_at),
    CONSTRAINT api_idempotency_tenant_fk FOREIGN KEY (tenant_id) REFERENCES api_tenants (id) ON DELETE CASCADE,
    CONSTRAINT api_idempotency_key_tenant_fk FOREIGN KEY (key_public_id, tenant_id) REFERENCES api_keys (public_id, tenant_id) ON DELETE RESTRICT
);

-- Project-bound keys could be issued before api_projects existed, so no
-- pre-migration value has a trustworthy binding to preserve.
UPDATE api_keys SET project_id = NULL WHERE project_id IS NOT NULL;

ALTER TABLE api_keys
    ADD CONSTRAINT api_keys_project_tenant_fk FOREIGN KEY (project_id, tenant_id) REFERENCES api_projects (id, tenant_id) ON DELETE RESTRICT;
