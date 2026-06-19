#!/usr/bin/env bash
set -e

wait_for() {
    local url="$1"
    local name="$2"
    printf 'Waiting for %s' "$name"
    for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30; do
        if curl -sf "$url/health" >/dev/null 2>&1; then
            printf ' ready\n'
            return 0
        fi
        printf '.'
        sleep 1
    done
    printf '\n%s did not become healthy within 30s\n' "$name" >&2
    return 1
}

GOB_URL="${GOB_URL:-http://localhost:8080}"
SQLITE_URL="${SQLITE_URL:-http://localhost:8081}"

wait_for "$GOB_URL"    "api-gob"
wait_for "$SQLITE_URL" "api-sqlite"
