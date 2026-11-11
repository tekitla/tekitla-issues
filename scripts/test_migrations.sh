#!/usr/bin/env bash
# =============================================================================
# Prueba de las migraciones contra un Postgres 16 descartable (docker)
# =============================================================================
# Aplica migrations/*.up.sql en orden y, antes de cada migración con backfill,
# carga datos de la versión anterior para verificar que el backfill los deja
# bien. Al final prueba down -> up de cada una que lo pide (hoy 000005 y 000006). No toca ninguna base real:
# levanta un contenedor propio y lo borra al terminar.
#
#   scripts/test_migrations.sh        (o `make test-migrations`)
#
# Sale con 1 en la primera verificación que falla.
# =============================================================================
set -euo pipefail

cd "$(dirname "$0")/.."
NAME="issues-migtest-$$"
IMAGE="${PG_IMAGE:-postgres:16}"

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker run -d --name "$NAME" -e POSTGRES_PASSWORD=postgres "$IMAGE" >/dev/null
docker cp migrations "$NAME:/migrations" >/dev/null
for _ in $(seq 1 60); do
  # pg_isready contesta antes de que termine el init: se espera a una consulta real.
  docker exec "$NAME" psql -U postgres -tAc 'SELECT 1' >/dev/null 2>&1 && break
  sleep 1
done

