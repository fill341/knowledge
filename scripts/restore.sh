#!/usr/bin/env bash
# restore.sh <dump> — restore a pg_dump (custom format) into the running stack.
# The API is stopped during the restore so nothing writes in between.
set -euo pipefail

dump="${1:?usage: restore.sh <path/to/knowledge-*.dump>}"
[ -f "$dump" ] || { echo "no such file: $dump" >&2; exit 1; }

cd "$(dirname "$0")/.."
compose="docker compose -f deploy/docker-compose.yml"

read -r -p "This replaces ALL data in the knowledge database with $dump. Type 'restore' to continue: " answer
[ "$answer" = "restore" ] || { echo "aborted"; exit 1; }

$compose stop knowledge-api
$compose exec -T postgres pg_restore --clean --if-exists --no-owner --role=kn_owner \
  -U kn_owner -d knowledge < "$dump"
$compose up -d --wait knowledge-api
echo "restored from $dump"
