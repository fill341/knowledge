# Развёртывание (локально, Docker Compose)

## 1. Состав

| Сервис | Образ | Назначение | Порт на хосте |
|--------|-------|------------|---------------|
| `postgres` | `pgvector/pgvector:pg17` | Хранилище | `127.0.0.1:5433` (только для админ-доступа, можно убрать) |
| `migrate` | сборка `knowledge-api` | Одноразово применяет миграции ролью `kn_owner` | — |
| `knowledge-api` | сборка `knowledge-api` | REST API, роль `kn_app` | `127.0.0.1:8787` |
| `backup` | `pgvector/pgvector:pg17` (ради `pg_dump` той же версии) | Ежедневный дамп с ротацией | — |
| `mcp-http` | сборка `mcp-server` | Профиль `mcp-http`, только для клиентов без stdio | `127.0.0.1:8788` |

`kn-mcp` и `kn` собираются на хост в `bin/` (`make build`; без Go — в Docker) — их запускают сами AI-инструменты.

## 2. `deploy/docker-compose.yml` (черновик)

```yaml
name: knowledge

services:
  postgres:
    image: pgvector/pgvector:pg17
    restart: unless-stopped
    environment:
      POSTGRES_DB: knowledge
      POSTGRES_USER: kn_owner
      POSTGRES_PASSWORD_FILE: /run/secrets/pg_owner_password
      KN_APP_PASSWORD_FILE: /run/secrets/pg_app_password   # читает init-скрипт
    secrets: [pg_owner_password, pg_app_password]
    volumes:
      - pgdata:/var/lib/postgresql/data
      - ./postgres/init:/docker-entrypoint-initdb.d:ro
    ports:
      - "127.0.0.1:5433:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U kn_owner -d knowledge"]
      interval: 5s
      timeout: 3s
      retries: 20
    networks: [backend]

  migrate:
    build:
      context: ..
      dockerfile: services/knowledge-api/Dockerfile
    command: ["migrate", "up"]
    environment:
      KN_DB_HOST: postgres
      KN_DB_NAME: knowledge
      KN_DB_USER: kn_owner
      KN_DB_PASSWORD_FILE: /run/secrets/pg_owner_password
    secrets: [pg_owner_password]
    depends_on:
      postgres: { condition: service_healthy }
    restart: "no"
    networks: [backend]

  knowledge-api:
    build:
      context: ..
      dockerfile: services/knowledge-api/Dockerfile
    command: ["serve"]
    restart: unless-stopped
    environment:
      KN_HTTP_ADDR: 0.0.0.0:8787
      KN_DB_HOST: postgres
      KN_DB_NAME: knowledge
      KN_DB_USER: kn_app
      KN_DB_PASSWORD_FILE: /run/secrets/pg_app_password
      KN_LOG_LEVEL: ${KN_LOG_LEVEL:-info}
      KN_AUDIT_READS: ${KN_AUDIT_READS:-false}
    secrets: [pg_app_password]
    depends_on:
      migrate: { condition: service_completed_successfully }
    ports:
      - "127.0.0.1:8787:8787"
    healthcheck:
      test: ["CMD", "/kn-api", "healthcheck"]   # distroless: без shell и curl
      interval: 10s
      timeout: 3s
      retries: 5
    read_only: true
    user: "65532:65532"
    networks: [backend]

  backup:
    image: pgvector/pgvector:pg17
    restart: unless-stopped
    entrypoint: ["/bin/sh", "/backup.sh"]
    environment:
      PGHOST: postgres
      PGDATABASE: knowledge
      PGUSER: kn_owner
      BACKUP_INTERVAL_SECONDS: ${BACKUP_INTERVAL_SECONDS:-86400}
      BACKUP_KEEP: ${BACKUP_KEEP:-14}
    secrets: [pg_owner_password]
    volumes:
      - ./backup/backup.sh:/backup.sh:ro
      - ${KN_BACKUP_DIR:-./backups}:/backups
    depends_on:
      postgres: { condition: service_healthy }
    networks: [backend]

  mcp-http:
    profiles: ["mcp-http"]
    build:
      context: ..
      dockerfile: services/mcp-server/Dockerfile
    command: ["--http", "0.0.0.0:8788"]
    environment:
      KN_API_URL: http://knowledge-api:8787
      # токен передаёт клиент в заголовке Authorization — сервер не хранит токенов
    ports:
      - "127.0.0.1:8788:8788"
    depends_on:
      knowledge-api: { condition: service_healthy }
    networks: [backend]

secrets:
  pg_owner_password:
    file: ./secrets/pg_owner_password
  pg_app_password:
    file: ./secrets/pg_app_password

volumes:
  pgdata:

networks:
  backend:
```

## 3. `deploy/postgres/init/01-roles.sh`

Выполняется только при первой инициализации пустого volume.

