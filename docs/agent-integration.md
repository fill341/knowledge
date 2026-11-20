# Интеграция агентов

## 1. Модель интеграции

```
Агент (Claude Code / Cursor / Codex / bb / ...)
   │  ① MCP-инструменты — чтение и запись по ходу работы
   │  ② Хук старта сессии — автоматический контекст (где инструмент это умеет)
   │  ③ Инструкция агенту (CLAUDE.md / AGENTS.md / rules) — когда и что сохранять
   ▼
kn-mcp ──REST──▶ knowledge-api
```

**Добавление нового агента без изменения ядра:**

1. `kn token create --principal <agent>@<profile> --scope space:work:write --scope space:shared:read`
2. Положить токен в Keychain.
3. Подключить `kn-mcp` в конфиге инструмента (любой MCP-клиент) или использовать `kn` CLI / REST, если MCP нет.
4. Добавить общую инструкцию из `adapters/common/AGENTS.snippet.md`.

## 2. MCP-сервер `kn-mcp`

- Отдельный Go-бинарник, транспорт **stdio** по умолчанию; `--http 127.0.0.1:8788` — опционально (streamable HTTP).
- Конфигурация через env:

| Переменная | Назначение |
|------------|------------|
| `KN_API_URL` | `http://127.0.0.1:8787` |
| `KN_TOKEN` | токен агента (подставляет `scripts/kn-mcp-launch` из Keychain) |
| `KN_AGENT` | `claude-code`, `cursor`, `codex`, ... — для провенанса |
| `KN_PROJECT` | явный проект `space/slug`; иначе — автоопределение |
| `KN_DEFAULT_SPACE` | пространство для записи, если проект не определён |

- **Автоопределение проекта** (по порядку): `KN_PROJECT`; репозиторий рабочей директории процесса; репозитории рабочих папок,
  которые сообщил клиент (MCP roots, протоколы до 2026-07-28 — Cursor запускает сервер из `$HOME`); явный вызов `kn_project`.
  Репозиторий ищется через `POST /v1/projects/resolve` сначала по `git remote` (нормализуется: `git@github.com:acme/x.git` →
  `github.com/acme/x`), затем по пути корня — `file:///…/x`, для репозиториев без remote. Папка вне git-репозитория
  (например, `$HOME`) не сопоставляется ни с чем. Привязать репозиторий к существующему проекту: агент — `kn_project` с
  `project` и `path`; человек — `kn project link <space>/<slug> --repo-from-cwd`. Хук `kn context --repo-from-cwd` ищет так же.
  Не нашёлся — `kn_context` подсказывает агенту вызвать `kn_project` (с `project` или `create: true`), а сохранение без проекта
  сопровождается предупреждением. У клиентов без локальной папки (ChatGPT через туннель) `kn_context` показывает
  список проектов, и агент выбирает его вызовом `kn_context(project="<space>/<slug>")`.
- **Сессия:** при первом обращении на запись лениво создаёт `agent_session`; её `id` подставляется во все записи как провенанс.
- Stateless относительно данных: никакого кэша знаний, всё — через API.

### Инструменты

Набор намеренно маленький: описания инструментов занимают контекст модели.

| Инструмент | Вход | REST | Назначение |
|------------|------|------|------------|
| `kn_context` | `project?`, `task?`, `max_tokens?` (по умолчанию 10000) | `POST /v1/context` | Контекст текущего проекта: правила, решения, уроки, связанное. `project` переключает сессию на проект; без проекта в ответе — список доступных |
| `kn_project` | `path?`, `project?`, `create?`, `name?` | `POST /v1/projects/resolve`, `POST /v1/projects` | Выбрать проект сессии по пути к репозиторию; с `create` — зарегистрировать |
| `kn_search` | `q`, `kinds?`, `project?`, `spaces?`, `limit?` | `POST /v1/search` | Поиск знаний |
| `kn_get` | `id`, `version?` | `GET /v1/entries/{id}` (+ `/impact`) | Полный текст записи; если срок перепроверки прошёл — предупреждение; список записей, которые от неё зависят |
| `kn_save` | `kind`, `title`, `body`, `summary?`, `tags?`, `attributes?`, `project?`, `confidence?`, `source_ref?` | `POST /v1/entries` | Сохранить знание (факт, урок, правило, заметку, сниппет) |
| `kn_record_decision` | `title`, `context`, `decision`, `alternatives?`, `consequences?` | `POST /v1/entries` (`kind=decision`) | Удобная обёртка для ADR — структурированные поля проще для модели |
| `kn_update` | `id`, `expected_version`, поля, `change_note` | `PATCH /v1/entries/{id}` | Обновить существующую запись вместо создания дубля |
| `kn_link` | `from_id`, `to_id`, `rel` | `POST /v1/entries/{id}/links` | Связать записи |
| `kn_log_session` | `summary`, `goal?`, `status?`, `done?`, `commits?`, `open_questions?`, `next_steps?`, `continues?`, `outcomes?`, `files_touched?` | `POST /v1/sessions/{id}/end` | Итог работы в конце сессии. С `next_steps` (или `status: in_progress`/`blocked`) — передача работы: следующий агент проекта увидит её в `kn_context` в разделе «Незавершённая работа»; подхватил — укажи её id в `continues` |

