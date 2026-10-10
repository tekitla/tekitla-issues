-- ============================================================
-- Proyectos e issues (issue 002, rebanada 1)
-- ============================================================
-- Un proyecto pertenece a un único dueño (owner_uid, el uid que propaga el
-- api-gateway en la metadata x-user-id). Sin organizaciones ni miembros todavía.
-- PKs UUID generadas en el service. Sin DEFAULT en columnas de negocio: el
-- service siempre setea estado, prioridad y next_issue_number.

-- ------------------------------------------------------------
-- projects
-- ------------------------------------------------------------
CREATE TABLE issues.projects (
    id                UUID        NOT NULL,
    owner_uid         TEXT        NOT NULL,
    key               TEXT        NOT NULL,
    name              TEXT        NOT NULL,
    description       TEXT        NOT NULL,
    -- Número que recibirá el próximo issue. Lo incrementa CreateIssue con
    -- UPDATE ... RETURNING en la misma transacción del INSERT (lock de fila).
    next_issue_number INTEGER     NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT projects_owner_uid_key_key UNIQUE (owner_uid, key),
    CONSTRAINT projects_key_format_chk CHECK (key ~ '^[A-Z][A-Z0-9]{1,9}$'),
    CONSTRAINT projects_owner_uid_not_empty_chk CHECK (owner_uid <> ''),
    CONSTRAINT projects_next_issue_number_chk CHECK (next_issue_number >= 1)
);

-- Soporta ListProjects: WHERE owner_uid = ? ORDER BY name, id.
CREATE INDEX projects_owner_uid_name_id_idx ON issues.projects (owner_uid, name, id);

-- ------------------------------------------------------------
-- issues
-- ------------------------------------------------------------
CREATE TABLE issues.issues (
    id          UUID        NOT NULL,
    project_id  UUID        NOT NULL REFERENCES issues.projects (id) ON DELETE RESTRICT,
    number      INTEGER     NOT NULL,
    title       TEXT        NOT NULL,
    description TEXT        NOT NULL,
    state       TEXT        NOT NULL,
    priority    SMALLINT    NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT issues_project_id_number_key UNIQUE (project_id, number),
    CONSTRAINT issues_number_chk CHECK (number >= 1),
    CONSTRAINT issues_state_chk CHECK (state IN ('todo', 'in_progress', 'done')),
    -- 1 low, 2 medium, 3 high, 4 urgent (mismo orden que el enum Priority).
    CONSTRAINT issues_priority_chk CHECK (priority BETWEEN 1 AND 4)
);

-- Soportan el keyset de ListIssues: ORDER BY priority DESC, created_at DESC, id DESC,
-- sin filtro de estado y con filtro de estado (tablero por columna).
CREATE INDEX issues_project_order_idx
    ON issues.issues (project_id, priority DESC, created_at DESC, id DESC);
CREATE INDEX issues_project_state_order_idx
    ON issues.issues (project_id, state, priority DESC, created_at DESC, id DESC);
