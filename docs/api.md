# API-контракты (REST v1)

Источником истины станет `api/openapi.yaml` (первый шаг MVP). Этот документ фиксирует контракт, по которому он пишется.

## 1. Общие соглашения

| Аспект | Соглашение |
|--------|------------|
| Базовый URL | `http://127.0.0.1:8787/v1` |
| Формат | JSON, UTF-8. Время — RFC 3339 UTC. ID — UUID (v7). |
| Аутентификация | `Authorization: Bearer kn_<prefix>_<secret>` на всех эндпоинтах, кроме `/healthz`, `/readyz`. |
| Адресация проекта | `"<space>/<project-slug>"`, например `"work/billing-service"`. |
| Пагинация | Курсорная: `?limit=50&cursor=<opaque>` → `{"items": [...], "next_cursor": "..."}`. Максимум `limit` — 200. |
| Конкурентность | Ответы с записью содержат `ETag: "<version>"`. `PATCH` требует `If-Match: "<version>"`, иначе `428`; несовпадение — `412`. |
| Идемпотентность | `POST` принимает `Idempotency-Key` (рекомендуется агентам). Повтор с тем же ключом и телом возвращает сохранённый ответ; с другим телом — `422`. |
| Ошибки | RFC 9457 `application/problem+json` с полем `code` (см. §6). |
| Права | Scope'ы вида `space:<slug>:<level>`, где level ∈ `read` < `write` < `review` < `admin`; плюс глобальный `admin`. Подробно — [security.md](security.md). |
| Видимость | Все списки и поиск автоматически ограничены пространствами, доступными токену на чтение. Запрос к записи из недоступного пространства — `404` (не раскрываем факт существования). |

## 2. Эндпоинты

### Служебные

| Метод | Путь | Права | Описание |
|-------|------|-------|----------|
| GET | `/healthz` | — | Процесс жив |
| GET | `/readyz` | — | БД доступна, миграции применены |
| GET | `/v1/me` | любой токен | Principal, scope'ы, доступные пространства |

### Пространства и проекты

| Метод | Путь | Права | Описание |
|-------|------|-------|----------|
| GET | `/v1/spaces` | read | Пространства, доступные токену |
| GET | `/v1/kinds` | любой токен | Типы записей и их JSON Schema |
| GET | `/v1/projects` | read | Список; фильтры `space`, `repo_url`, `include_archived` |
| POST | `/v1/projects` | write в space | Создать проект |
| GET | `/v1/projects/{space}/{slug}` | read | Проект + статистика записей |
| PATCH | `/v1/projects/{space}/{slug}` | write | Изменить (имя, описание, `repo_urls`) |
| POST | `/v1/projects/resolve` | read | `{repo_url}` → проект (автоопределение по `git remote` или по пути `file:///…` для репозиториев без remote; пути сохраняют регистр) |

### Записи

| Метод | Путь | Права | Описание |
|-------|------|-------|----------|
| GET | `/v1/entries` | read | Список; фильтры `space`, `project`, `kind`, `status`, `tag`, `pinned`, `updated_since` |
| POST | `/v1/entries` | write | Создать (проходит конвейер качества, §5) |
| GET | `/v1/entries/{id}` | read | Текущая версия + ссылки + `usage` за 30 дней (`shown`, `delivered`, `opened`, `last_day`). Сам вызов считается полным открытием (ADR-9) |
| PATCH | `/v1/entries/{id}` | write | Частичное изменение, `If-Match` обязателен |
| POST | `/v1/entries/{id}/transition` | write / review | Смена статуса: `{to, superseded_by?, note?}` |
| POST | `/v1/entries/{id}/promote` | write в обоих space | Копия в другое пространство со связью `derived_from` |
| GET | `/v1/entries/{id}/versions` | read | Список версий |
| GET | `/v1/entries/{id}/versions/{n}` | read | Конкретная версия |
| POST | `/v1/entries/{id}/revert` | write | Новая версия с содержимым версии `n`: `{to_version}` |
| GET | `/v1/entries/{id}/links` | read | Входящие и исходящие связи |
| GET | `/v1/entries/{id}/link-suggestions` | read | До 5 предложений связей: живые решения, правила, уроки и факты того же пространства, связанные по заголовку и сути; уже связанные исключены. Только предложения — связь ставит агент (`kn_link`) или человек. При создании решения, правила, урока или факта то же приходит предупреждением `suggested_links` |
| GET | `/v1/entries/{id}/impact` | read | Анализ влияния: живые видимые записи, опирающиеся на эту через `depends_on` / `references` / `derived_from`, до 3 шагов (`depth`, `rel`, `via`) |
| POST | `/v1/entries/{id}/links` | write | `{to_id, rel}` |
| DELETE | `/v1/entries/{id}/links/{to_id}/{rel}` | write | Удалить связь |

