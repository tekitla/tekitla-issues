-- ============================================================
-- Issue 009 (tekitla-protos v0.9.0): enlaces de solo lectura de un proyecto.
-- ============================================================
-- Solo crea una tabla nueva: ningún dato existente cambia de significado y
-- el binario anterior no la lee ni la escribe, así que se puede aplicar antes
-- del deploy. Sin BEGIN/COMMIT propios: scripts/apply_migration.sh envuelve
-- el archivo en una transacción (y lo prueba en seco con ROLLBACK).
--
-- Número: el 009 se queda con 000008 y el 026 toma 000009 (decisión de
-- Victor, 2026-11-18: el 009 publica primero).

CREATE TABLE issues.share_links (
    id                 UUID        NOT NULL,
    project_id         UUID        NOT NULL REFERENCES issues.projects (id) ON DELETE RESTRICT,
    -- SHA-256 del token en claro (`tks_…`). El token no se guarda.
    token_hash         BYTEA       NOT NULL,
    -- 'plan' | 'panel' (enum ShareLinkScope). No cambia.
    scope              TEXT        NOT NULL,
    -- Para quién es (p. ej. «jefe»).
    note               TEXT        NOT NULL,
    -- Nombre con el que se muestra al dueño en las vistas públicas.
    owner_display_name TEXT        NOT NULL,
    -- uid de quien lo creó (auditoría). Nunca sale del servicio.
    created_by_uid     TEXT        NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL,
    -- NULL: sin vencimiento.
    expires_at         TIMESTAMPTZ,
    -- Aproximado: se actualiza como mucho una vez por minuto.
    last_used_at       TIMESTAMPTZ,
    -- No se deshace.
    revoked_at         TIMESTAMPTZ,
    PRIMARY KEY (id),
    CONSTRAINT share_links_token_hash_key UNIQUE (token_hash),
    CONSTRAINT share_links_token_hash_len_chk CHECK (octet_length(token_hash) = 32),
    CONSTRAINT share_links_scope_chk CHECK (scope IN ('plan', 'panel')),
    CONSTRAINT share_links_note_chk CHECK (char_length(note) BETWEEN 1 AND 80),
    CONSTRAINT share_links_owner_name_chk CHECK (char_length(owner_display_name) <= 40),
    CONSTRAINT share_links_created_by_chk CHECK (created_by_uid <> ''),
    CONSTRAINT share_links_expires_chk CHECK (expires_at IS NULL OR expires_at > created_at)
);

-- ListShareLinks: los del proyecto, los más nuevos primero. También sirve
-- para contar los activos al crear.
CREATE INDEX share_links_project_created_idx ON issues.share_links (project_id, created_at DESC, id DESC);
