ALTER TABLE api_keys DROP FOREIGN KEY api_keys_project_tenant_fk;
DROP TABLE IF EXISTS api_idempotency;
DROP TABLE IF EXISTS api_projects;
ALTER TABLE api_keys DROP INDEX api_keys_public_tenant_uq;
ALTER TABLE projects DROP INDEX projects_id_user_uq;
ALTER TABLE api_tenants DROP INDEX api_tenants_id_user_uq;
