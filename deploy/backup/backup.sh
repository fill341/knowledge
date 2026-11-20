#!/bin/sh
# Periodic pg_dump with rotation. Runs inside the backup container.
set -eu

export PGPASSWORD="$(cat /run/secrets/pg_owner_password)"

dump() {
  ts="$(date -u +%Y%m%dT%H%M%SZ)"
  if pg_dump --format=custom --file="/backups/knowledge-$ts.dump.part"; then
    mv "/backups/knowledge-$ts.dump.part" "/backups/knowledge-$ts.dump"
    echo "backup ok: knowledge-$ts.dump"
  else
    rm -f "/backups/knowledge-$ts.dump.part"
    echo "backup FAILED" >&2
    return 1
  fi
  ls -1t /backups/knowledge-*.dump 2>/dev/null | tail -n +"$((BACKUP_KEEP + 1))" | xargs -r rm --
}

# `backup.sh once` makes a single dump (used by `make backup`)
if [ "${1:-}" = "once" ]; then
  dump
  exit $?
fi

while true; do
  dump || true
  sleep "$BACKUP_INTERVAL_SECONDS"
done