`DELETE /v1/entries/{id}` намеренно отсутствует: удаление = `transition` в `archived`.

### Поиск и контекст

| Метод | Путь | Права | Описание |
|-------|------|-------|----------|
| POST | `/v1/search` | read | Полнотекстовый поиск с фильтрами (§3) |
| POST | `/v1/context` | read | Пакет контекста для агента под бюджет токенов (§4); `?format=markdown` — сразу текст для хуков |

### Сессии агентов

| Метод | Путь | Права | Описание |
|-------|------|-------|----------|
| POST | `/v1/sessions` | write | Начать: `{agent, project?, repo_url?, client_info}` → `{id}` |
| POST | `/v1/sessions/{id}/end` | write | Завершить: `{summary, goal, status, done[], commits[], open_questions[], next_steps[], continues[], outcomes[], files_touched[]}` → записи из `outcomes` и структурированный `session_log`. Статус `in_progress`/`blocked` (по умолчанию `in_progress`, если есть `next_steps`) — незавершённая работа: её видит следующий агент проекта в `kn_context`, пока поздний итог не укажет её в `continues` (ADR-11) |

### Review

| Метод | Путь | Права | Описание |
|-------|------|-------|----------|
| GET | `/v1/review-queue` | review | Записи в `proposed` + активные записи с прошедшим сроком перепроверки (`review_after`, иначе последнее изменение + `review_days` типа, ADR-12; одобрение продлевает срок); `duplicate_of` — живые записи пространства с тем же текстом |
| POST | `/v1/entries/{id}/review` | review | `{action: approve\|reject\|request_changes, comment?}` → `active` / `archived` / `draft` |

### Здоровье базы (ADR-8)

| Метод | Путь | Права | Описание |
|-------|------|-------|----------|
| GET | `/v1/health?project=&limit=&lang=` | read | Отчёт: дубли по содержимому, почти-дубли, противоречия, опора на устаревшие знания, сломанные замены, просроченная перепроверка, очередь проверки, записи без связей, неполные записи, пустые поиски, незакрытые сессии, невостребованные знания (после 14 дней сбора статистики использования), риски доступа (только админ). Только чтение: исправления — существующими переходами и связями. Язык текстов (en/ru) — из `lang`, иначе из первого тега `Accept-Language` (`ru*` — русский, иначе английский); kn и kn-mcp шлют `KN_LANG`, по умолчанию `ru` |
| GET | `/v1/stats` | read | Счётчики по пространствам, статусам, типам, очередь проверки |
| GET | `/v1/health/history?days=` | admin | Ежедневные снимки отчёта о здоровье всей базы: балл и счётчики проверок (ADR-13). Снимок делает API раз в день сам |
| GET | `/v1/projects/{space}/{slug}/skill?format=claude\|cursor\|agents` | read | Knowledge Skill (ADR-14): одобренные правила и решения проекта файлом для агента; детерминированно, `kn skill export` пишет его в репозиторий |
| POST | `/v1/eval` | read | Оценка поиска на размеченном наборе запросов: recall@5, recall@k, MRR, доля пустых, попадание в контекст. Тот же код поиска и контекста, что у агентов; в аудит пишется одно событие `eval`, а не поиски. См. [eval/README.md](../eval/README.md) |
| GET | `/v1/graph` | read | Граф для карты знаний: записи (с `mass` — объёмом текста, `review_due` — пора перепроверить, `used` — сколько раз выдана агентам или открыта за 90 дней), связи между видимыми записями, неявные связи `affinity` (близость по словам, тегам и источнику, считается при чтении), проекты, `activity` — почасовая активность проектов (правки записей и сессии агентов) для гало активности и плеера эволюции; у записей и связей — `created_at` |

