# Cursor

## 1. Токен

```bash
make token PRINCIPAL=cursor@personal SCOPES="space:personal:write space:shared:read"
```

## 2. MCP-сервер

`~/.cursor/mcp.json` (глобально) или `.cursor/mcp.json` в проекте — см. [`mcp.json`](mcp.json).
Путь к `kn-mcp-launch` укажи абсолютный, второй аргумент — профиль токена (`personal` или `work`).

## 3. Инструкция агенту

Скопируй [`knowledge.mdc`](knowledge.mdc) в `.cursor/rules/` проекта или вставь его текст (без шапки `---`) в
Settings → Rules → User Rules — тогда правило действует во всех проектах. User Rules хранятся в облаке аккаунта Cursor:
снаружи их не записать, только в Settings или попросив агента Cursor.
У Cursor нет хука старта сессии, поэтому правило просит агента первым делом вызвать `kn_context`.
