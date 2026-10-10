-- ============================================================
-- Espacios de trabajo, proyectos e issues (issue 002, rebanada 1)
-- ============================================================
-- Todo proyecto vive en un espacio de trabajo. En la rebanada 1 cada usuario
-- tiene solo su espacio personal (owner_uid, el uid que propaga el api-gateway
-- en la metadata x-user-id), creado en su primer acceso. Desde la rebanada 3
-- un espacio es un tenant de accounts-service (issue 005) y el personal pasa a
-- ser un tenant personal con el mismo id.
-- PKs UUID generadas en el service. Sin DEFAULT en columnas de negocio: el
-- service siempre setea estado, prioridad, rank y next_issue_number.

-- ------------------------------------------------------------
-- workspaces
-- ------------------------------------------------------------
CREATE TABLE issues.workspaces (
    id         UUID        NOT NULL,
    -- Dueño del espacio personal: uno por usuario.
    owner_uid  TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT workspaces_owner_uid_key UNIQUE (owner_uid),
    CONSTRAINT workspaces_owner_uid_not_empty_chk CHECK (owner_uid <> '')
);

-- ------------------------------------------------------------
-- projects
-- ------------------------------------------------------------
CREATE TABLE issues.projects (
    id                UUID        NOT NULL,
    workspace_id      UUID        NOT NULL REFERENCES issues.workspaces (id) ON DELETE RESTRICT,
    key               TEXT        NOT NULL,
    name              TEXT        NOT NULL,
    description       TEXT        NOT NULL,
    -- Número que recibirá el próximo issue. Lo incrementa CreateIssue con
    -- UPDATE ... RETURNING en la misma transacción del INSERT (lock de fila).
    next_issue_number INTEGER     NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT projects_workspace_id_key_key UNIQUE (workspace_id, key),
    CONSTRAINT projects_key_format_chk CHECK (key ~ '^[A-Z][A-Z0-9]{1,9}$'),
    CONSTRAINT projects_next_issue_number_chk CHECK (next_issue_number >= 1)
);

-- Soporta ListProjects: WHERE workspace_id IN (...) ORDER BY name, id.
CREATE INDEX projects_workspace_id_name_id_idx ON issues.projects (workspace_id, name, id);

-- ------------------------------------------------------------
-- issues
-- ------------------------------------------------------------
CREATE TABLE issues.issues (
    id          UUID        NOT NULL,
    project_id  UUID        NOT NULL REFERENCES issues.projects (id) ON DELETE RESTRICT,
    number      INTEGER     NOT NULL,
    title       TEXT        NOT NULL,
    description TEXT        NOT NULL,
    -- Categoría del estado (enum IssueState).
    state       TEXT        NOT NULL,
    priority    SMALLINT    NOT NULL,
    -- Orden manual del proyecto (fractional indexing, ver model/rank.go).
    -- COLLATE "C": se compara byte a byte, igual que en Go; con la collation
    -- del cluster ('en_US.UTF-8', etc.) mayúsculas y minúsculas se mezclan.
    rank        TEXT        COLLATE "C" NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id),
    CONSTRAINT issues_project_id_number_key UNIQUE (project_id, number),
    -- También soporta el keyset de ListIssues sin filtro de estado:
    -- WHERE project_id = ? AND rank > ? ORDER BY rank.
    CONSTRAINT issues_project_id_rank_key UNIQUE (project_id, rank),
    CONSTRAINT issues_number_chk CHECK (number >= 1),
    CONSTRAINT issues_state_chk CHECK (state IN ('todo', 'in_progress', 'done', 'canceled')),
    -- 1 low, 2 medium, 3 high, 4 urgent (mismo orden que el enum Priority).
    CONSTRAINT issues_priority_chk CHECK (priority BETWEEN 1 AND 4),
    CONSTRAINT issues_rank_not_empty_chk CHECK (rank <> '')
);

-- Soporta el keyset de ListIssues con filtro de estado (tablero por columna).
CREATE INDEX issues_project_state_rank_idx ON issues.issues (project_id, state, rank);