```bash
#!/usr/bin/env bash
set -euo pipefail
app_password="$(cat "$KN_APP_PASSWORD_FILE")"
psql -v ON_ERROR_STOP=1 -v app_password="$app_password" \
     --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<'SQL'
CREATE ROLE kn_app LOGIN PASSWORD :'app_password';
GRANT CONNECT ON DATABASE knowledge TO kn_app;
GRANT USAGE ON SCHEMA public TO kn_app;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
SQL
```

Права на таблицы выдаются в миграциях (см. конец DDL в [data-model.md](data-model.md)), а не через `ALTER DEFAULT PRIVILEGES`, — чтобы явно не выдавать `DELETE`.

## 4. Бэкапы

`deploy/backup/backup.sh`:

```sh
#!/bin/sh
set -eu
export PGPASSWORD="$(cat /run/secrets/pg_owner_password)"
while true; do
  ts="$(date -u +%Y%m%dT%H%M%SZ)"
  if pg_dump --format=custom --file="/backups/knowledge-$ts.dump.part"; then
    mv "/backups/knowledge-$ts.dump.part" "/backups/knowledge-$ts.dump"
    echo "backup ok: knowledge-$ts.dump"
  else
    rm -f "/backups/knowledge-$ts.dump.part"
    echo "backup FAILED" >&2
  fi
  ls -1t /backups/knowledge-*.dump 2>/dev/null | tail -n +"$((BACKUP_KEEP + 1))" | xargs -r rm --
  sleep "$BACKUP_INTERVAL_SECONDS"
done
```

Три уровня защиты данных:

1. **История версий** в самой БД — откат любой записи.
2. **`pg_dump`** ежедневно, 14 копий, в `KN_BACKUP_DIR` (по умолчанию `deploy/backups`, в `.gitignore`). Рекомендуется направить на внешний диск или в Time Machine-покрываемую папку. Опционально: шифрование `age` перед записью.
3. **Экспорт в Markdown/JSONL** (`kn export`) — человекочитаемая копия, не требующая PostgreSQL для чтения. Например, еженедельно через `launchd`.

### Восстановление (`scripts/restore.sh <dump>`)

```bash
docker compose -f deploy/docker-compose.yml stop knowledge-api
docker compose -f deploy/docker-compose.yml exec -T postgres \
  pg_restore --clean --if-exists --no-owner --role=kn_owner -U kn_owner -d knowledge < "$1"
docker compose -f deploy/docker-compose.yml up -d knowledge-api
```

`make restore-test` — раз в месяц: поднимает временный контейнер PostgreSQL, восстанавливает последний дамп и проверяет
число записей и выборочные `entry_versions`. Бэкап, который не проверялся, считается отсутствующим.

## 5. Повседневные команды (`Makefile`)

| Команда | Действие |
|---------|----------|
| `make secrets` | Сгенерировать `deploy/secrets/*` (если их нет) |
| `make up` / `make down` | Поднять / остановить стек (`down` **без** `-v`) |
| `make logs` | Логи сервисов |
| `make migrate` | Применить миграции |
| `make token PRINCIPAL=claude-code@work SCOPES="space:work:write space:shared:read"` | Выпустить токен и положить в Keychain |
| `make backup` | Внеочередной дамп |
| `make restore DUMP=...` | Восстановление |
| `make setup` | Первый запуск: стек, `kn`/`kn-mcp` в `bin/` (локальным Go или в Docker), admin-токен |
| `make connect AGENT=claude-code\|cursor\|codex [PROFILE=personal]` | Токен агента + MCP-сервер, хук и инструкция в конфиге инструмента (`kn connect`) |
| `make ui` | Веб-интерфейс с уже выполненным входом (`kn ui`: одноразовая ссылка на минуту) |
| `make build` | Собрать `kn`, `kn-mcp` в `bin/`; без Go — в контейнере `golang` под ОС и архитектуру хоста |

## 6. Первый запуск

```bash
make setup                        # secrets → up → build → admin-token (пропускается, если рабочий токен уже в Keychain)
make connect AGENT=claude-code    # cursor, codex
make ui
```

Вручную то же самое: `make up`, `make build`, `make admin-token`, затем
`make token PRINCIPAL=claude-code@personal SCOPES="space:personal:write space:shared:read"` и
`claude mcp add knowledge --scope user -- "$PWD/scripts/kn-mcp-launch" claude-code personal`.

## 7. Предостережения

- `docker compose down -v` удаляет volume `pgdata` вместе со всеми знаниями. В `Makefile` эта команда не используется.
- Мажорное обновление PostgreSQL (17 → 18) — только через дамп и восстановление в новый volume; в образе 18 также изменился путь данных.
- Порт `5433` открыт только на `127.0.0.1`; если прямой доступ к БД не нужен — убрать `ports` у `postgres`.
