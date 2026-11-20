-- Issue 016: deshace 000010_integrations (conexiones, repos y links se
-- pierden; los secretos viven en variables de entorno y no se tocan).
DROP TABLE issues.webhook_deliveries;
DROP TABLE issues.issue_links;
DROP TABLE issues.repositories;
DROP TABLE issues.repo_connections;