# sql ejecuta SQL (stdin o $1) y devuelve el resultado sin formato.
sql() {
  if [ $# -gt 0 ]; then
    docker exec -i "$NAME" psql -U postgres -v ON_ERROR_STOP=1 -X -q -tA -c "$1"
  else
    docker exec -i "$NAME" psql -U postgres -v ON_ERROR_STOP=1 -X -q -tA
  fi
}
apply() { echo "== $1"; docker exec "$NAME" psql -U postgres -v ON_ERROR_STOP=1 -X -q -f "/migrations/$1.sql"; }
fails=0
check() { # check <descripción> <esperado> <consulta>
  local got
  got="$(sql "$3" 2>&1 | tr -d '\r')"
  if [ "$got" = "$2" ]; then echo "ok   $1"; else echo "FAIL $1: esperaba [$2], obtuve [$got]"; fails=1; fi
}
must_fail() { # must_fail <descripción> <sentencia>
  if sql "$2" >/dev/null 2>&1; then echo "FAIL $1: debería haber fallado"; fails=1; else echo "ok   $1 (rechazado)"; fi
}

apply 000001_create_issues_schema.up
apply 000002_create_projects_and_issues.up

echo "== datos de la rebanada 1"
sql <<'SQL'
INSERT INTO issues.workspaces (id, owner_uid) VALUES ('00000000-0000-4000-8000-000000000001', 'u1');
INSERT INTO issues.projects (id, workspace_id, key, name, description, next_issue_number)
VALUES ('00000000-0000-4000-8000-0000000000a1', '00000000-0000-4000-8000-000000000001', 'TK', 'Tekitla', '', 5),
       ('00000000-0000-4000-8000-0000000000a2', '00000000-0000-4000-8000-000000000001', 'OT', 'Otro', '', 2);
INSERT INTO issues.issues (id, project_id, number, title, description, state, priority, rank)
VALUES (gen_random_uuid(), '00000000-0000-4000-8000-0000000000a1', 1, 'Árbol de Ñandú', '', 'todo', 2, 'a0'),
       (gen_random_uuid(), '00000000-0000-4000-8000-0000000000a1', 2, 'En curso', '', 'in_progress', 3, 'a1'),
       (gen_random_uuid(), '00000000-0000-4000-8000-0000000000a1', 3, 'Hecho', '', 'done', 1, 'a2'),
       (gen_random_uuid(), '00000000-0000-4000-8000-0000000000a1', 4, 'Descartado', '', 'canceled', 4, 'a3'),
       (gen_random_uuid(), '00000000-0000-4000-8000-0000000000a2', 1, 'Otro', '', 'canceled', 2, 'a0');
SQL

apply 000003_statuses_labels_comments_events.up

echo "== datos de la rebanada 3a: un estado propio por categoría"
sql <<'SQL'
INSERT INTO issues.statuses (id, project_id, name, category, rank)
VALUES (gen_random_uuid(), '00000000-0000-4000-8000-0000000000a1', 'En revisión', 'in_progress', 'a1V'),
       (gen_random_uuid(), '00000000-0000-4000-8000-0000000000a1', 'Archivado', 'canceled', 'a4');
SQL

apply 000004_discard_search_status_settings.up

echo "== backfill de 000004"
check "10 estados" "10" "SELECT count(*) FROM issues.statuses"
check "color por categoría" "0" "SELECT count(*) FROM issues.statuses WHERE color <> CASE category
  WHEN 'todo' THEN 1 WHEN 'in_progress' THEN 2 WHEN 'done' THEN 3 WHEN 'canceled' THEN 5 END"
check "CANCELED ocultos, el resto visibles" "0" "SELECT count(*) FROM issues.statuses WHERE hidden <> (category = 'canceled')"
check "sin límite WIP" "0" "SELECT count(*) FROM issues.statuses WHERE wip_limit <> 0"
check "issues sin motivo de descarte" "0" "SELECT count(*) FROM issues.issues WHERE discard_reason IS NOT NULL"
check "sin DEFAULT en las columnas nuevas" "0" "SELECT count(*) FROM information_schema.columns
  WHERE table_schema = 'issues' AND table_name = 'statuses' AND column_name IN ('color', 'wip_limit', 'hidden')
    AND column_default IS NOT NULL"
check "search_fold sin acentos ni mayúsculas" "arbol de nandu" "SELECT issues.search_fold('Árbol de ÑANDÚ')"
check "búsqueda por título sin acentos" "1" "SELECT number FROM issues.issues
  WHERE issues.search_fold(title) LIKE '%' || issues.search_fold('ARBOL de nandu') || '%'"

echo "== restricciones de 000004"
must_fail "motivo en un issue que no es CANCELED" "UPDATE issues.issues SET discard_reason = 'x', discarded_by = 'u1', discarded_at = now()
  WHERE number = 1 AND project_id = '00000000-0000-4000-8000-0000000000a1'"
must_fail "motivo sin autor ni fecha" "UPDATE issues.issues SET discard_reason = 'x' WHERE number = 4 AND project_id = '00000000-0000-4000-8000-0000000000a1'"
must_fail "motivo vacío" "UPDATE issues.issues SET discard_reason = '', discarded_by = 'u1', discarded_at = now()
  WHERE number = 4 AND project_id = '00000000-0000-4000-8000-0000000000a1'"
must_fail "color fuera de rango" "UPDATE issues.statuses SET color = 8"
must_fail "límite WIP negativo" "UPDATE issues.statuses SET wip_limit = -1"
sql "UPDATE issues.issues SET discard_reason = 'duplicado', discarded_by = 'u1', discarded_at = now()
  WHERE number = 4 AND project_id = '00000000-0000-4000-8000-0000000000a1'"
sql "INSERT INTO issues.issue_events (id, issue_id, actor_uid, kind, data)
  SELECT gen_random_uuid(), id, 'u1', 'discarded',
         '{\"from_id\":\"x\",\"from_name\":\"Por hacer\",\"from_category\":\"todo\",\"to_id\":\"y\",\"to_name\":\"Descartado\",\"to_category\":\"canceled\",\"reason\":\"duplicado\"}'::jsonb
    FROM issues.issues
   WHERE number = 4 AND project_id = '00000000-0000-4000-8000-0000000000a1'"
echo "ok   motivo completo en un CANCELED y evento 'discarded'"

apply 000004_discard_search_status_settings.down
check "down: sin columnas nuevas" "0" "SELECT count(*) FROM information_schema.columns
  WHERE table_schema = 'issues' AND ((table_name = 'issues' AND column_name LIKE 'discard%')
     OR (table_name = 'statuses' AND column_name IN ('color', 'wip_limit', 'hidden')))"
check "down: sin eventos discarded" "0" "SELECT count(*) FROM issues.issue_events WHERE kind IN ('discarded', 'restored')"
check "down: el descarte queda como status_changed" "Descartado" "SELECT data->>'to_name' FROM issues.issue_events WHERE kind = 'status_changed' AND data->>'reason' = 'duplicado'"
check "down: el issue sigue descartado" "canceled" "SELECT state FROM issues.issues WHERE number = 4 AND project_id = '00000000-0000-4000-8000-0000000000a1'"

apply 000004_discard_search_status_settings.up
check "up de nuevo: backfill" "0" "SELECT count(*) FROM issues.statuses WHERE hidden <> (category = 'canceled')"

echo "== datos de 000004: una etiqueta puesta a mano"
sql <<'SQL'
INSERT INTO issues.labels (id, project_id, name, color)
VALUES ('00000000-0000-4000-8000-0000000000c1', '00000000-0000-4000-8000-0000000000a1', 'backend', 1);
INSERT INTO issues.issue_labels (issue_id, label_id)
SELECT id, '00000000-0000-4000-8000-0000000000c1' FROM issues.issues
 WHERE number = 1 AND project_id = '00000000-0000-4000-8000-0000000000a1';
SQL
EVENTS_BEFORE="$(sql "SELECT count(*) FROM issues.issue_events" | tr -d '\r')"

apply 000005_actors_tokens_assignee_import.up

echo "== 000005: los datos existentes no cambian de significado (CU-025-12)"
check "issues sin asignar" "0" "SELECT count(*) FROM issues.issues WHERE assignee_actor_id IS NOT NULL"
check "issues sin clave de origen" "0" "SELECT count(*) FROM issues.issues WHERE source_key IS NOT NULL"
check "eventos sin agente" "0" "SELECT count(*) FROM issues.issue_events WHERE agent_id IS NOT NULL"
check "eventos intactos" "$EVENTS_BEFORE" "SELECT count(*) FROM issues.issue_events"
check "etiquetas existentes = a mano" "1|0" "SELECT count(*), count(*) FILTER (WHERE imported) FROM issues.issue_labels"
check "sin DEFAULT en asignado, clave de origen y agente" "0" "SELECT count(*) FROM information_schema.columns
  WHERE table_schema = 'issues' AND column_default IS NOT NULL AND (
        (table_name = 'issues' AND column_name IN ('assignee_actor_id', 'source_key'))
     OR (table_name = 'issue_events' AND column_name = 'agent_id'))"
check "sin actores ni tokens" "0|0" "SELECT (SELECT count(*) FROM issues.actors), (SELECT count(*) FROM issues.tokens)"

echo "== restricciones de 000005"
P1=00000000-0000-4000-8000-0000000000d1
A1=00000000-0000-4000-8000-0000000000d2
TK=00000000-0000-4000-8000-0000000000a1
sql "INSERT INTO issues.actors (id, kind, user_uid, display_name, created_at) VALUES ('$P1', 'person', 'u1', '', now())"
sql "INSERT INTO issues.actors (id, kind, owner_actor_id, tool, display_name, created_at) VALUES ('$A1', 'agent', '$P1', 'codex', 'Codex PC-01', now())"
must_fail "persona sin uid" "INSERT INTO issues.actors (id, kind, display_name, created_at) VALUES (gen_random_uuid(), 'person', '', now())"
must_fail "uid repetido" "INSERT INTO issues.actors (id, kind, user_uid, display_name, created_at) VALUES (gen_random_uuid(), 'person', 'u1', '', now())"
must_fail "agente sin dueño" "INSERT INTO issues.actors (id, kind, tool, display_name, created_at) VALUES (gen_random_uuid(), 'agent', 'codex', 'x', now())"
must_fail "agente sin herramienta" "INSERT INTO issues.actors (id, kind, owner_actor_id, display_name, created_at) VALUES (gen_random_uuid(), 'agent', '$P1', 'x', now())"
must_fail "herramienta desconocida" "INSERT INTO issues.actors (id, kind, owner_actor_id, tool, display_name, created_at) VALUES (gen_random_uuid(), 'agent', '$P1', 'gpt', 'x', now())"
must_fail "agente sin nombre" "INSERT INTO issues.actors (id, kind, owner_actor_id, tool, display_name, created_at) VALUES (gen_random_uuid(), 'agent', '$P1', 'codex', '', now())"
must_fail "persona con uid y dueño" "INSERT INTO issues.actors (id, kind, user_uid, owner_actor_id, display_name, created_at) VALUES (gen_random_uuid(), 'person', 'u9', '$P1', '', now())"
must_fail "nombre de agente repetido (sin mayúsculas)" "INSERT INTO issues.actors (id, kind, owner_actor_id, tool, display_name, created_at) VALUES (gen_random_uuid(), 'agent', '$P1', 'cloud', 'codex pc-01', now())"
sql "UPDATE issues.actors SET revoked_at = now() WHERE id = '$A1'"
sql "INSERT INTO issues.actors (id, kind, owner_actor_id, tool, display_name, created_at) VALUES (gen_random_uuid(), 'agent', '$P1', 'codex', 'Codex PC-01', now())"
echo "ok   el nombre de un agente revocado se puede reusar"
HASH="decode(repeat('ab', 32), 'hex')"
OTHER="decode(repeat('cd', 32), 'hex')"
sql "INSERT INTO issues.tokens (id, actor_id, name, token_hash, scopes, created_at, expires_at)
  VALUES (gen_random_uuid(), '$A1', 'ci', $HASH, ARRAY['issues:read', 'issues:write'], now(), now() + interval '30 days')"
echo "ok   token con los dos scopes"
must_fail "hash repetido" "INSERT INTO issues.tokens (id, actor_id, name, token_hash, scopes, created_at, expires_at)
  VALUES (gen_random_uuid(), '$A1', '', $HASH, ARRAY['issues:read'], now(), now() + interval '1 day')"
must_fail "hash que no es SHA-256" "INSERT INTO issues.tokens (id, actor_id, name, token_hash, scopes, created_at, expires_at)
  VALUES (gen_random_uuid(), '$A1', '', decode('abcd', 'hex'), ARRAY['issues:read'], now(), now() + interval '1 day')"
must_fail "sin scopes" "INSERT INTO issues.tokens (id, actor_id, name, token_hash, scopes, created_at, expires_at)
  VALUES (gen_random_uuid(), '$A1', '', $OTHER, ARRAY[]::TEXT[], now(), now() + interval '1 day')"
must_fail "scope desconocido" "INSERT INTO issues.tokens (id, actor_id, name, token_hash, scopes, created_at, expires_at)
  VALUES (gen_random_uuid(), '$A1', '', $OTHER, ARRAY['issues:admin'], now(), now() + interval '1 day')"
must_fail "vence antes de crearse" "INSERT INTO issues.tokens (id, actor_id, name, token_hash, scopes, created_at, expires_at)
  VALUES (gen_random_uuid(), '$A1', '', $OTHER, ARRAY['issues:read'], now(), now())"
must_fail "asignado inexistente" "UPDATE issues.issues SET assignee_actor_id = gen_random_uuid() WHERE number = 1"
must_fail "agente de evento inexistente" "UPDATE issues.issue_events SET agent_id = gen_random_uuid()"
must_fail "clave de origen vacía" "UPDATE issues.issues SET source_key = '' WHERE number = 1"
sql "UPDATE issues.issues SET source_key = '001-a' WHERE number = 1 AND project_id = '$TK'"
must_fail "clave de origen repetida en el proyecto" "UPDATE issues.issues SET source_key = '001-a' WHERE number = 2 AND project_id = '$TK'"
sql "UPDATE issues.issues SET source_key = '001-a' WHERE project_id = '00000000-0000-4000-8000-0000000000a2'"
echo "ok   la misma clave de origen en otro proyecto"
sql "UPDATE issues.issues SET assignee_actor_id = '$A1' WHERE number = 1 AND project_id = '$TK'"
sql "INSERT INTO issues.issue_events (id, issue_id, actor_uid, agent_id, kind, data)
  SELECT gen_random_uuid(), id, 'u1', '$A1', 'assignee_changed', '{}'::jsonb
    FROM issues.issues WHERE number = 1 AND project_id = '$TK'"
sql "INSERT INTO issues.issue_labels (issue_id, label_id, imported)
  SELECT id, '00000000-0000-4000-8000-0000000000c1', true FROM issues.issues WHERE number = 2 AND project_id = '$TK'"
echo "ok   asignado, evento assignee_changed con agente y etiqueta importada"

apply 000005_actors_tokens_assignee_import.down
check "down: sin tablas nuevas" "0" "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'issues' AND table_name IN ('actors', 'tokens')"
check "down: sin columnas nuevas" "0" "SELECT count(*) FROM information_schema.columns WHERE table_schema = 'issues' AND (
        (table_name = 'issues' AND column_name IN ('assignee_actor_id', 'source_key'))
     OR (table_name = 'issue_events' AND column_name = 'agent_id')
     OR (table_name = 'issue_labels' AND column_name = 'imported'))"
