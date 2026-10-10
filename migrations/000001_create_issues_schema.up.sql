-- ============================================================
-- Schema issues (issues-service)
-- ============================================================
-- No hay migration runner ni tabla schema_migrations: los SQL se aplican a
-- mano con scripts/apply_migration.sh ANTES de desplegar el contenedor que
-- los necesita (ver CLAUDE.md, "Deploy").
--
-- Las tablas viven en migraciones propias (000002 en adelante).

CREATE SCHEMA IF NOT EXISTS issues;