### Экспорт / импорт

| Метод | Путь | Права | Описание |
|-------|------|-------|----------|
| GET | `/v1/export?space=&format=markdown\|jsonl&include_history=` | read | `tar.gz` (markdown) или `application/x-ndjson` (поток) |
| POST | `/v1/import` | write | JSONL; `mode: create_only \| upsert_by_id`; `dry_run`; `skip_duplicate_content` — пропускать новые записи с текстом, который уже есть в пространстве (в отчёте `duplicates[]` они перечислены всегда) |

### Администрирование

| Метод | Путь | Права | Описание |
|-------|------|-------|----------|
| GET / POST | `/v1/admin/principals` | admin | Список / создание principal |
| GET / POST | `/v1/admin/tokens` | admin | Список (без секретов) / выпуск (секрет показывается один раз) |
| POST | `/v1/admin/tokens/{id}/revoke` | admin | Отзыв |
| GET | `/v1/admin/audit` | admin | Журнал; фильтры `principal`, `action`, `target_id`, `since` |
| PUT | `/v1/admin/kinds/{kind}` | admin | Добавить/изменить тип записи и его JSON Schema |

Первый admin-токен выпускается не через API, а командой в контейнере: `kn-api token create --principal me --scope admin`.

## 3. Поиск

```http
POST /v1/search
```
```json
{
  "q": "ретраи отправки вебхуков",
  "spaces": ["work", "shared"],
  "project": "work/billing-service",
  "kinds": ["decision", "lesson"],
  "status": ["active", "proposed"],
  "tags": ["webhooks"],
  "mode": "fts",
  "limit": 20,
  "cursor": null
}
```

- `project` поднимает записи проекта в выдаче (×1.5); `project_only: true` оставляет только их.
- Все поля кроме `q` необязательны. `spaces` по умолчанию — все доступные токену; недоступное пространство в списке → `403`.
- `q` разбирается `websearch_to_tsquery('russian', q)` (поддерживает `"фразы"`, `-исключение`, `or`): должны совпасть все слова.
- Если так найдено меньше 3 записей (обычно это запрос-фраза на естественном языке), первая страница дополняется:
  1. до 3 записей с похожим заголовком (триграммы);
  2. записями, где есть хотя бы одно слово запроса. Они ранжируются по доле совпавших слов в квадрате; слово, найденное
     только в теле, считается за половину, чтобы длинные документы не вытесняли записи по теме. Запросы с
     операторами (`"…"`, `-слово`, `or`) так не расширяются.

  Порядок и веса выбраны по `kn eval` (см. [roadmap.md](roadmap.md), этап 4).
- Ранжирование: `ts_rank_cd(search_tsv, query)` × вес статуса (`active` 1.0, `proposed` 0.7, `deprecated` 0.3) × затухание по давности (полупериод 180 дней) × 1.5 если запись из запрошенного проекта.
- `mode: "hybrid"` зарезервирован для фазы 3; до этого → `422 unsupported_mode`.

```json
{
  "items": [
    {
      "id": "0193b2c4-...",
      "space": "work",
      "project": "work/billing-service",
      "kind": "decision",
      "title": "Ретраи вебхуков: экспоненциальный backoff, максимум 8 попыток",
      "status": "active",
      "version": 3,
      "tags": ["webhooks", "reliability"],
      "score": 0.82,
      "highlight": "…используем <b>экспоненциальный</b> backoff с jitter…",
      "updated_at": "2026-10-02T09:14:00Z"
    }
  ],
  "next_cursor": null
}
```

## 4. Формирование контекста

Главный эндпоинт для агентов: один вызов в начале сессии вместо многих поисков.

```http
POST /v1/context
```
```json
{
  "project": "work/billing-service",
  "repo_url": "git@github.com:acme/billing-service.git",
  "task": "добавить ретраи в отправку вебхуков",
  "spaces": ["work", "shared"],
  "max_tokens": 10000,
  "sections": ["rules", "decisions", "lessons", "related"],
  "format": "markdown"
}
```

