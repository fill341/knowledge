# Claude Code

Подключение на уровне пользователя — работает во всех проектах. `<profile>` — `personal` или `work`.
`/ABS/knowledge` — абсолютный путь к клону репозитория (`bin/` обычно не в `PATH`, поэтому пути абсолютные).

## 1. Токен

```bash
make token PRINCIPAL=claude-code@personal SCOPES="space:personal:write space:shared:read"
```

## 2. MCP-сервер

```bash
claude mcp add knowledge --scope user -- /ABS/knowledge/scripts/kn-mcp-launch claude-code personal
```

`kn-mcp` определяет проект по `git remote origin` текущего репозитория. Чтобы репозиторий распознавался,
зарегистрируй его один раз:

```bash
cd ~/Code/billing && kn project create personal/billing --name "Billing" --repo-from-cwd
```

## 3. Автоматический контекст при старте сессии

Хук `SessionStart`: всё, что он пишет в stdout, добавляется в контекст сессии. Добавь в `~/.claude/settings.json`
(см. `settings.hooks.json`):

```json
{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          { "type": "command", "timeout": 15, "command": "KN_PRINCIPAL=claude-code@personal /ABS/knowledge/bin/kn context --repo-from-cwd --hook --max-tokens 2700" }
        ]
      }
    ]
  }
}
```

`--hook` никогда не роняет старт сессии: если API недоступен или токена нет, хук молча ничего не выводит
(причина пишется в stderr).

## 4. Инструкция агенту

Добавь содержимое [`../common/AGENTS.snippet.md`](../common/AGENTS.snippet.md) в `~/.claude/CLAUDE.md`.

## Проверка

```bash
claude mcp list            # knowledge: ✓ connected
```

В сессии: «найди в базе знаний решения по ретраям» → агент вызывает `kn_search`.
