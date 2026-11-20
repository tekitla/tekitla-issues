-- ============================================================
-- Issue 016: integración con GitHub y GitLab (conexiones, repos, links de
-- ramas y PRs a issues, y entregas de webhooks).
-- ============================================================
-- Solo crea tablas nuevas: ningún dato existente cambia de significado y el
-- binario anterior no las lee ni las escribe, así que se puede aplicar antes
-- del deploy. Los DEFAULT son de tablas nuevas. Sin BEGIN/COMMIT propios:
-- scripts/apply_migration.sh envuelve el archivo en una transacción.
--
-- Ningún secreto en claro: la llave de la GitHub App y los tokens de GitLab
-- viven en variables de entorno (credential_ref es solo el nombre); del
-- secreto de webhook de GitLab se guarda su SHA-256.
--
-- Número: lo toma quien publique primero entre el 016 y el 036 (000010 u
-- 000011). Como solo crea tablas, renumerar es renombrar los archivos.

CREATE TABLE issues.repo_connections (
    id                  UUID        NOT NULL,
    project_id          UUID        NOT NULL REFERENCES issues.projects (id) ON DELETE RESTRICT,
    -- 'github' | 'gitlab'.
    provider            TEXT        NOT NULL,
    -- github.com, gitlab.com o el host de la instancia.
    host                TEXT        NOT NULL,
    -- Org de GitHub o ruta del grupo de GitLab.
    owner               TEXT        NOT NULL,
    -- GitHub: id de la instalación de la App.
    installation_id     BIGINT,
    -- GitLab: nombre de la variable de entorno con el token, nunca el token.
    credential_ref      TEXT,
    -- GitLab con webhook: SHA-256 del secreto tkw_. Nunca sale del repo.
    webhook_secret_hash BYTEA,
    -- GitLab: 'webhook' | 'poll' (plan §3.2).
    sync_mode           TEXT,
    poll_interval_min   INTEGER,
    last_polled_at      TIMESTAMPTZ,
    -- 'all' | 'topic' | 'selected'.
    scope               TEXT        NOT NULL,
    topic               TEXT,
    selected            TEXT[]      NOT NULL DEFAULT '{}',
    created_by_uid      TEXT        NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL,
    last_delivery_at    TIMESTAMPTZ,
    PRIMARY KEY (id),
    CONSTRAINT repo_connections_owner_key UNIQUE (provider, host, owner),
    CONSTRAINT repo_connections_hash_key UNIQUE (webhook_secret_hash),
    CONSTRAINT repo_connections_provider_chk CHECK (provider IN ('github', 'gitlab')),
    -- COALESCE(…, false): una comparación con NULL da NULL, y un CHECK con
    -- NULL pasa. Así una columna que falta hace fallar la restricción.
    CONSTRAINT repo_connections_scope_chk CHECK (COALESCE(
        (scope = 'all' AND topic IS NULL)
        OR (scope = 'topic' AND topic ~ '^[a-z0-9][a-z0-9-]{0,49}$')
        OR (scope = 'selected' AND topic IS NULL AND cardinality(selected) BETWEEN 1 AND 300), false)),
    CONSTRAINT repo_connections_cred_chk CHECK (COALESCE(
        (provider = 'github' AND installation_id > 0 AND credential_ref IS NULL
            AND webhook_secret_hash IS NULL AND sync_mode IS NULL AND poll_interval_min IS NULL)
        OR (provider = 'gitlab' AND installation_id IS NULL
            AND credential_ref ~ '^TEKITLA_GITLAB_TOKEN_[A-Z0-9_]{1,40}$'
            AND ((sync_mode = 'webhook' AND octet_length(webhook_secret_hash) = 32 AND poll_interval_min IS NULL)
              OR (sync_mode = 'poll' AND webhook_secret_hash IS NULL AND poll_interval_min BETWEEN 2 AND 60))), false))
);

CREATE INDEX repo_connections_project_idx ON issues.repo_connections (project_id, created_at);

CREATE TABLE issues.repositories (
    id             UUID   NOT NULL,
    connection_id  UUID   NOT NULL REFERENCES issues.repo_connections (id) ON DELETE CASCADE,
    project_id     UUID   NOT NULL REFERENCES issues.projects (id) ON DELETE RESTRICT,
    -- Id del repo en el proveedor: sobrevive a renombres.
    external_id    BIGINT NOT NULL,
    owner          TEXT   NOT NULL,
    name           TEXT   NOT NULL,
    default_branch TEXT   NOT NULL,
    -- 'code' | 'docs' (docs: issue 017).
    kind           TEXT   NOT NULL DEFAULT 'code',
    PRIMARY KEY (id),
    CONSTRAINT repositories_external_key UNIQUE (connection_id, external_id),
    CONSTRAINT repositories_kind_chk CHECK (kind IN ('code', 'docs')),
    CONSTRAINT repositories_external_chk CHECK (external_id > 0)
);

