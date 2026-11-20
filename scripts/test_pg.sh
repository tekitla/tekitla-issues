#!/usr/bin/env bash
# =============================================================================
# Tests contra un Postgres 16 descartable (docker): los que se saltan sin
# TEKITLA_PG_TEST_URL (hoy, el repo de la integración del issue 016).
# Levanta un contenedor propio y lo borra al terminar. No toca ninguna base real.
#
#   scripts/test_pg.sh        (o `make test-pg`)
# =============================================================================
set -euo pipefail

cd "$(dirname "$0")/.."
NAME="issues-pgtest-$$"
IMAGE="${PG_IMAGE:-postgres:16}"
PORT="${PG_TEST_PORT:-25432}"

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker run -d --name "$NAME" -e POSTGRES_PASSWORD=postgres -p "127.0.0.1:$PORT:5432" "$IMAGE" >/dev/null
for _ in $(seq 1 60); do
  docker exec "$NAME" psql -U postgres -tAc 'SELECT 1' >/dev/null 2>&1 && break
  sleep 1
done

TEKITLA_PG_TEST_URL="postgres://postgres:postgres@127.0.0.1:$PORT/postgres?sslmode=disable" \
  go test ./app/infra/postgres/ -run 'Postgres' -count=1 -v
