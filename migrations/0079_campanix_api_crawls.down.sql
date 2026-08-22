DROP TABLE IF EXISTS api_crawls;
ALTER TABLE crawls DROP INDEX crawls_id_project_uq;
ALTER TABLE api_projects DROP INDEX api_projects_id_upstream_uq;
