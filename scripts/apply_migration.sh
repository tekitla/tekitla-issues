#!/usr/bin/env bash
set -euo pipefail

# =============================================================================
# Aplicar una migración a QA o PROD — issues-service
# =============================================================================
#
# No hay migration runner ni tabla schema_migrations: los .up.sql se aplican a
# mano con psql, ANTES de que arranque el contenedor que los necesita. Sirve
# para cualquier destino (Cloud SQL, el Postgres compartido de Coolify, uno
# local): solo necesita una DATABASE_URL alcanzable desde esta máquina.
#
# Pasos:
#   1. Prueba en seco: el .up.sql dentro de BEGIN ... ROLLBACK. Si falla, corta.
#   2. Aplica (ON_ERROR_STOP). En prod además:
#        - sin --apply solo corre la prueba en seco;
#        - con --apply pide escribir el nombre de la base (necesita terminal).
#
# Credenciales: DATABASE_URL por variable de entorno (no como argumento, para
# que no quede en el historial). La URL no se imprime. Para cargarla sin eco:
#   read -rs DATABASE_URL && export DATABASE_URL
#
# Cómo llegar a la base:
#   - Cloud SQL: cloud-sql-proxy <proj:region:inst> --port 5432 y
#       DATABASE_URL=postgres://USER:PASS@localhost:5432/DB?sslmode=disable
#   - Coolify: túnel SSH al contenedor del Postgres compartido del entorno
#     (su IP en la red `coolify`; ver docs/deploy.md)
#       ssh -N -L 15432:<IP-del-contenedor>:5432 usuario@vps
#       DATABASE_URL=postgres://USER:PASS@localhost:15432/DB?sslmode=disable
#
# psql: local o en Docker (scripts/psql_env.sh).
#
# Uso:
#   DATABASE_URL=... ./scripts/apply_migration.sh qa   000001_create_issues_schema
#   DATABASE_URL=... ./scripts/apply_migration.sh prod 000001_create_issues_schema           # solo prueba en seco
#   DATABASE_URL=... ./scripts/apply_migration.sh prod 000001_create_issues_schema --apply   # aplica
#
# Variables:
#   DATABASE_URL       (requerida) postgres://USER:PASS@HOST:PORT/DB?...
#   PG_IMAGE           (default postgres:18) imagen para psql si no hay local
#   PG_USE_DOCKER      1 = usar Docker aunque haya psql local
#   PG_DOCKER_NETWORK  red de Docker a la que conectar el contenedor
# =============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

usage() {
  echo "Uso: DATABASE_URL=... $0 <qa|prod> <migración> [--apply]   (ej. 000001_create_issues_schema)" >&2
  exit 1
}

ENVIRONMENT="${1:-}"
MIGRATION="${2:-}"
FLAG="${3:-}"
[[ -n "$ENVIRONMENT" && -n "$MIGRATION" ]] || usage

case "$ENVIRONMENT" in
  qa)   APPLY=1 ;;
  prod) APPLY=0 ;;
  *)    echo "Entorno inválido: '$ENVIRONMENT'" >&2; usage ;;
esac
case "$FLAG" in
  "") ;;
  --apply) APPLY=1 ;;
  *) echo "Flag desconocido: $FLAG" >&2; usage ;;
esac

# shellcheck source=psql_env.sh
source "$SCRIPT_DIR/psql_env.sh"

# Acepta el nombre a secas, con .up.sql o con la ruta completa.
name="$(basename "$MIGRATION")"
name="${name%.sql}"
name="${name%.up}"
SQL_FILE="$SCRIPT_DIR/../migrations/$name.up.sql"

[[ -f "$SQL_FILE" ]] || { echo "No existe $SQL_FILE" >&2; exit 1; }

# Un BEGIN/COMMIT propio rompería la prueba en seco: el COMMIT de adentro
# persistiría antes del ROLLBACK de afuera.
if grep -qiE '^[[:space:]]*(BEGIN|COMMIT|ROLLBACK)[[:space:]]*;' "$SQL_FILE"; then
  echo "$name trae su propio BEGIN/COMMIT: no se puede probar en seco. Aplicala a mano." >&2
  exit 1
fi

echo "> $name -> $DATABASE ($ENVIRONMENT)"

echo "1/2 Prueba en seco (BEGIN ... ROLLBACK)"
{ echo "BEGIN;"; cat "$SQL_FILE"; echo; echo "ROLLBACK;"; } | run_psql
echo "    ok"

if [[ $APPLY -ne 1 ]]; then
  echo "Sin --apply: no se persistió nada. Para aplicar: $0 $ENVIRONMENT $name --apply"
  exit 0
fi

if [[ "$ENVIRONMENT" == "prod" ]]; then
  confirm_prod "$name"
fi

echo "2/2 Aplicando"
run_psql < "$SQL_FILE"
echo "$name aplicada en $DATABASE"
