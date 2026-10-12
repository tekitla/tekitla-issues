-- Revierte 000004. Pierde el motivo de los descartes, el color, el límite
-- WIP y la visibilidad de los estados; los issues descartados siguen en su
-- estado CANCELED. Los eventos 'discarded' y 'restored' se borran (el CHECK
-- de 000003 no los acepta): el historial pierde esas entradas.

ALTER TABLE issues.statuses
    DROP CONSTRAINT IF EXISTS statuses_wip_limit_chk,
    DROP CONSTRAINT IF EXISTS statuses_color_chk,
    DROP COLUMN IF EXISTS hidden,
    DROP COLUMN IF EXISTS wip_limit,
    DROP COLUMN IF EXISTS color;

DROP INDEX IF EXISTS issues.issues_project_priority_rank_idx;
DROP INDEX IF EXISTS issues.issues_project_updated_rank_idx;
DROP FUNCTION IF EXISTS issues.search_fold(TEXT);

DELETE FROM issues.issue_events WHERE kind IN ('discarded', 'restored');
ALTER TABLE issues.issue_events DROP CONSTRAINT issue_events_kind_chk;
ALTER TABLE issues.issue_events ADD CONSTRAINT issue_events_kind_chk CHECK (kind IN ('created',
    'status_changed', 'title_changed', 'description_changed', 'priority_changed', 'labels_changed',
    'commented'));

ALTER TABLE issues.issues
    DROP CONSTRAINT IF EXISTS issues_discard_reason_not_empty_chk,
    DROP CONSTRAINT IF EXISTS issues_discard_canceled_chk,
    DROP CONSTRAINT IF EXISTS issues_discard_all_or_none_chk,
    DROP COLUMN IF EXISTS discarded_at,
    DROP COLUMN IF EXISTS discarded_by,
    DROP COLUMN IF EXISTS discard_reason;
