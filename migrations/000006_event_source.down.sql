-- Revierte 000006. Pierde la fuente y la referencia de cada evento. Los
-- eventos importados con ImportIssueEvents se conservan como status_changed
-- comunes, con su fecha del origen (el binario de v0.5.0 los lee: los ids
-- de estado vacíos son texto).

CREATE INDEX IF NOT EXISTS issue_events_issue_id_seq_idx ON issues.issue_events (issue_id, seq);
DROP INDEX IF EXISTS issues.issue_events_issue_id_created_at_seq_idx;
DROP INDEX IF EXISTS issues.issue_events_issue_id_source_ref_key;

ALTER TABLE issues.issue_events
    DROP CONSTRAINT IF EXISTS issue_events_source_ref_len_chk,
    DROP CONSTRAINT IF EXISTS issue_events_source_ref_chk,
    DROP CONSTRAINT IF EXISTS issue_events_source_chk,
    DROP COLUMN IF EXISTS source_ref,
    DROP COLUMN IF EXISTS source;
