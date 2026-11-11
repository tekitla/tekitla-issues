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
# Alcance: TODOS los eventos sin fuente de los issues del proyecto <clave>
# del espacio de <uid>. Los que ya tienen fuente (registrados desde v0.6.0,
# o importados con ImportIssueEvents) no se tocan. Requiere 000006.
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
 WHERE w.owner_uid = :'owner' AND p.key = upper(:'key') AND e.source IS NULL;

\\echo 'Proyecto (issues, issues importados):'
SELECT p.key, p.name, count(i.id) AS issues, count(i.source_key) AS importados
  FROM issues.projects p
  JOIN issues.workspaces w ON w.id = p.workspace_id
  LEFT JOIN issues.issues i ON i.project_id = p.id
 WHERE w.owner_uid = :'owner' AND p.key = upper(:'key')
 GROUP BY p.key, p.name;

\\echo 'Eventos sin fuente que pasan a import_sync, por tipo:'
SELECT kind, count(*) FROM backfill_target GROUP BY kind ORDER BY kind;

UPDATE issues.issue_events SET source = 'import_sync' WHERE id IN (SELECT id FROM backfill_target);
SELECT count(*) AS marcados FROM backfill_target;
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
