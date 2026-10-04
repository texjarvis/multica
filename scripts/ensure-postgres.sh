#!/usr/bin/env bash
# Fail closed. This script never runs an unqualified `docker compose`.
#
# A set DATABASE_URL is an external database: this script only runs pg_isready.
# A localhost URL is not permission to start the Compose project in the
# current directory. The directory name `multica` would select the live
# self-host project.
#
# Docker Compose runs only when all of the following are set:
#   MULTICA_ISOLATED_POSTGRES=1
#   COMPOSE_PROJECT_NAME   a project name other than `multica`
#   COMPOSE_FILE           a compose file whose basename does not refer to
#                          the self-host stack
# The invocation is always:
#   docker compose -p "$COMPOSE_PROJECT_NAME" -f "$COMPOSE_FILE" ...
# Without that opt-in and without DATABASE_URL, the script exits non-zero.
set -euo pipefail

ENV_FILE="${1:-.env}"

if [ ! -f "$ENV_FILE" ]; then
  echo "Missing env file: $ENV_FILE"
  echo "Create .env from .env.example, or run 'make worktree-env' and use .env.worktree."
  exit 1
fi

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

POSTGRES_DB="${POSTGRES_DB:-multica}"
POSTGRES_USER="${POSTGRES_USER:-multica}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-multica}"
DATABASE_URL="${DATABASE_URL:-}"

export PGPASSWORD="$POSTGRES_PASSWORD"

db_host=""
db_port="${POSTGRES_PORT:-5432}"
db_name="$POSTGRES_DB"

parse_database_url() {
  local rest authority hostport path port_part

  rest="${DATABASE_URL#*://}"
  rest="${rest%%\?*}"
  authority="${rest%%/*}"
  path="${rest#*/}"

  if [ "$authority" = "$rest" ]; then
    path=""
  fi

  hostport="${authority##*@}"

  if [[ "$hostport" == \[* ]]; then
    db_host="${hostport#\[}"
    db_host="${db_host%%]*}"
    port_part="${hostport#*\]}"
    if [[ "$port_part" == :* ]] && [ -n "${port_part#:}" ]; then
      db_port="${port_part#:}"
    fi
  else
    db_host="${hostport%%:*}"
    if [[ "$hostport" == *:* ]] && [ -n "${hostport##*:}" ]; then
      db_port="${hostport##*:}"
    fi
  fi

  if [ -n "$path" ]; then
    db_name="${path%%/*}"
  fi
}

if [ -n "$DATABASE_URL" ]; then
  parse_database_url
fi

isolated_requested() {
  case "${MULTICA_ISOLATED_POSTGRES:-}" in
    1|true|TRUE|yes|YES) return 0 ;;
    *) return 1 ;;
  esac
}

refuse_live_compose_target() {
  local project_lc file_lc
  if [ -z "${COMPOSE_PROJECT_NAME:-}" ]; then
    echo "COMPOSE_PROJECT_NAME must be set to an isolated project name."
    exit 1
  fi
  project_lc="$(printf '%s' "$COMPOSE_PROJECT_NAME" | tr '[:upper:]' '[:lower:]')"
  if [ "$project_lc" = "multica" ]; then
    echo "Refusing COMPOSE_PROJECT_NAME=multica. That name is the live self-host project."
    exit 1
  fi
  if [ -z "${COMPOSE_FILE:-}" ]; then
    echo "COMPOSE_FILE must be set to an isolated compose file."
    exit 1
  fi
  file_lc="$(printf '%s' "$(basename "$COMPOSE_FILE")" | tr '[:upper:]' '[:lower:]')"
  case "$file_lc" in
    *selfhost*)
      echo "Refusing self-host compose file: $COMPOSE_FILE"
      exit 1
      ;;
  esac
}

compose() {
  docker compose -p "$COMPOSE_PROJECT_NAME" -f "$COMPOSE_FILE" "$@"
}

wait_for_external() {
  if ! command -v pg_isready > /dev/null 2>&1; then
    echo "pg_isready is required to verify DATABASE_URL. Refusing to continue."
    exit 1
  fi
  echo "==> External database (host: ${db_host:-unknown}). Not starting Docker."
  echo "==> Waiting for PostgreSQL at ${db_host}:${db_port} to be ready..."
  until pg_isready -d "$DATABASE_URL" > /dev/null 2>&1; do
    sleep 1
  done
  echo "✓ PostgreSQL ready (external: ${db_host}:${db_port}). Database: $db_name"
}

ensure_isolated_compose() {
  refuse_live_compose_target
  echo "==> Starting isolated PostgreSQL project '$COMPOSE_PROJECT_NAME' from '$COMPOSE_FILE'..."
  compose up -d postgres

  echo "==> Waiting for isolated PostgreSQL to be ready..."
  until compose exec -T postgres pg_isready -U "$POSTGRES_USER" -d postgres > /dev/null 2>&1; do
    sleep 1
  done

  echo "==> Ensuring database '$POSTGRES_DB' exists..."
  db_exists="$(compose exec -T postgres \
    psql -U "$POSTGRES_USER" -d postgres -Atqc "SELECT 1 FROM pg_database WHERE datname = '$POSTGRES_DB'")"

  if [ "$db_exists" != "1" ]; then
    compose exec -T postgres \
      psql -U "$POSTGRES_USER" -d postgres -v ON_ERROR_STOP=1 \
      -c "CREATE DATABASE \"$POSTGRES_DB\"" \
      > /dev/null
  fi

  echo "✓ PostgreSQL ready (isolated compose project: $COMPOSE_PROJECT_NAME). Database: $POSTGRES_DB"
}

if isolated_requested; then
  ensure_isolated_compose
elif [ -n "$DATABASE_URL" ]; then
  wait_for_external
else
  echo "Refusing to start PostgreSQL."
  echo "Set DATABASE_URL to an external database, or set MULTICA_ISOLATED_POSTGRES=1"
  echo "with COMPOSE_PROJECT_NAME (not multica) and COMPOSE_FILE for an isolated project."
  exit 1
fi
