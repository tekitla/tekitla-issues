-- ============================================================
-- Issue 025 (tekitla-protos v0.5.0, ADR 0009): actores (personas y
-- agentes), tokens por actor, asignado, autoría con agente en el historial e
-- importación con clave de origen.
-- ============================================================
-- Solo agrega: tablas nuevas y columnas nullable sin DEFAULT (salvo
-- issue_labels.imported, ver abajo). Los issues existentes quedan sin asignar
-- y sin clave de origen, sus eventos sin agente y sus etiquetas como puestas
-- a mano (issue_labels.imported = false). Ningún dato existente cambia de
-- significado. Sin BEGIN/COMMIT propios:
-- scripts/apply_migration.sh envuelve el archivo en una transacción (y lo
-- prueba en seco con ROLLBACK).
-- Compatible con el binario anterior (v0.4): no lee ni escribe las columnas
-- nuevas, y lo que escribe queda sin asignar, sin agente y con etiquetas a
-- mano. Se puede aplicar antes de desplegar la imagen nueva.


-- ------------------------------------------------------------
-- actors: personas (una por uid de Firebase) y sus agentes
-- ------------------------------------------------------------
CREATE TABLE issues.actors (
    id             UUID        NOT NULL,
    -- 'person' | 'agent' (enum ActorKind).
    kind           TEXT        NOT NULL,
    -- Solo en personas: su uid de Firebase (x-user-id del gateway).
    user_uid       TEXT,
    -- Solo en agentes: la persona dueña (el servicio verifica que sea una
    -- persona).
    owner_actor_id UUID        REFERENCES issues.actors (id) ON DELETE RESTRICT,
    -- Solo en agentes: 'claude_code' | 'codex' | 'cloud' | 'other' (enum
    -- AgentTool).
    tool           TEXT,
    -- Persona: vacío (el servicio no conoce su nombre). Agente: el que le puso
    -- su dueño.
    display_name   TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL,
    -- No se deshace. Revocar a una persona revoca a sus agentes.
    revoked_at     TIMESTAMPTZ,
    PRIMARY KEY (id),
    CONSTRAINT actors_user_uid_key UNIQUE (user_uid),
    CONSTRAINT actors_kind_chk CHECK (kind IN ('person', 'agent')),
    CONSTRAINT actors_person_chk CHECK ((kind = 'person') = (user_uid IS NOT NULL)),
    CONSTRAINT actors_agent_owner_chk CHECK ((kind = 'agent') = (owner_actor_id IS NOT NULL)),
    CONSTRAINT actors_agent_tool_chk CHECK ((kind = 'agent') = (tool IS NOT NULL)),
    CONSTRAINT actors_tool_chk CHECK (tool IN ('claude_code', 'codex', 'cloud', 'other')),
    CONSTRAINT actors_user_uid_not_empty_chk CHECK (user_uid <> ''),
    CONSTRAINT actors_agent_name_chk CHECK (kind = 'person' OR display_name <> '')
);

-- Nombre de agente único entre los no revocados del mismo dueño, sin
-- distinguir mayúsculas. Sirve también para listar los agentes de un dueño.
CREATE UNIQUE INDEX actors_owner_lower_name_key ON issues.actors (owner_actor_id, lower(display_name))
    WHERE revoked_at IS NULL;
CREATE INDEX actors_owner_actor_id_idx ON issues.actors (owner_actor_id);

-- ------------------------------------------------------------
-- tokens: `tka_…` emitidos a nombre de un actor. Solo el SHA-256.
-- ------------------------------------------------------------
CREATE TABLE issues.tokens (
    id           UUID        NOT NULL,
    actor_id     UUID        NOT NULL REFERENCES issues.actors (id) ON DELETE RESTRICT,
    name         TEXT        NOT NULL,
    -- SHA-256 del token en claro. El token no se guarda.
    token_hash   BYTEA       NOT NULL,
    -- 'issues:read' | 'issues:write' (enum TokenScope), sin repetidos.
    scopes       TEXT[]      NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL,
    -- Obligatorio: 90 días como máximo (lo valida el servicio).
    expires_at   TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    PRIMARY KEY (id),
    CONSTRAINT tokens_token_hash_key UNIQUE (token_hash),
    CONSTRAINT tokens_token_hash_len_chk CHECK (octet_length(token_hash) = 32),
    CONSTRAINT tokens_scopes_chk CHECK (cardinality(scopes) > 0
        AND scopes <@ ARRAY['issues:read', 'issues:write']::TEXT[]),
    CONSTRAINT tokens_expires_chk CHECK (expires_at > created_at)
);

CREATE INDEX tokens_actor_id_idx ON issues.tokens (actor_id);

-- ------------------------------------------------------------
-- issues: asignado y clave de origen
-- ------------------------------------------------------------
ALTER TABLE issues.issues
    -- Persona o agente; NULL = sin asignar. Puede quedar un actor revocado.
    ADD COLUMN assignee_actor_id UUID REFERENCES issues.actors (id) ON DELETE RESTRICT,
    -- Clave en el tracker de origen (ImportIssue); NULL si se creó en Tekitla.
    ADD COLUMN source_key        TEXT,
    ADD CONSTRAINT issues_source_key_not_empty_chk CHECK (source_key <> ''),
    ADD CONSTRAINT issues_project_id_source_key_key UNIQUE (project_id, source_key);

-- Soporta el filtro por asignado de ListIssues.
CREATE INDEX issues_project_assignee_idx ON issues.issues (project_id, assignee_actor_id);

-- Etiquetas que puso el importador (ImportIssue solo agrega o quita esas,
-- nunca las puestas a mano). DEFAULT false no cambia el significado de nada:
-- las existentes son todas a mano, y también las que ponga el binario
-- anterior mientras conviva con la migración. El importador siempre escribe
-- true.
ALTER TABLE issues.issue_labels ADD COLUMN imported BOOLEAN NOT NULL DEFAULT false;

-- ------------------------------------------------------------
-- issue_events: autoría con agente y evento de asignado
-- ------------------------------------------------------------
-- actor_uid sigue siendo la persona responsable; agent_id es el agente que
-- hizo el cambio a su nombre (NULL si lo hizo la persona, y en todos los
-- eventos anteriores).
ALTER TABLE issues.issue_events ADD COLUMN agent_id UUID REFERENCES issues.actors (id) ON DELETE RESTRICT;

ALTER TABLE issues.issue_events DROP CONSTRAINT issue_events_kind_chk;
ALTER TABLE issues.issue_events ADD CONSTRAINT issue_events_kind_chk CHECK (kind IN ('created',
    'status_changed', 'title_changed', 'description_changed', 'priority_changed', 'labels_changed',
    'commented', 'discarded', 'restored', 'assignee_changed'));
