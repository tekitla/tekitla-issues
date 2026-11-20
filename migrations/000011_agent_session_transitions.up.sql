-- ============================================================
-- Issue 036, parte 3 (tekitla-protos v0.12.0): historia de las sesiones de
-- agentes, para medir la eficiencia del flujo y la cola humana, y el motivo
-- de la espera (espera.ps1 -Motivo).
-- ============================================================
-- Solo agrega: una tabla nueva y una columna con DEFAULT ''. El DEFAULT no
-- cambia el significado de ninguna fila: hasta hoy ninguna sesión traía
-- motivo, y '' es «sin motivo». Sin BEGIN/COMMIT propios:
-- scripts/apply_migration.sh envuelve el archivo en una transacción (y lo
-- prueba en seco con ROLLBACK).
-- Compatible con el binario anterior (prod-v0.8.0): no lee la tabla nueva y
-- sus INSERT a agent_sessions toman el DEFAULT. Va antes del deploy de la
-- imagen que la usa (la imagen nueva escribe las dos cosas).
-- Sin backfill: las sesiones anteriores no guardaban historia. La eficiencia
-- se mide desde el primer reporte con la imagen nueva.
-- Número: 000010 es del issue 016 (integraciones). Si este sale antes, se
-- renumera.


-- ------------------------------------------------------------
-- agent_sessions: para qué espera la sesión a su persona
-- ------------------------------------------------------------
ALTER TABLE issues.agent_sessions ADD COLUMN waiting_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE issues.agent_sessions ADD CONSTRAINT agent_sessions_waiting_reason_chk
    CHECK (waiting_reason IN ('', 'review', 'testing', 'decision', 'deploy'));
-- Sin espera no hay motivo. esperando-ok sin token (waiting_ok con
-- waiting_on 'none') también espera a su persona y puede traerlo.
ALTER TABLE issues.agent_sessions ADD CONSTRAINT agent_sessions_waiting_reason_none_chk
    CHECK (waiting_on <> 'none' OR status = 'waiting_ok' OR waiting_reason = '');


-- ------------------------------------------------------------
-- agent_session_transitions: cada cambio de estado, espera, motivo o issue
-- ------------------------------------------------------------
-- Desde at la sesión está así hasta la fila siguiente (por at, seq). La
-- escribe ReportSessions; los latidos no escriben nada. 'unreported' marca
-- un hueco sin reportes de más de SESSION_STALL_AFTER (solo existe acá).
CREATE TABLE issues.agent_session_transitions (
    session_id     UUID        NOT NULL REFERENCES issues.agent_sessions (id) ON DELETE CASCADE,
    seq            BIGSERIAL   NOT NULL,
    at             TIMESTAMPTZ NOT NULL,
    status         TEXT        NOT NULL,
    waiting_on     TEXT        NOT NULL,
    waiting_reason TEXT        NOT NULL,
    -- A qué issue estaba ligada en ese momento; los dos NULL = sin issue.
    project_id     UUID        REFERENCES issues.projects (id) ON DELETE CASCADE,
    issue_number   INTEGER,
    PRIMARY KEY (session_id, seq),
    CONSTRAINT agent_session_transitions_status_chk
        CHECK (status IN ('working', 'waiting_ok', 'idle', 'ended', 'unknown', 'unreported')),
    CONSTRAINT agent_session_transitions_waiting_on_chk
        CHECK (waiting_on IN ('none', 'person', 'session', 'other')),
    CONSTRAINT agent_session_transitions_waiting_reason_chk
        CHECK (waiting_reason IN ('', 'review', 'testing', 'decision', 'deploy')),
    CONSTRAINT agent_session_transitions_issue_chk CHECK (
        (project_id IS NULL AND issue_number IS NULL)
        OR (project_id IS NOT NULL AND issue_number IS NOT NULL AND issue_number > 0))
);

-- GetFlowMetrics y GetIssueFlowEfficiency: las sesiones que tocaron un
-- proyecto o un issue.
CREATE INDEX agent_session_transitions_issue_idx
    ON issues.agent_session_transitions (project_id, issue_number, at);