check "down: sin eventos assignee_changed, el resto intacto" "$EVENTS_BEFORE" "SELECT count(*) FROM issues.issue_events"
check "down: las etiquetas importadas quedan como comunes" "2" "SELECT count(*) FROM issues.issue_labels"

apply 000005_actors_tokens_assignee_import.up
check "up de nuevo: issues sin asignar y etiquetas a mano" "0|0" "SELECT (SELECT count(*) FROM issues.issues WHERE assignee_actor_id IS NOT NULL),
  (SELECT count(*) FROM issues.issue_labels WHERE imported)"

EVENTS_BEFORE_6="$(sql "SELECT count(*) FROM issues.issue_events" | tr -d '\r')"
apply 000006_event_source.up

echo "== 000006: los eventos existentes no cambian de significado (NULL = LIVE)"
check "eventos sin fuente ni referencia" "$EVENTS_BEFORE_6|0|0" "SELECT count(*), count(source), count(source_ref) FROM issues.issue_events"
check "sin DEFAULT en source y source_ref" "0" "SELECT count(*) FROM information_schema.columns
  WHERE table_schema = 'issues' AND table_name = 'issue_events' AND column_name IN ('source', 'source_ref') AND column_default IS NOT NULL"
check "índice viejo reemplazado" "0|1" "SELECT count(*) FILTER (WHERE indexname = 'issue_events_issue_id_seq_idx'),
  count(*) FILTER (WHERE indexname = 'issue_events_issue_id_created_at_seq_idx') FROM pg_indexes WHERE schemaname = 'issues'"

