#!/usr/bin/env bash
set -euo pipefail

# =============================================================================
# Marcar IMPORT_SYNC los eventos de un proyecto espejo — issues-service
# =============================================================================
#
# Issue 036 (protos v0.6.0): los eventos que registró ImportIssue antes de
# 000006 quedaron sin fuente (= LIVE), con la fecha de cada corrida. En un
# proyecto que es espejo de otro tracker (RND: nadie movió sus issues desde
# el panel, decisión de Victor del 2026-11-11), esos eventos no son cambios
# en vivo: las métricas de flujo los contarían junto al histórico importado.
# Este script les pone source = 'import_sync'.
#
# Alcance: los eventos sin fuente de los issues importados (con clave de
# origen) del proyecto <clave> del espacio de <uid>, de los tipos que
# escribe ImportIssue (created, status_changed, title_changed,
# description_changed, priority_changed, labels_changed). Comentarios,
# asignados, descartes y restauraciones nunca los escribe ImportIssue: no se
# tocan, y la prueba en seco los muestra aparte. Los eventos que ya tienen
# fuente (desde v0.6.0, o de ImportIssueEvents) tampoco. Requiere 000006.
#
# Además alinea con su created_at la fecha del evento «creado» (de
# ImportIssue: sin fuente o import_sync) de los issues importados del
# proyecto. En v0.5 quedaba con la fecha de la corrida, y desde v0.6.0 el
# contrato dice que es el alta (así el historial empieza por el «creado»).
# Es la única fecha que cambia; la prueba en seco la cuenta.
#
# Sin --apply (en QA y en prod) solo muestra qué cambiaría: el proyecto y
# cuántos eventos por tipo, dentro de BEGIN ... ROLLBACK. Con --apply lo
# guarda (en prod pide escribir el nombre de la base). Se puede correr de
# nuevo: la segunda vez no encuentra nada.
#
# Uso:
#   DATABASE_URL=... ./scripts/backfill_import_sync.sh qa   <uid> RND            # solo muestra
#   DATABASE_URL=... ./scripts/backfill_import_sync.sh prod <uid> RND --apply    # guarda
#
# Credenciales y cómo llegar a la base: como apply_migration.sh (DATABASE_URL
# por variable de entorno; psql local o en Docker, scripts/psql_env.sh).
# =============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

usage() {
  echo "Uso: DATABASE_URL=... $0 <qa|prod> <uid del dueño> <clave del proyecto> [--apply]" >&2
  exit 1
}

ENVIRONMENT="${1:-}"
OWNER="${2:-}"
KEY="${3:-}"
FLAG="${4:-}"
[[ -n "$ENVIRONMENT" && -n "$OWNER" && -n "$KEY" ]] || usage
case "$ENVIRONMENT" in
  qa | prod) ;;
  *) echo "Entorno inválido: '$ENVIRONMENT'" >&2; usage ;;
esac
APPLY=0
case "$FLAG" in
  "") ;;
  --apply) APPLY=1 ;;
  *) echo "Flag desconocido: $FLAG" >&2; usage ;;
esac

# shellcheck source=psql_env.sh
source "$SCRIPT_DIR/psql_env.sh"

# El proyecto y los eventos que se marcarían. Las variables van con :'x'
# (psql las cita): nada del uid ni de la clave se interpola en el SQL.
SQL_TARGET="
CREATE TEMP TABLE backfill_target ON COMMIT DROP AS
SELECT e.id, e.kind
  FROM issues.issue_events e
  JOIN issues.issues i ON i.id = e.issue_id
  JOIN issues.projects p ON p.id = i.project_id
  JOIN issues.workspaces w ON w.id = p.workspace_id
 WHERE w.owner_uid = :'owner' AND p.key = upper(:'key') AND e.source IS NULL
   AND i.source_key IS NOT NULL
   AND e.kind IN ('created', 'status_changed', 'title_changed', 'description_changed', 'priority_changed', 'labels_changed');

\\echo 'Proyecto (issues, issues importados):'
SELECT p.key, p.name, count(i.id) AS issues, count(i.source_key) AS importados
  FROM issues.projects p
  JOIN issues.workspaces w ON w.id = p.workspace_id
  LEFT JOIN issues.issues i ON i.project_id = p.id
 WHERE w.owner_uid = :'owner' AND p.key = upper(:'key')
 GROUP BY p.key, p.name;

\\echo 'Eventos sin fuente que pasan a import_sync, por tipo:'
SELECT kind, count(*) FROM backfill_target GROUP BY kind ORDER BY kind;

\\echo 'Eventos sin fuente que NO se tocan (issues creados en Tekitla o tipos que ImportIssue no escribe):'
SELECT CASE WHEN i.source_key IS NULL THEN 'creado en Tekitla' ELSE 'importado' END AS issue, e.kind, count(*)
  FROM issues.issue_events e
  JOIN issues.issues i ON i.id = e.issue_id
  JOIN issues.projects p ON p.id = i.project_id
  JOIN issues.workspaces w ON w.id = p.workspace_id
 WHERE w.owner_uid = :'owner' AND p.key = upper(:'key') AND e.source IS NULL
   AND e.id NOT IN (SELECT id FROM backfill_target)
 GROUP BY 1, 2 ORDER BY 1, 2;

UPDATE issues.issue_events SET source = 'import_sync' WHERE id IN (SELECT id FROM backfill_target);
SELECT count(*) AS marcados FROM backfill_target;

\\echo 'Evento «creado» de issues importados con la fecha de la corrida, que pasa al alta del issue (contrato v0.6.0):'
WITH aligned AS (
  UPDATE issues.issue_events e SET created_at = i.created_at
    FROM issues.issues i
    JOIN issues.projects p ON p.id = i.project_id
    JOIN issues.workspaces w ON w.id = p.workspace_id
   WHERE e.issue_id = i.id AND e.kind = 'created' AND e.created_at <> i.created_at
     AND COALESCE(e.source, 'live') IN ('live', 'import_sync')
     AND w.owner_uid = :'owner' AND p.key = upper(:'key') AND i.source_key IS NOT NULL
  RETURNING e.id)
SELECT count(*) AS altas_alineadas FROM aligned;
"

echo "> backfill import_sync: proyecto $KEY de $OWNER -> $DATABASE ($ENVIRONMENT)"

if [[ $APPLY -ne 1 ]]; then
  echo "Prueba en seco (BEGIN ... ROLLBACK)"
  { echo "BEGIN;"; echo "$SQL_TARGET"; echo "ROLLBACK;"; } | run_psql -v owner="$OWNER" -v key="$KEY"
  echo "Sin --apply: no se guardó nada. Para guardar: $0 $ENVIRONMENT $OWNER $KEY --apply"
  exit 0
fi

if [[ "$ENVIRONMENT" == "prod" ]]; then
  confirm_prod "el backfill import_sync del proyecto $KEY"
fi

{ echo "BEGIN;"; echo "$SQL_TARGET"; echo "COMMIT;"; } | run_psql -v owner="$OWNER" -v key="$KEY"
echo "Backfill aplicado en $DATABASE"
