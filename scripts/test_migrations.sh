#!/usr/bin/env bash
# =============================================================================
# Prueba de las migraciones contra un Postgres 16 descartable (docker)
# =============================================================================
# Aplica migrations/*.up.sql en orden y, antes de cada migración con backfill,
# carga datos de la versión anterior para verificar que el backfill los deja
# bien. Al final prueba down -> up de la última. No toca ninguna base real:
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
  SELECT gen_random_uuid(), id, 'u1', 'discarded', '{}'::jsonb FROM issues.issues
   WHERE number = 4 AND project_id = '00000000-0000-4000-8000-0000000000a1'"
echo "ok   motivo completo en un CANCELED y evento 'discarded'"

apply 000004_discard_search_status_settings.down
check "down: sin columnas nuevas" "0" "SELECT count(*) FROM information_schema.columns
  WHERE table_schema = 'issues' AND ((table_name = 'issues' AND column_name LIKE 'discard%')
     OR (table_name = 'statuses' AND column_name IN ('color', 'wip_limit', 'hidden')))"
check "down: sin eventos discarded" "0" "SELECT count(*) FROM issues.issue_events WHERE kind IN ('discarded', 'restored')"
check "down: el issue sigue descartado" "canceled" "SELECT state FROM issues.issues WHERE number = 4 AND project_id = '00000000-0000-4000-8000-0000000000a1'"

apply 000004_discard_search_status_settings.up
check "up de nuevo: backfill" "0" "SELECT count(*) FROM issues.statuses WHERE hidden <> (category = 'canceled')"

if [ "$fails" -ne 0 ]; then echo "Migraciones: con fallas"; exit 1; fi
echo "Migraciones: todo ok"
