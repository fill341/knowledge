# bb

Ядро ничего не знает о bb: для него bb — обычный клиент со своими токенами.

## Принцип

- У каждого профиля bb свой principal и токен:

  ```bash
  make token PRINCIPAL=bb@personal SCOPES="space:personal:write space:shared:read"
  make token PRINCIPAL=bb@work     SCOPES="space:work:write space:shared:read"
  ```

- Агентам, которых bb запускает в профиле, передаётся MCP-конфиг с **токеном этого профиля** —
  например через `kn-mcp-launch <agent> <profile>`. Тогда агент Work-профиля физически не может прочитать Personal.
- Для собственных нужд оркестрации (передать контекст между агентами, сохранить итог пайплайна) bb ходит
  в REST напрямую — через `kn` CLI или Go-клиент `pkg/kclient`:

  ```bash
  KN_PRINCIPAL=bb@work kn context --project work/billing --task "..." --format json
  KN_PRINCIPAL=bb@work kn save --project work/billing --kind note --title "Итог пайплайна" --body - --source-ref "bb:run/123" < summary.md
  ```

## Что осталось уточнить

Конкретная схема зависит от того, как в bb устроены профили и откуда агенты получают MCP-конфиги.
Это открытый вопрос из [docs/mvp-plan.md](../../docs/mvp-plan.md).
