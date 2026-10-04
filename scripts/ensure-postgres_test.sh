#!/usr/bin/env bash
# Safety contract for scripts/ensure-postgres.sh. Stubs docker and pg_isready
# so the test never invokes the real Compose CLI.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$ROOT/scripts/ensure-postgres.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cat > "$TMP/docker" << 'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "${DOCKER_LOG:?}"
exit 0
EOF
cat > "$TMP/pg_isready" << 'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "${PG_ISREADY_LOG:?}"
exit 0
EOF
chmod +x "$TMP/docker" "$TMP/pg_isready"

ENV_FILE="$TMP/env"
DOCKER_LOG="$TMP/docker.log"
PG_LOG="$TMP/pg.log"

reset_logs() {
  : > "$DOCKER_LOG"
  : > "$PG_LOG"
}

run_script() {
  # shellcheck disable=SC2086
  env \
    -u MULTICA_ISOLATED_POSTGRES \
    -u COMPOSE_PROJECT_NAME \
    -u COMPOSE_FILE \
    DOCKER_LOG="$DOCKER_LOG" \
    PG_ISREADY_LOG="$PG_LOG" \
    PATH="$TMP:$PATH" \
    "$@" \
    bash "$SCRIPT" "$ENV_FILE"
}

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

assert_no_docker() {
  if [ -s "$DOCKER_LOG" ]; then
    echo "docker was invoked:" >&2
    cat "$DOCKER_LOG" >&2
    fail "docker must not run"
  fi
}

assert_compose_isolated() {
  local project="$1"
  local file="$2"
  if ! grep -q -- "-p ${project} -f ${file}" "$DOCKER_LOG"; then
    echo "docker log:" >&2
    cat "$DOCKER_LOG" >&2
    fail "compose invocation missing -p ${project} -f ${file}"
  fi
  if grep -Eq '(^| )compose up -d postgres($| )' "$DOCKER_LOG"; then
    fail "unqualified docker compose up was recorded"
  fi
  # Every docker line is a compose invocation with both flags.
  while IFS= read -r line; do
    case "$line" in
      "compose -p ${project} -f ${file} "*) ;;
      *) fail "docker args were not an isolated compose invocation: $line" ;;
    esac
  done < "$DOCKER_LOG"
}

cat > "$ENV_FILE" << 'EOF'
DATABASE_URL=postgres://multica:multica@127.0.0.1:5433/multica_isolated?sslmode=disable
POSTGRES_DB=multica_isolated
POSTGRES_USER=multica
POSTGRES_PASSWORD=multica
EOF
reset_logs
run_script
assert_no_docker
if ! grep -q '127.0.0.1:5433/multica_isolated' "$PG_LOG"; then
  fail "pg_isready was not called for the external URL"
fi
echo "ok external localhost URL does not call docker"

cat > "$ENV_FILE" << 'EOF'
POSTGRES_DB=multica
POSTGRES_USER=multica
POSTGRES_PASSWORD=multica
EOF
reset_logs
if run_script; then
  fail "missing DATABASE_URL and opt-in should exit non-zero"
fi
assert_no_docker
echo "ok missing URL and opt-in fails closed"

reset_logs
if run_script MULTICA_ISOLATED_POSTGRES=1 COMPOSE_PROJECT_NAME=multica COMPOSE_FILE=/tmp/isolated-compose.yml; then
  fail "project name multica should be refused"
fi
assert_no_docker
echo "ok project name multica is refused"

reset_logs
if run_script MULTICA_ISOLATED_POSTGRES=1 COMPOSE_PROJECT_NAME=Multica COMPOSE_FILE=/tmp/isolated-compose.yml; then
  fail "project name Multica should be refused"
fi
assert_no_docker
echo "ok project name Multica is refused"

reset_logs
if run_script MULTICA_ISOLATED_POSTGRES=1 COMPOSE_PROJECT_NAME=vis18345 COMPOSE_FILE=/tmp/docker-compose.selfhost.yml; then
  fail "selfhost compose file should be refused"
fi
assert_no_docker
echo "ok selfhost compose file is refused"

reset_logs
run_script MULTICA_ISOLATED_POSTGRES=1 COMPOSE_PROJECT_NAME=vis18345 COMPOSE_FILE=/tmp/isolated-compose.yml
assert_compose_isolated vis18345 /tmp/isolated-compose.yml
echo "ok explicit isolated compose uses -p and -f"

cat > "$ENV_FILE" << 'EOF'
DATABASE_URL=postgres://multica:multica@127.0.0.1:5433/multica_isolated?sslmode=disable
POSTGRES_DB=multica_isolated
POSTGRES_USER=multica
POSTGRES_PASSWORD=multica
EOF
reset_logs
run_script MULTICA_ISOLATED_POSTGRES=1 COMPOSE_PROJECT_NAME=vis18345 COMPOSE_FILE=/tmp/isolated-compose.yml
assert_compose_isolated vis18345 /tmp/isolated-compose.yml
if [ -s "$PG_LOG" ]; then
  fail "isolated opt-in should not also probe via host pg_isready"
fi
echo "ok URL plus safe opt-in still uses flagged compose"

echo "ensure-postgres safety contract passed"
