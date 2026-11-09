-- Revierte 000005. Pierde actores, agentes y tokens, el asignado y la clave
-- de origen de los issues, la marca de etiquetas importadas (quedan como
-- etiquetas comunes) y el agente de los eventos (actor_uid, la persona
-- responsable, se conserva). Los eventos 'assignee_changed' se borran: el
-- CHECK de 000004 no los acepta y no tienen equivalente.

DELETE FROM issues.issue_events WHERE kind = 'assignee_changed';
ALTER TABLE issues.issue_events DROP CONSTRAINT issue_events_kind_chk;
ALTER TABLE issues.issue_events ADD CONSTRAINT issue_events_kind_chk CHECK (kind IN ('created',
    'status_changed', 'title_changed', 'description_changed', 'priority_changed', 'labels_changed',
    'commented', 'discarded', 'restored'));
ALTER TABLE issues.issue_events DROP COLUMN IF EXISTS agent_id;

ALTER TABLE issues.issue_labels DROP COLUMN IF EXISTS imported;

DROP INDEX IF EXISTS issues.issues_project_assignee_idx;
ALTER TABLE issues.issues
    DROP CONSTRAINT IF EXISTS issues_project_id_source_key_key,
    DROP CONSTRAINT IF EXISTS issues_source_key_not_empty_chk,
    DROP COLUMN IF EXISTS source_key,
    DROP COLUMN IF EXISTS assignee_actor_id;

DROP TABLE IF EXISTS issues.tokens;
DROP TABLE IF EXISTS issues.actors;
