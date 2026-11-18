-- ============================================================
-- Issue 026 (tekitla-protos v0.10.0): sesiones de agentes que reporta el
-- recolector local de herdr, y el permiso sessions:write de los tokens.
-- ============================================================
-- Solo agrega: una tabla nueva y un valor más permitido en
-- tokens.scopes. Ningún dato existente cambia. Sin BEGIN/COMMIT propios:
-- scripts/apply_migration.sh envuelve el archivo en una transacción (y lo
-- prueba en seco con ROLLBACK).
-- Compatible con el binario anterior (v0.9): no lee la tabla nueva y nunca
-- emite tokens con sessions:write. Se puede aplicar antes de desplegar la
-- imagen nueva (y tiene que ir antes: la imagen nueva la usa).
-- Número: 000008 es del issue 009 (share_links).


-- ------------------------------------------------------------
-- tokens: sessions:write (reportar sesiones; no lee ni cambia issues)
-- ------------------------------------------------------------
ALTER TABLE issues.tokens DROP CONSTRAINT tokens_scopes_chk;
ALTER TABLE issues.tokens ADD CONSTRAINT tokens_scopes_chk CHECK (cardinality(scopes) > 0
    AND scopes <@ ARRAY['issues:read', 'issues:write', 'sessions:write']::TEXT[]);


-- ------------------------------------------------------------
-- agent_sessions: una corrida de una herramienta en una terminal
-- ------------------------------------------------------------
CREATE TABLE issues.agent_sessions (
    id                UUID        NOT NULL,
    -- La persona dueña (la del token que reporta).
    owner_actor_id    UUID        NOT NULL REFERENCES issues.actors (id) ON DELETE RESTRICT,
    -- El agente de la persona para esa herramienta y máquina.
    agent_actor_id    UUID        NOT NULL REFERENCES issues.actors (id) ON DELETE RESTRICT,
    -- 'herdr' (enum SessionSource).
    source            TEXT        NOT NULL,
    -- Nombre corto de la máquina, [A-Za-z0-9._-]{1,40}.
    machine           TEXT        NOT NULL,
    -- Id de la sesión en su origen (agent_session.value de herdr).
    external_id       TEXT        NOT NULL,
    -- 'claude_code' | 'codex' | 'cloud' | 'other' (enum AgentTool).
    tool              TEXT        NOT NULL,
    -- Relativo a la raíz configurada en el recolector; '' fuera de ella.
    -- Nunca una ruta absoluta (lo valida el servicio).
    cwd_rel           TEXT        NOT NULL,
    session_name      TEXT        NOT NULL,
    workspace_label   TEXT        NOT NULL,
    -- Proyecto e issue a los que se ligó; los dos NULL = sin issue. El
    -- número puede no existir todavía en el proyecto.
    project_id        UUID        REFERENCES issues.projects (id) ON DELETE CASCADE,
    issue_number      INTEGER,
    -- 'none' | 'agent_name' | 'workspace' | 'cwd' (enum SessionLink).
    link              TEXT        NOT NULL,
    -- 'working' | 'waiting_ok' | 'idle' | 'ended' | 'unknown'. 'colgado'
    -- (STALLED) no se guarda: se calcula al leer.
    status            TEXT        NOT NULL,
    -- 'none' | 'person' | 'session' | 'other' (enum WaitingOn).
    waiting_on        TEXT        NOT NULL,
    waiting_target    TEXT        NOT NULL,
    waiting_text      TEXT        NOT NULL,
    status_changed_at TIMESTAMPTZ NOT NULL,
    last_seen_at      TIMESTAMPTZ NOT NULL,
    started_at        TIMESTAMPTZ NOT NULL,
    ended_at          TIMESTAMPTZ,
    PRIMARY KEY (id),
    -- Clave de idempotencia de ReportSessions.
    CONSTRAINT agent_sessions_external_key UNIQUE (owner_actor_id, source, machine, external_id),
    CONSTRAINT agent_sessions_source_chk CHECK (source IN ('herdr')),
    CONSTRAINT agent_sessions_machine_chk CHECK (machine ~ '^[A-Za-z0-9._-]{1,40}$'),
    CONSTRAINT agent_sessions_external_id_chk CHECK (external_id <> ''),
    CONSTRAINT agent_sessions_tool_chk CHECK (tool IN ('claude_code', 'codex', 'cloud', 'other')),
    CONSTRAINT agent_sessions_link_chk CHECK (link IN ('none', 'agent_name', 'workspace', 'cwd')),
    CONSTRAINT agent_sessions_issue_chk CHECK (
        (project_id IS NULL AND issue_number IS NULL AND link = 'none')
        OR (project_id IS NOT NULL AND issue_number > 0 AND link <> 'none')),
    CONSTRAINT agent_sessions_status_chk CHECK (status IN ('working', 'waiting_ok', 'idle', 'ended', 'unknown')),
    CONSTRAINT agent_sessions_ended_chk CHECK ((status = 'ended') = (ended_at IS NOT NULL)),
    CONSTRAINT agent_sessions_waiting_on_chk CHECK (waiting_on IN ('none', 'person', 'session', 'other')),
    -- Una sesión que trabaja o terminó no espera nada.
    CONSTRAINT agent_sessions_waiting_free_chk CHECK (
        status NOT IN ('working', 'ended') OR (waiting_on = 'none' AND waiting_target = '' AND waiting_text = ''))
);

-- Resumen de cada issue (Issue.sessions) y ListAgentSessions por issue.
CREATE INDEX agent_sessions_issue_idx ON issues.agent_sessions (project_id, issue_number)
    WHERE status <> 'ended';
-- ListAgentSessions de una persona, por last_seen_at.
CREATE INDEX agent_sessions_owner_seen_idx ON issues.agent_sessions (owner_actor_id, last_seen_at DESC, id DESC);
CREATE INDEX agent_sessions_agent_actor_id_idx ON issues.agent_sessions (agent_actor_id);