CREATE INDEX repositories_project_idx ON issues.repositories (project_id, owner, name);

CREATE TABLE issues.issue_links (
    id               UUID        NOT NULL,
    issue_id         UUID        NOT NULL REFERENCES issues.issues (id) ON DELETE CASCADE,
    repository_id    UUID        NOT NULL REFERENCES issues.repositories (id) ON DELETE CASCADE,
    branch           TEXT        NOT NULL,
    -- Último commit conocido de la rama; NULL si todavía no se vio un push.
    head_sha         TEXT,
    -- Primera línea del commit (decisión 7 de Victor).
    head_title       TEXT        NOT NULL DEFAULT '',
    commits          INTEGER     NOT NULL DEFAULT 0,
    -- NULL: sin PR.
    pr_number        INTEGER,
    pr_title         TEXT        NOT NULL DEFAULT '',
    -- 'none' | 'draft' | 'open' | 'merged' | 'closed'.
    pr_state         TEXT        NOT NULL,
    -- updated_at del PR en el proveedor: un evento más viejo no lo pisa.
    pr_updated_at    TIMESTAMPTZ,
    review_requested BOOLEAN     NOT NULL DEFAULT false,
    -- Agregado de ci_suites: 'none' | 'pending' | 'success' | 'failed' | 'canceled'.
    ci_status        TEXT        NOT NULL,
    -- Estado de cada check suite (o pipeline) del head: {"<id>": "<estado>"}.
    ci_suites        JSONB       NOT NULL DEFAULT '{}',
    -- 'branch_name' | 'pr_text' | 'manual'.
    origin           TEXT        NOT NULL,
    -- Quitado a mano: un evento nuevo de la rama no lo recrea.
    suppressed       BOOLEAN     NOT NULL DEFAULT false,
    branch_deleted   BOOLEAN     NOT NULL DEFAULT false,
    -- Login del proveedor (nunca un email ni un uid).
    updated_by       TEXT        NOT NULL DEFAULT '',
    updated_at       TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (id),
    CONSTRAINT issue_links_key UNIQUE (issue_id, repository_id, branch),
    CONSTRAINT issue_links_branch_chk CHECK (char_length(branch) BETWEEN 1 AND 255),
    CONSTRAINT issue_links_sha_chk CHECK (head_sha IS NULL OR head_sha ~ '^[0-9a-f]{40}$'),
    CONSTRAINT issue_links_head_title_chk CHECK (char_length(head_title) <= 120),
    CONSTRAINT issue_links_commits_chk CHECK (commits >= 0),
    CONSTRAINT issue_links_pr_number_chk CHECK (pr_number IS NULL OR pr_number > 0),
    CONSTRAINT issue_links_pr_title_chk CHECK (char_length(pr_title) <= 200),
    CONSTRAINT issue_links_pr_state_chk CHECK (pr_state IN ('none', 'draft', 'open', 'merged', 'closed')),
    CONSTRAINT issue_links_pr_chk CHECK ((pr_state = 'none') = (pr_number IS NULL)),
    CONSTRAINT issue_links_ci_chk CHECK (ci_status IN ('none', 'pending', 'success', 'failed', 'canceled')),
    CONSTRAINT issue_links_origin_chk CHECK (origin IN ('branch_name', 'pr_text', 'manual')),
    CONSTRAINT issue_links_updated_by_chk CHECK (char_length(updated_by) <= 100)
);

-- Eventos de una rama y del CI de un commit.
CREATE INDEX issue_links_repo_branch_idx ON issues.issue_links (repository_id, branch);
CREATE INDEX issue_links_repo_sha_idx ON issues.issue_links (repository_id, head_sha);

CREATE TABLE issues.webhook_deliveries (
    provider    TEXT        NOT NULL,
    -- X-GitHub-Delivery / X-Gitlab-Event-UUID.
    delivery_id TEXT        NOT NULL,
    event       TEXT        NOT NULL,
    received_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (provider, delivery_id),
    CONSTRAINT webhook_deliveries_provider_chk CHECK (provider IN ('github', 'gitlab')),
    CONSTRAINT webhook_deliveries_id_chk CHECK (char_length(delivery_id) BETWEEN 1 AND 100)
);

-- Borrado de las entregas de más de 30 días.
CREATE INDEX webhook_deliveries_received_idx ON issues.webhook_deliveries (received_at);
