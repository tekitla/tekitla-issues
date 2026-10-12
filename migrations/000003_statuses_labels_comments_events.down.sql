-- Revierte 000003. Pierde estados propios, etiquetas, comentarios e historial;
-- issues.state (la categoría) queda como estaba.

DROP TABLE IF EXISTS issues.issue_events;
DROP TABLE IF EXISTS issues.comments;
DROP TABLE IF EXISTS issues.issue_labels;
DROP TABLE IF EXISTS issues.labels;

ALTER TABLE issues.issues DROP CONSTRAINT IF EXISTS issues_status_fkey;
DROP INDEX IF EXISTS issues.issues_status_id_idx;
ALTER TABLE issues.issues DROP COLUMN IF EXISTS status_id;

DROP TABLE IF EXISTS issues.statuses;

