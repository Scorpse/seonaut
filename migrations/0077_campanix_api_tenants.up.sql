ALTER TABLE users ADD COLUMN api_only TINYINT(1) NOT NULL DEFAULT 0;

CREATE TABLE api_tenants (
    id CHAR(36) NOT NULL,
    external_tenant_id VARCHAR(191) NOT NULL,
    upstream_user_id INT UNSIGNED NOT NULL,
    service_email VARCHAR(256) NOT NULL,
    state VARCHAR(24) NOT NULL DEFAULT 'active',
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY api_tenants_external_id_uq (external_tenant_id),
    UNIQUE KEY api_tenants_user_uq (upstream_user_id),
    CONSTRAINT api_tenants_state_chk CHECK (state IN ('active', 'suspended')),
    CONSTRAINT api_tenants_user_fk FOREIGN KEY (upstream_user_id) REFERENCES users (id) ON DELETE RESTRICT
);

ALTER TABLE api_keys
    ADD CONSTRAINT api_keys_tenant_fk FOREIGN KEY (tenant_id) REFERENCES api_tenants (id) ON DELETE RESTRICT;