- Задаётся `project` или `repo_url` (резолвится по `projects.repo_urls`). Без обоих — контекст только из пространственных правил и поиска по `task`.
- `max_tokens` по умолчанию 10000 (хуку старта сессии — 2700, см. ниже). `task` необязателен: без него — режим старта сессии.
- `sections` ограничивает состав: `rules`; `decisions` и `lessons` — эти типы; `related` — остальные типы.

**Алгоритм сборки (детерминированный, без LLM, ADR-10):**

1. **rules** — `active` правила и закреплённые записи проекта плюс закреплённые записи уровня пространства (своего
   и `shared`). Идут первыми, целиком, но не больше 35 % бюджета; дальше — кратко, затем в оглавление. Если правила
   проекта не помещаются в 35 % бюджета по умолчанию, отчёт о здоровье предупреждает (`rules_budget`).
2. **handoff** — незавершённая работа проекта: до 3 последних итогов сессий со статусом `in_progress`/`blocked`
   за 30 дней, которые никто не подхватил (`continues`). Целиком, не больше 15 % бюджета.
3. **С задачей — task («По задаче»):** один пул из поиска по задаче внутри проекта, поиска по задаче во всех
   разрешённых пространствах (проект с бустом) и — как слабый сигнал — свежих решений и уроков проекта. Ранжирование —
   слияние рангов `Σ w / (10 + rank)` с поправкой на тип (решение 1,3 · урок 1,2 · факт 1,15 · правило 1,1 · сниппет 1 ·
   заметка 0,9 · документ 0,8 · лог сессии 0,6).
4. **Без задачи — decisions, lessons, facts:** свежие записи проекта; решения не больше 30 % бюджета, уроки — 20 %,
   факты — остаток.
5. **Упаковка:** 90 % бюджета — разделы по порядку; для записи берётся `summary` (или первые ~300 символов `body`),
   полный `body` — только у правил. То, что не поместилось, не выбрасывается: до 15 записей перечисляются в разделе
   **more** («Не поместились — открыть: `kn_get <id>`») в резерве 10 %. `indexed_count` — сколько в оглавлении,
   `omitted_count` — сколько не попало и туда. Оценка токенов — `символы / 3.5` (не зависит от модели).

```json
{
  "project": { "ref": "work/billing-service", "name": "Billing Service" },
  "sections": [
    { "name": "rules", "items": [ { "id": "...", "title": "...", "kind": "rule", "content": "...", "truncated": false } ] },
    { "name": "task", "items": [ ... ] },
    { "name": "more", "items": [ { "id": "...", "title": "...", "kind": "decision", "truncated": true } ] }
  ],
  "markdown": "# Контекст: Billing Service\n\n## Правила\n- ...",
  "token_estimate": 5870,
  "truncated": true,
  "indexed_count": 12,
  "omitted_count": 0
}
```

В `markdown` у каждой записи указан `id`, чтобы агент мог запросить полный текст через `GET /v1/entries/{id}`.

## 5. Создание записи и конвейер качества

```http
POST /v1/entries
Idempotency-Key: 6f1c...
```
```json
{
  "space": "work",
  "project": "work/billing-service",
  "kind": "decision",
  "title": "Ретраи вебхуков: экспоненциальный backoff, максимум 8 попыток",
  "summary": "Backoff 2^n с jitter, потолок 1 ч, после 8 попыток — в DLQ.",
  "body": "## Контекст\n...",
  "attributes": {
    "context": "Партнёры теряли события при кратковременных падениях.",
    "decision": "Экспоненциальный backoff с full jitter, 8 попыток, затем DLQ.",
    "alternatives": ["фиксированный интервал", "бесконечные ретраи"],
    "consequences": "Нужен мониторинг DLQ.",
    "decided_at": "2026-10-02"
  },
  "tags": ["webhooks", "reliability"],
  "confidence": 0.9,
  "session_id": "0193b2a0-...",
  "source_ref": "git:acme/billing-service@4f2a91c",
  "draft": false
}
```

**Конвейер (в одной транзакции, по порядку):**

