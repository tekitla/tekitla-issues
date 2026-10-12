-- ============================================================
-- Issues 011 a 013 (tekitla-protos v0.4.0): descartar con motivo, búsqueda
-- y orden en la lista, y color, límite WIP y columnas ocultas en los estados.
-- ============================================================
-- Migración con backfill: cada estado existente recibe el color de su
-- categoría, sin límite WIP, y los CANCELED quedan ocultos (como nacen desde
-- ahora). Sin BEGIN/COMMIT propios: scripts/apply_migration.sh envuelve el
-- archivo en una transacción (y lo prueba en seco con ROLLBACK).
-- Compatible con el binario anterior: solo agrega columnas con valor y
-- amplía el CHECK de issue_events.kind.


-- ------------------------------------------------------------
-- 011: motivo del descarte (Issue.discard)
-- ------------------------------------------------------------
-- Las tres juntas o ninguna; solo en un issue CANCELED. Las pone
-- DiscardIssue y se borran cuando el issue sale de CANCELED.
ALTER TABLE issues.issues
    ADD COLUMN discard_reason TEXT,
    -- uid de quien lo descartó (x-user-id del gateway).
    ADD COLUMN discarded_by   TEXT,
    ADD COLUMN discarded_at   TIMESTAMPTZ,
    ADD CONSTRAINT issues_discard_all_or_none_chk CHECK (
        (discard_reason IS NULL) = (discarded_by IS NULL)
        AND (discard_reason IS NULL) = (discarded_at IS NULL)),
    ADD CONSTRAINT issues_discard_canceled_chk CHECK (discard_reason IS NULL OR state = 'canceled'),
    ADD CONSTRAINT issues_discard_reason_not_empty_chk CHECK (discard_reason <> '');

ALTER TABLE issues.issue_events DROP CONSTRAINT issue_events_kind_chk;
ALTER TABLE issues.issue_events ADD CONSTRAINT issue_events_kind_chk CHECK (kind IN ('created',
    'status_changed', 'title_changed', 'description_changed', 'priority_changed', 'labels_changed',
    'commented', 'discarded', 'restored'));

-- ------------------------------------------------------------
-- 012: búsqueda y orden de ListIssues
-- ------------------------------------------------------------
-- Texto para comparar sin mayúsculas ni acentos: quita los acentos del
-- español, portugués y francés y pasa a minúsculas. translate() antes de
-- lower() para no depender de la configuración regional de la base. La
-- búsqueda aplica la misma función al título y al texto buscado.
CREATE FUNCTION issues.search_fold(t TEXT) RETURNS TEXT
    LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
    RETURN lower(translate(t,
        'ÁÀÂÄÃÅáàâäãåÉÈÊËéèêëÍÌÎÏíìîïÓÒÔÖÕóòôöõÚÙÛÜúùûüÑñÇçÝýÿ',
        'AAAAAAaaaaaaEEEEeeeeIIIIiiiiOOOOOoooooUUUUuuuuNnCcYyy'));

-- Órdenes de ListIssues además de rank (empates por rank).
CREATE INDEX issues_project_updated_rank_idx ON issues.issues (project_id, updated_at DESC, rank);
CREATE INDEX issues_project_priority_rank_idx ON issues.issues (project_id, priority DESC, rank);

-- ------------------------------------------------------------
-- 013: color, límite WIP y visibilidad de los estados
-- ------------------------------------------------------------
-- color: mismo enum que labels.color (1 neutral ... 7 brand).
ALTER TABLE issues.statuses
    ADD COLUMN color     SMALLINT,
    ADD COLUMN wip_limit INTEGER,
    ADD COLUMN hidden    BOOLEAN;

-- El tono de cada categoría (model.DefaultStatusColor): todo neutral,
-- in_progress info, done success, canceled danger.
UPDATE issues.statuses
   SET color = CASE category
                   WHEN 'todo'        THEN 1
                   WHEN 'in_progress' THEN 2
                   WHEN 'done'        THEN 3
                   WHEN 'canceled'    THEN 5
               END,
       wip_limit = 0,
       hidden = (category = 'canceled');

-- Sin DEFAULT: el servicio siempre los escribe.
ALTER TABLE issues.statuses
    ALTER COLUMN color SET NOT NULL,
    ALTER COLUMN wip_limit SET NOT NULL,
    ALTER COLUMN hidden SET NOT NULL,
    ADD CONSTRAINT statuses_color_chk CHECK (color BETWEEN 1 AND 7),
    -- 0 = sin límite.
    ADD CONSTRAINT statuses_wip_limit_chk CHECK (wip_limit >= 0);
