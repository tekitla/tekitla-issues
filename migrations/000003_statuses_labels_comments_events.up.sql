-- ============================================================
-- Rebanada 3a (issue 002): estados configurables, etiquetas, comentarios e
-- historial de cada issue.
-- ============================================================
-- Migración con backfill: los proyectos existentes reciben sus cuatro estados
-- (uno por categoría, mismos nombres que crea el servicio) y cada issue el
-- estado de su categoría. Sin BEGIN/COMMIT propios: scripts/apply_migration.sh
-- envuelve el archivo en una transacción (y lo prueba en seco con ROLLBACK).
-- PKs UUID: el servicio las genera; el backfill usa gen_random_uuid()
-- (Postgres 13+).


-- ------------------------------------------------------------
-- statuses: columnas del tablero de cada proyecto
-- ------------------------------------------------------------
CREATE TABLE issues.statuses (
    id         UUID        NOT NULL,
    project_id UUID        NOT NULL REFERENCES issues.projects (id) ON DELETE RESTRICT,
    name       TEXT        NOT NULL,
    -- Categoría fija (enum IssueState); no cambia después de creado.
    category   TEXT        NOT NULL,
    -- Orden de las columnas (fractional indexing, como issues.rank).
    rank       TEXT        COLLATE "C" NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT statuses_project_id_rank_key UNIQUE (project_id, rank),
    -- Destino de la FK compuesta de issues: el estado es del mismo proyecto y
    -- issues.state es siempre su categoría.
    CONSTRAINT statuses_id_project_id_category_key UNIQUE (id, project_id, category),
    CONSTRAINT statuses_category_chk CHECK (category IN ('todo', 'in_progress', 'done', 'canceled')),
    CONSTRAINT statuses_name_not_empty_chk CHECK (name <> ''),
    CONSTRAINT statuses_rank_not_empty_chk CHECK (rank <> '')
);

-- Nombre único por proyecto sin distinguir mayúsculas.
CREATE UNIQUE INDEX statuses_project_id_lower_name_key ON issues.statuses (project_id, lower(name));

INSERT INTO issues.statuses (id, project_id, name, category, rank, created_at, updated_at)
SELECT gen_random_uuid(), p.id, d.name, d.category, d.rank, p.created_at, p.created_at
  FROM issues.projects p
 CROSS JOIN (VALUES ('Por hacer',  'todo',        'a0'),
                    ('En curso',   'in_progress', 'a1'),
                    ('Hecho',      'done',        'a2'),
                    ('Descartado', 'canceled',    'a3')) AS d (name, category, rank);

ALTER TABLE issues.issues ADD COLUMN status_id UUID;

UPDATE issues.issues i
   SET status_id = s.id
  FROM issues.statuses s
 WHERE s.project_id = i.project_id
   AND s.category = i.state;

ALTER TABLE issues.issues
    ALTER COLUMN status_id SET NOT NULL,
    ADD CONSTRAINT issues_status_fkey FOREIGN KEY (status_id, project_id, state)
        REFERENCES issues.statuses (id, project_id, category) ON DELETE RESTRICT;

-- Soporta mover issues al borrar un estado y la FK de arriba.
CREATE INDEX issues_status_id_idx ON issues.issues (status_id);

-- ------------------------------------------------------------
-- labels + issue_labels
-- ------------------------------------------------------------
CREATE TABLE issues.labels (
    id         UUID        NOT NULL,
    project_id UUID        NOT NULL REFERENCES issues.projects (id) ON DELETE RESTRICT,
    name       TEXT        NOT NULL,
    -- 1 neutral, 2 info, 3 success, 4 warning, 5 danger, 6 accent, 7 brand
    -- (mismo orden que el enum LabelColor).
    color      SMALLINT    NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT labels_color_chk CHECK (color BETWEEN 1 AND 7),
    CONSTRAINT labels_name_not_empty_chk CHECK (name <> '')
);

CREATE UNIQUE INDEX labels_project_id_lower_name_key ON issues.labels (project_id, lower(name));

CREATE TABLE issues.issue_labels (
    issue_id UUID NOT NULL REFERENCES issues.issues (id) ON DELETE CASCADE,
    -- Borrar una etiqueta la quita de todos los issues.
    label_id UUID NOT NULL REFERENCES issues.labels (id) ON DELETE CASCADE,
    PRIMARY KEY (issue_id, label_id)
);

CREATE INDEX issue_labels_label_id_idx ON issues.issue_labels (label_id);

-- ------------------------------------------------------------
-- comments
-- ------------------------------------------------------------
CREATE TABLE issues.comments (
    id         UUID        NOT NULL,
    issue_id   UUID        NOT NULL REFERENCES issues.issues (id) ON DELETE RESTRICT,
    -- uid de quien lo escribió (x-user-id del gateway).
    author_uid TEXT        NOT NULL,
    body       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT comments_author_uid_not_empty_chk CHECK (author_uid <> ''),
    CONSTRAINT comments_body_not_empty_chk CHECK (body <> '')
);

-- ------------------------------------------------------------
-- issue_events: historial de cada issue
-- ------------------------------------------------------------
CREATE TABLE issues.issue_events (
    id         UUID        NOT NULL,
    -- Orden estable del historial (y cursor de ListIssueEvents): dos eventos
    -- de la misma transacción comparten created_at.
    seq        BIGINT      GENERATED ALWAYS AS IDENTITY,
    issue_id   UUID        NOT NULL REFERENCES issues.issues (id) ON DELETE RESTRICT,
    actor_uid  TEXT        NOT NULL,
    kind       TEXT        NOT NULL,
    -- Detalle del cambio según kind (ver app/infra/postgres/event_repo.go).
    data       JSONB       NOT NULL,
    comment_id UUID        REFERENCES issues.comments (id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT issue_events_seq_key UNIQUE (seq),
    CONSTRAINT issue_events_kind_chk CHECK (kind IN ('created', 'status_changed', 'title_changed',
        'description_changed', 'priority_changed', 'labels_changed', 'commented')),
    CONSTRAINT issue_events_comment_chk CHECK ((kind = 'commented') = (comment_id IS NOT NULL)),
    CONSTRAINT issue_events_actor_uid_not_empty_chk CHECK (actor_uid <> '')
);

-- Soporta ListIssueEvents: WHERE issue_id = ? AND seq > ? ORDER BY seq.
CREATE INDEX issue_events_issue_id_seq_idx ON issues.issue_events (issue_id, seq);

-- Los issues existentes arrancan su historial con «creado», a nombre del
-- dueño del espacio (en la rebanada 1, el único que pudo crearlos).
INSERT INTO issues.issue_events (id, issue_id, actor_uid, kind, data, created_at)
SELECT gen_random_uuid(), i.id, w.owner_uid, 'created', '{}'::jsonb, i.created_at
  FROM issues.issues i
  JOIN issues.projects p ON p.id = i.project_id
  JOIN issues.workspaces w ON w.id = p.workspace_id
 ORDER BY i.created_at, i.number;