| # | Проверка | Результат при нарушении |
|---|----------|-------------------------|
| 1 | Scope: `write` в `space` | `403 forbidden_space` |
| 2 | Структура: длины, теги (`^[a-z0-9][a-z0-9-]{0,31}$`, ≤ 10 шт.), проект в том же пространстве | `422 validation_failed` |
| 3 | `attributes` по JSON Schema типа | `422 validation_failed` с путями ошибок |
| 4 | Провенанс: для principal-агента обязателен `session_id` или `source_ref` | `422 provenance_required` |
| 5 | Сканер секретов по `title`, `summary`, `body`, `attributes` | `422 secret_detected` (с позицией, без самого значения) |
| 6 | Точный дубль (`content_hash`) | `409 duplicate` + `existing_id` |
| 7 | Похожие записи: триграммное сходство заголовка ≥ 0.6 в том же проекте и типе | не блокирует → `warnings[]` (`similar_entries`) |
| 7b | Тот же текст под другим заголовком: живая запись пространства с тем же `body_hash` (пробелы схлопнуты, текст ≥ 200 символов) | по умолчанию не блокирует → `warnings[]` (`duplicate_content`); с `"on_duplicate_content": "reject"` → `409 duplicate_content` + `existing_id` |
| 8 | Статус: `draft` если `draft: true`; `proposed` если агент и тип требует review; иначе `active` | — |
| 9 | Запись + версия 1 (триггер) + аудит | — |

```http
201 Created
ETag: "1"
Location: /v1/entries/0193b2c4-...
```
```json
{
  "entry": { "id": "0193b2c4-...", "status": "proposed", "version": 1, "...": "..." },
  "warnings": [
    {
      "code": "similar_entries",
      "message": "Найдены похожие записи — возможно, стоит обновить существующую",
      "items": [ { "id": "0192ff10-...", "title": "Политика ретраев для вебхуков", "similarity": 0.71 } ]
    }
  ]
}
```

### Изменение записи

```http
PATCH /v1/entries/0193b2c4-...
If-Match: "3"
```
```json
{ "summary": "…", "tags": ["webhooks"], "change_note": "уточнил потолок задержки" }
```

К `PATCH` применяются шаги 2–7 конвейера (7b — если меняется текст). Агент с `write` правит `active`-записи напрямую, без повторной проверки — решение владельца базы
(2026-11-17): проверка нужна новым решениям, правилам и фактам, а правка видна в истории и аудите и откатывается
через `revert`. Какие новые записи агентов идут на проверку, задаёт флаг типа «Записи агентов этого типа проходят
проверку» (страница «Типы записей») и `agent_always_review` пространства.

## 6. Ошибки

```json
{
  "type": "https://knowledge.local/problems/version_conflict",
  "title": "Version conflict",
  "status": 412,
  "code": "version_conflict",
  "detail": "Entry was modified: expected version 3, current 4",
  "current_version": 4
}
```

| HTTP | `code` | Когда |
|------|--------|-------|
| 400 | `bad_request` | Невалидный JSON / параметры |
| 401 | `unauthenticated` | Нет токена, токен неизвестен, отозван или истёк |
| 403 | `forbidden_space` / `forbidden` | Нет нужного scope (для явно указанного пространства) |
| 404 | `not_found` | Нет объекта или он в недоступном пространстве |
| 409 | `duplicate` | Точный дубль, в ответе `existing_id` |
| 409 | `invalid_transition` | Недопустимый переход статуса |
| 412 | `version_conflict` | `If-Match` не совпал |
| 422 | `validation_failed`, `secret_detected`, `provenance_required`, `idempotency_mismatch`, `unsupported_mode` | Нарушены правила качества |
| 428 | `precondition_required` | `PATCH` без `If-Match` |
| 429 | `rate_limited` | Защита от зациклившегося агента (по умолчанию 60 записей/мин на токен) |
| 500 | `internal` | Ошибка сервиса (детали — только в логах) |

## 7. Версионирование API

- Путь `/v1`. Обратно совместимые изменения (новые поля, эндпоинты, значения enum в ответах) — без смены версии; клиенты обязаны игнорировать неизвестные поля.
- Несовместимые — `/v2` параллельно с `/v1` до миграции всех адаптеров.
