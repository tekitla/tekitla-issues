# shellcheck shell=bash
# =============================================================================
# psql contra DATABASE_URL, local o en Docker. Lo cargan (source) los scripts
# que tocan una base real: apply_migration.sh y backfill_import_sync.sh.
# =============================================================================
#
# Define run_psql (psql con ON_ERROR_STOP, -X y -q; los argumentos extra
# pasan tal cual) y DATABASE (el nombre de la base, para confirmar en prod).
# La URL no se imprime ni queda en `ps`.
#
# psql: usa el local si existe; si no, la imagen $PG_IMAGE con Docker. En modo
# Docker, localhost/127.0.0.1 de la URL se reescribe a host.docker.internal
# (el contenedor no ve el localhost de la máquina). PG_DOCKER_NETWORK=<red>
# conecta el contenedor a una red de Docker y no reescribe nada.
#
# Variables: DATABASE_URL (requerida), PG_IMAGE (default postgres:18),
# PG_USE_DOCKER (1 = Docker aunque haya psql local), PG_DOCKER_NETWORK.

PG_IMAGE="${PG_IMAGE:-postgres:18}"

[[ -n "${DATABASE_URL:-}" ]] || { echo "Falta DATABASE_URL (variable de entorno)" >&2; exit 1; }

# Nombre de la base: lo que sigue a la última "/" sin la query string.
DATABASE="${DATABASE_URL%%\?*}"
DATABASE="${DATABASE##*/}"
[[ -n "$DATABASE" ]] || { echo "DATABASE_URL no trae nombre de base" >&2; exit 1; }

if command -v psql >/dev/null 2>&1 && [[ "${PG_USE_DOCKER:-}" != "1" ]]; then
  run_psql() { psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -X -q "$@"; }
else
  command -v docker >/dev/null 2>&1 || { echo "No hay psql local ni docker" >&2; exit 1; }
  DOCKER_ARGS=(--rm -i)
  if [[ -n "${PG_DOCKER_NETWORK:-}" ]]; then
    DOCKER_ARGS+=(--network "$PG_DOCKER_NETWORK")
    PG_URL="$DATABASE_URL"
  else
    DOCKER_ARGS+=(--add-host=host.docker.internal:host-gateway)
    PG_URL="$(printf '%s' "$DATABASE_URL" | sed -E 's#@(localhost|127\.0\.0\.1)([:/])#@host.docker.internal\2#')"
  fi
  # La URL viaja por variable de entorno (no queda en `ps` del host).
  run_psql() {
    PG_URL="$PG_URL" docker run "${DOCKER_ARGS[@]}" -e PG_URL "$PG_IMAGE" \
      sh -c 'exec psql "$PG_URL" -v ON_ERROR_STOP=1 -X -q "$@"' psql "$@"
  }
fi

# confirm_prod <qué> pide escribir el nombre de la base (necesita terminal).
confirm_prod() {
  echo
  echo "Vas a aplicar $1 en PROD ($DATABASE)."
  # Sin terminal el read recibe EOF: avisar en vez de cortar en silencio.
  if ! read -r -p "Escribí '$DATABASE' para confirmar: " answer; then
    echo >&2
    echo "No hay terminal para confirmar: no se aplicó nada." >&2
    exit 1
  fi
  [[ "$answer" == "$DATABASE" ]] || { echo "Cancelado." >&2; exit 1; }
}
