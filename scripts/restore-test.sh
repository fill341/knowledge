#!/usr/bin/env bash
# restore-test.sh — restore the latest dump into a throwaway database on the running postgres
# and compare row counts with the live database. A backup that was never restored is not a backup.
set -euo pipefail

cd "$(dirname "$0")/.."
compose="docker compose -f deploy/docker-compose.yml"
dir="${KN_BACKUP_DIR:-deploy/backups}"

latest="$(ls -1t "$dir"/knowledge-*.dump 2>/dev/null | head -1 || true)"
[ -n "$latest" ] || { echo "no dumps in $dir" >&2; exit 1; }
echo "testing $latest"

psql() { $compose exec -T postgres psql -U kn_owner -v ON_ERROR_STOP=1 -tA "$@"; }

psql -d knowledge -c "DROP DATABASE IF EXISTS kn_restore_test WITH (FORCE)" >/dev/null
psql -d knowledge -c "CREATE DATABASE kn_restore_test" >/dev/null
trap 'psql -d knowledge -c "DROP DATABASE IF EXISTS kn_restore_test WITH (FORCE)" >/dev/null' EXIT

$compose exec -T postgres pg_restore --no-owner -U kn_owner -d kn_restore_test < "$latest"

query="SELECT (SELECT count(*) FROM entries) || ' entries, ' ||
              (SELECT count(*) FROM entry_versions) || ' versions, ' ||
              (SELECT count(*) FROM projects) || ' projects, ' ||
              (SELECT count(*) FROM audit_log) || ' audit rows'"
restored="$(psql -d kn_restore_test -c "$query")"
live="$(psql -d knowledge -c "$query")"
echo "restored: $restored"
echo "live:     $live"
echo "OK: dump restores cleanly (live data may be newer than the dump)"
