-- 000006: fuente de los eventos del historial (issue 036, protos v0.6.0).
--
-- source: de dónde sale el evento. NULL = LIVE: todos los eventos
-- anteriores, sin DEFAULT que les cambie el significado. Los eventos nuevos
-- llevan el valor explícito. Los de ImportIssue anteriores a v0.6.0 quedan
-- NULL (LIVE); marcarlos import_sync en un proyecto espejo es un paso aparte
-- y revisable: scripts/backfill_import_sync.sh.
--
-- source_ref: referencia en el origen de un evento de ImportIssueEvents (p.
-- ej. el sha del commit). Única por issue: es la idempotencia del RPC.
--
-- ListIssueEvents pasa a ordenar por (created_at, seq): un evento importado
-- aparece en su fecha del origen. El índice (issue_id, seq) de 000003 ya no
-- lo usa nadie y se reemplaza.
--
-- Compatible con el binario de v0.5.0: columnas nullable que no lee.

ALTER TABLE issues.issue_events
    ADD COLUMN source TEXT,
    ADD COLUMN source_ref TEXT;

ALTER TABLE issues.issue_events
    ADD CONSTRAINT issue_events_source_chk
        CHECK (source IN ('live', 'import_history', 'import_file', 'import_sync')),
    -- source_ref va si y solo si el evento vino de ImportIssueEvents.
    ADD CONSTRAINT issue_events_source_ref_chk
        CHECK ((source_ref IS NOT NULL) = (COALESCE(source, 'live') IN ('import_history', 'import_file'))),
    ADD CONSTRAINT issue_events_source_ref_len_chk
        CHECK (source_ref IS NULL OR (source_ref <> '' AND char_length(source_ref) <= 200));

CREATE UNIQUE INDEX issue_events_issue_id_source_ref_key
    ON issues.issue_events (issue_id, source_ref) WHERE source_ref IS NOT NULL;

CREATE INDEX issue_events_issue_id_created_at_seq_idx
    ON issues.issue_events (issue_id, created_at, seq);

DROP INDEX IF EXISTS issues.issue_events_issue_id_seq_idx;
