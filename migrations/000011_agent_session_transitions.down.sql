-- Revierte 000011. Se pierde la historia de las sesiones (y con ella la
-- eficiencia del flujo medida hasta ahí) y el motivo de las esperas. Las
-- sesiones en sí se conservan.

DROP TABLE IF EXISTS issues.agent_session_transitions;

ALTER TABLE issues.agent_sessions DROP CONSTRAINT IF EXISTS agent_sessions_waiting_reason_none_chk;
ALTER TABLE issues.agent_sessions DROP CONSTRAINT IF EXISTS agent_sessions_waiting_reason_chk;
ALTER TABLE issues.agent_sessions DROP COLUMN IF EXISTS waiting_reason;