Ответы инструментов — компактный текст (markdown), не сырой JSON; ошибки API переводятся в понятные модели сообщения
(например, на `409 duplicate` — «такая запись уже есть: `<id>`, используй `kn_update`»).

### Ресурсы и промпты

- Ресурс `kn://context/current` — тот же пакет, что `kn_context` без `task`.
- Ресурс `kn://entry/{id}` — запись по id.
- Промпт `save-session` — шаблон «подведи итоги сессии и сохрани решения/уроки».

## 3. Общая инструкция агенту

`adapters/common/AGENTS.snippet.md` — вставляется в `CLAUDE.md`, `AGENTS.md`, Cursor rules:

```markdown
## База знаний (kn)

- В начале задачи вызови `kn_context` с кратким описанием задачи (если контекст не был добавлен автоматически).
- Перед тем как принимать архитектурное решение или выбирать подход — `kn_search`: возможно, решение уже есть.
- Сохраняй только то, что пригодится в будущих сессиях:
  - решение с альтернативами → `kn_record_decision`;
  - грабли, на которые наступил, и как их обойти → `kn_save` kind=lesson;
  - устойчивое правило проекта → `kn_save` kind=rule;
  - проверенный факт о системе → `kn_save` kind=fact.
- Не сохраняй: секреты, токены, персональные данные, сырые логи, то, что очевидно из кода или git-истории.
- Если похожая запись уже есть — обнови её (`kn_update`), а не создавай новую.
- В конце значимой сессии — `kn_log_session` с итогами.
```

## 4. Адаптеры

### Claude Code (`adapters/claude-code/`)

MCP-сервер (уровень пользователя, чтобы работал во всех проектах):

```bash
claude mcp add knowledge --scope user -- /path/to/knowledge/scripts/kn-mcp-launch claude-code personal
```

Хук `SessionStart` — stdout хука добавляется в контекст сессии, поэтому контекст приходит без вызова инструмента:

```json
{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          { "type": "command", "command": "KN_PRINCIPAL=claude-code@personal kn context --repo-from-cwd --hook --max-tokens 2700" }
        ]
      }
    ]
  }
}
```

> **Почему у хука 2700 токенов, а не 10 000.** Claude Code, по сообщениям пользователей (официально не подтверждено),
> обрезает вывод хука примерно до 10 000 символов: всё сверх этого модель видит только как превью ~2 КБ и путь к файлу.
> 2700 токенов — это ≈ 9400 символов, с запасом. Полный контекст на 10 000 токенов агент берёт вызовом `kn_context`
> (ответ MCP: предупреждение Claude Code после 10 000 токенов, предел по умолчанию — 25 000, `MAX_MCP_OUTPUT_TOKENS`).

Плюс фрагмент `CLAUDE.md` из §3.

### Cursor (`adapters/cursor/`)

`~/.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "knowledge": {
      "command": "/path/to/knowledge/scripts/kn-mcp-launch",
      "args": ["cursor", "work"]
    }
  }
}
```

Инструкция — в `.cursor/rules/knowledge.mdc` (`alwaysApply: true`), содержимое из §3. Автоконтекст — через инструкцию «вызови `kn_context` первым».

### Codex (`adapters/codex/`)

`~/.codex/config.toml`:

```toml
[mcp_servers.knowledge]
command = "/path/to/knowledge/scripts/kn-mcp-launch"
args = ["codex", "work"]
```

