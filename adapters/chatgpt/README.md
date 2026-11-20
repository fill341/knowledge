# ChatGPT

ChatGPT подключается только к **удалённым** MCP-серверам (SSE / streaming HTTP) и умеет авторизоваться
только через OAuth или без авторизации — отправить наш Bearer-токен он не может. Поэтому используем
**OpenAI Secure MCP Tunnel**: на Mac работает `tunnel-client`, который сам ходит к OpenAI исходящими
HTTPS-соединениями и запускает `kn-mcp` через stdio. База знаний наружу не открывается, токен остаётся в Keychain.

```
ChatGPT (web) ──▶ OpenAI ◀── tunnel-client (Mac, только исходящие) ──stdio──▶ kn-mcp-launch chatgpt personal ──▶ knowledge-api
```

Документация OpenAI: [Add custom MCP server](https://developers.openai.com/api/docs/guides/custom-mcp-server),
[Secure MCP Tunnels](https://developers.openai.com/api/docs/guides/secure-mcp-tunnels).

## Что важно понимать

- **Данные уходят в OpenAI**: всё, что ChatGPT прочитает через инструменты (контекст, поиск, тексты записей), попадает
  в разговор ChatGPT. Поэтому токен `chatgpt@personal` видит только `personal` (запись) и `shared` (чтение) — `work` ему недоступен.
- **Кто может пользоваться**: туннель привязан к твоей организации OpenAI, а плагин в ChatGPT создаётся «без авторизации».
  В личном аккаунте это только ты. В рабочем workspace ChatGPT туннель и плагин видны по правам workspace — там так не делать.
- **Записи от ChatGPT** проходят те же проверки, что и от других агентов: решения, правила и факты идут на проверку
  в «На проверке»; инструменты записи ChatGPT по умолчанию подтверждает у тебя перед вызовом.
- **У ChatGPT нет репозитория**, поэтому проект он не определит сам: `kn_context` без проекта показывает список
  проектов, а записи без проекта попадают в `personal`. Чтобы работать в проекте, скажи ему, например:
  «@knowledge kn_context с project=personal/billing» — сессия переключится на проект.

## Настройка (один раз)

1. **Токен базы знаний** → Keychain:
   `make token PRINCIPAL=chatgpt@personal SCOPES="space:personal:write space:shared:read"`.
2. **Туннель в OpenAI Platform**: https://platform.openai.com/settings/organization/tunnels → создать туннель,
   запомнить `tunnel_id`. Там же получить runtime API key для `tunnel-client` и положить его в Keychain:
   ```bash
   security add-generic-password -U -s openai-tunnel -a control-plane -w 'sk-...'
   ```
3. **tunnel-client**: скачать из https://github.com/openai/tunnel-client/releases/latest (darwin-arm64 для Apple Silicon) и положить в `bin/` репозитория или в `PATH`.
4. **Профиль туннеля**:
   ```bash
   scripts/kn-chatgpt-tunnel init tunnel_...
   scripts/kn-chatgpt-tunnel doctor
   ```
5. **Плагин в ChatGPT** (веб): https://chatgpt.com/plugins → «+» → **Add custom MCP server** → имя `knowledge` →
   Connection: **Tunnel** → выбрать туннель → Authentication: **No authentication** → принять предупреждение →
   **Create as a plugin** → установить плагин.

## Каждый раз

```bash
scripts/kn-chatgpt-tunnel        # держать запущенным, пока пользуешься базой из ChatGPT
```

В чате: набрать `@` и выбрать плагин `knowledge`, например: «@knowledge что у нас решено про ретраи вебхуков?»

Чтобы ChatGPT сам брал контекст, можно добавить в Custom Instructions: «Для задач по моим проектам сначала
вызывай @knowledge kn_context (с project=<space>/<slug>, если речь о конкретном проекте)».

После изменений `kn-mcp` (`make build`) туннель нужно перезапустить.