echo "== restricciones de 000006"
ISSUE1="(SELECT id FROM issues.issues WHERE number = 1 AND project_id = '$TK')"
ISSUE2="(SELECT id FROM issues.issues WHERE number = 2 AND project_id = '$TK')"
ins() { echo "INSERT INTO issues.issue_events (id, issue_id, actor_uid, kind, data, source, source_ref)
  SELECT gen_random_uuid(), id, 'u1', 'status_changed', '{}'::jsonb, $2 FROM issues.issues WHERE id = $1"; }
must_fail "fuente desconocida" "$(ins "$ISSUE1" "'manual', NULL")"
must_fail "importado sin referencia" "$(ins "$ISSUE1" "'import_history', NULL")"
must_fail "en vivo con referencia" "$(ins "$ISSUE1" "'live', 'abc'")"
must_fail "sin fuente con referencia" "$(ins "$ISSUE1" "NULL, 'abc'")"
must_fail "sync con referencia" "$(ins "$ISSUE1" "'import_sync', 'abc'")"
must_fail "referencia vacía" "$(ins "$ISSUE1" "'import_file', ''")"
must_fail "referencia de más de 200" "$(ins "$ISSUE1" "'import_file', repeat('r', 201)")"
sql "$(ins "$ISSUE1" "'import_history', 'abc'")"
sql "$(ins "$ISSUE1" "'live', NULL")"
sql "$(ins "$ISSUE1" "'import_sync', NULL")"
echo "ok   import_history con referencia, live y import_sync sin referencia"
must_fail "referencia repetida en el issue" "$(ins "$ISSUE1" "'import_file', 'abc'")"
sql "$(ins "$ISSUE2" "'import_history', 'abc'")"
echo "ok   la misma referencia en otro issue"

apply 000006_event_source.down
check "down: sin columnas nuevas" "0" "SELECT count(*) FROM information_schema.columns
  WHERE table_schema = 'issues' AND table_name = 'issue_events' AND column_name IN ('source', 'source_ref')"
check "down: los eventos importados se conservan" "$((EVENTS_BEFORE_6 + 4))" "SELECT count(*) FROM issues.issue_events"
check "down: vuelve el índice de 000003" "1" "SELECT count(*) FROM pg_indexes WHERE schemaname = 'issues' AND indexname = 'issue_events_issue_id_seq_idx'"

apply 000006_event_source.up
check "up de nuevo: todo sin fuente" "0" "SELECT count(source) FROM issues.issue_events"

if [ "$fails" -ne 0 ]; then echo "Migraciones: con fallas"; exit 1; fi
echo "Migraciones: todo ok"