Инструкция — в `~/.codex/AGENTS.md` или `AGENTS.md` проекта.

### bb (`adapters/bb/`)

Ядро про bb ничего не знает; bb — один из клиентов.

- У каждого профиля bb (`personal`, `work`) — свой principal и токен: `bb@personal`, `bb@work`.
- bb передаёт агентам, которые запускает, конфиг `kn-mcp` с токеном своего профиля — так агент в Work-профиле физически не может прочитать Personal.
- Для собственных нужд оркестрации (передать контекст между агентами, сохранить итог пайплайна) bb ходит в REST напрямую через `kn`-клиент.
- Конкретная схема зависит от того, как устроены конфиги и запуск агентов в bb — уточняется на шаге интеграции.

### ChatGPT (`adapters/chatgpt/`)

ChatGPT подключается только к удалённым MCP-серверам (OAuth или без авторизации), поэтому используется
OpenAI Secure MCP Tunnel: `scripts/kn-chatgpt-tunnel` запускает `tunnel-client`, который ходит к OpenAI только исходящими
соединениями и стартует `kn-mcp-launch chatgpt personal` через stdio. Токен `chatgpt@personal` — без доступа к `work`,
так как всё прочитанное уходит в OpenAI. Подробности — [adapters/chatgpt/README.md](../adapters/chatgpt/README.md).

### Агенты без MCP

`kn` CLI покрывает те же операции (`kn context`, `kn search`, `kn save --kind lesson ...`, `kn get <id>`) и пригоден для
shell-инструментов, скриптов и хуков. Транспорт — тот же REST, тот же токен из Keychain.

## 5. `scripts/kn-mcp-launch`

Обёртка, чтобы токены не лежали в конфигах инструментов:

```bash
#!/usr/bin/env bash
# kn-mcp-launch <agent> <profile>
set -euo pipefail
agent="$1"; profile="$2"
export KN_API_URL="${KN_API_URL:-http://127.0.0.1:8787}"
export KN_AGENT="$agent"
export KN_TOKEN="$(security find-generic-password -s "kn-token" -a "${agent}@${profile}" -w)"
exec kn-mcp
```

## 6. Obsidian

Obsidian — дополнительный интерфейс, а не источник истины.

**Фаза 1 — экспорт только для чтения** (`kn export --format obsidian --space personal --out ~/Vault/Knowledge`):

```
Knowledge/
├── _README.md                     # «папка генерируется, правки будут перезаписаны»
├── billing-service/
│   ├── decision/Ретраи вебхуков — экспоненциальный backoff.md
│   └── lesson/...
└── _no-project/...
```

```markdown
---
kn_id: 0193b2c4-...
kn_version: 3
kind: decision
status: active
space: work
project: work/billing-service
tags: [webhooks, reliability]
updated_at: 2026-10-02T09:14:00Z
---
# Ретраи вебхуков: экспоненциальный backoff, максимум 8 попыток

...

## Связи
- depends_on: [[Политика ретраев для вебхуков]]
```

**Фаза 2 — двусторонняя синхронизация** (`tools/obsidian-sync`):

- Изменённые файлы (по mtime и хэшу) отправляются как `PATCH` с `If-Match: kn_version`.
- Конфликт версий → рядом создаётся `<имя>.conflict.md` с серверной версией, ничего не перезаписывается молча.
- Новые файлы без `kn_id` в папке `inbox/` → `POST /v1/entries` как `note` от principal `me`.

## Knowledge Skills (ADR-14)

Одобренные правила, закреплённые записи и решения проекта можно положить в репозиторий файлом, который клиент читает
сам, без вызова `kn_context`:

```sh
cd ~/Code/billing
kn skill export                  # .claude/skills/kn-<slug>/SKILL.md, .cursor/rules/kn-<slug>.mdc, секция в AGENTS.md
kn skill export --format claude  # только навык Claude Code
kn skill export --check          # код 1, если файлы устарели (для CI или pre-commit)
```

Файлы детерминированные (без дат): изменились знания — изменился файл. В `AGENTS.md` переписывается только секция
между маркерами `<!-- kn:begin … -->` и `<!-- kn:end -->`. Записи «на проверке» в навык не попадают. Править
навык руками бессмысленно — следующий экспорт перезапишет; исправления — в kn.
