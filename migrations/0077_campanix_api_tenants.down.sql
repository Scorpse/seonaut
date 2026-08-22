ALTER TABLE api_keys DROP FOREIGN KEY api_keys_tenant_fk;
DROP TABLE IF EXISTS api_tenants;
ALTER TABLE users DROP COLUMN api_only;
