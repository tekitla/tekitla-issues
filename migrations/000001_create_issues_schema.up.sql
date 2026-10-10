-- ============================================================
-- Schema issues (issues-service)
-- ============================================================
-- No hay migration runner ni tabla schema_migrations: los SQL se aplican a
-- mano con scripts/apply_migration.sh ANTES de desplegar el contenedor que
-- los necesita (ver CLAUDE.md, "Deploy").
--
-- Mientras nada esté desplegado, no acumules migraciones: reescribí 000001.
-- PKs: UUID por default (generado en el service con uuid.NewString()).

CREATE SCHEMA IF NOT EXISTS issues;

-- ------------------------------------------------------------
-- items (EJEMPLO de la plantilla: borrar junto con la entidad Item)
-- ------------------------------------------------------------
CREATE TABLE issues.items (
    id          UUID        NOT NULL,
    name        TEXT        NOT NULL,
    description TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT items_name_key UNIQUE (name)
);

-- Soporta el keyset de ListItems: ORDER BY created_at DESC, id DESC.
CREATE INDEX items_created_at_id_idx ON issues.items (created_at DESC, id DESC);
