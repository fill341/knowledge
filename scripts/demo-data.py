#!/usr/bin/env python3
"""Generate a fictional demo knowledge base (JSONL for `kn import`) for screenshots and trying the UI.

Five made-up projects with decisions, rules, facts, lessons, notes, session logs and links between
them. Deterministic: the same file every run. Import it into a throwaway database, never into your
working one (the service cannot delete entries):

  scripts/demo-data.py > /tmp/demo.jsonl
  KN_TOKEN=<admin token of the demo base> kn --url http://127.0.0.1:8799 import /tmp/demo.jsonl
  psql … -d <demo database> < scripts/demo-history.sql   # spread entries, links and sessions over 4 months

The screenshots in docs/screenshots were taken from this dataset.
"""

from __future__ import annotations

import json
import random
import sys
import uuid

rnd = random.Random(42)

PROJECTS = {
    "billing": ("Billing", "Платёжный сервис: подписки, счета, вебхуки провайдеров", [
        ("decision", "Идемпотентность вебхуков по ключу провайдера, а не по хэшу тела", ["webhooks", "payments"]),
        ("decision", "Ретраи исходящих вебхуков: экспонента с потолком 1 час и DLQ", ["webhooks", "retries"]),
        ("decision", "Деньги храним в minor units (bigint), не в numeric", ["money", "postgres"]),
        ("decision", "Outbox-таблица вместо двойной записи в БД и очередь", ["outbox", "postgres", "queue"]),
        ("rule", "Любое изменение схемы платежей — только через обратимую миграцию", ["migrations", "postgres"]),
        ("rule", "Суммы в логах маскируем, номера карт не логируем никогда", ["security", "logging"]),
        ("fact", "Stripe шлёт вебхук повторно до 3 суток при ответе не 2xx", ["webhooks", "stripe"]),
        ("fact", "Таймаут обработчика вебхука у провайдера — 10 секунд", ["webhooks"]),
        ("lesson", "Двойное списание: ретрай клиента до коммита транзакции", ["payments", "retries", "postgres"]),
        ("lesson", "SELECT FOR UPDATE SKIP LOCKED в outbox терял порядок событий одного счёта", ["outbox", "postgres", "queue"]),
        ("lesson", "Часовой пояс в датах продления подписки: считать в UTC, показывать в TZ клиента", ["subscriptions", "time"]),
        ("note", "Карта статусов счёта и разрешённых переходов", ["invoices"]),
        ("snippet", "SQL: найти счета, застрявшие в pending дольше часа", ["invoices", "postgres"]),
        ("decision", "Пересчёт пропорций при смене тарифа — на стороне провайдера", ["subscriptions", "stripe"]),
        ("lesson", "Вебхук пришёл раньше, чем ответ API создания подписки", ["webhooks", "subscriptions"]),
        ("fact", "Лимит API провайдера — 100 запросов в секунду на аккаунт", ["stripe", "limits"]),
        ("rule", "Каждый внешний вызов — с таймаутом и идемпотентным ключом", ["retries", "http"]),
    ]),
    "mobile": ("Mobile app", "Клиент на React Native: офлайн-режим, пуши, синхронизация", [
        ("decision", "Офлайн-первый клиент: локальная SQLite и очередь мутаций", ["offline", "sync", "sqlite"]),
        ("decision", "Конфликты синхронизации: last-write-wins по полю, не по записи", ["sync", "conflicts"]),
        ("decision", "Навигация на react-navigation, без собственного роутера", ["navigation"]),
        ("rule", "Новые экраны — только с состояниями загрузки, пустым и ошибкой", ["ux"]),
        ("rule", "Тексты интерфейса — только через i18n-ключи", ["i18n", "ux"]),
        ("fact", "iOS убивает фоновую синхронизацию через 30 секунд", ["ios", "sync"]),
        ("fact", "FCM доставляет data-push в Doze-режиме только с высоким приоритетом", ["android", "push"]),
        ("lesson", "Hermes и Intl: форматирование дат падало на Android 8", ["android", "time", "i18n"]),
        ("lesson", "Очередь мутаций отправлялась дважды после холодного старта", ["sync", "offline", "retries"]),
        ("lesson", "Пуш открывал не тот экран, если приложение было выгружено", ["push", "navigation"]),
        ("note", "Чек-лист релиза в сторы", ["release"]),
        ("decision", "Фича-флаги с сервера, кэш на устройстве на 24 часа", ["flags"]),
        ("lesson", "Сборка iOS ломалась после обновления Xcode: pod cache", ["ios", "ci"]),
        ("fact", "Размер бандла после Hermes — 6,8 МБ", ["performance"]),
        ("snippet", "Хук useOnline с подпиской на NetInfo", ["offline"]),
    ]),
    "infra": ("Infrastructure", "Kubernetes, Terraform, CI/CD и наблюдаемость", [
        ("decision", "Один кластер на окружение, неймспейс на сервис", ["kubernetes"]),
        ("decision", "Terraform state в S3 с блокировкой в DynamoDB", ["terraform", "aws"]),
        ("decision", "Логи в Loki, метрики в Prometheus, трейсы в Tempo", ["observability"]),
        ("decision", "Секреты — External Secrets из AWS Secrets Manager, не в Git", ["security", "kubernetes", "aws"]),
        ("rule", "Каждый сервис отдаёт /healthz и /readyz и метрики на :9090", ["observability", "kubernetes"]),
        ("rule", "Прод меняется только через PR в infra-репозиторий", ["terraform", "process"]),
        ("fact", "Ноды кластера пересоздаются раз в 14 дней (AMI rotation)", ["kubernetes", "aws"]),
        ("fact", "Бэкапы Postgres — ежедневно, хранение 30 дней, PITR 7 дней", ["postgres", "backups"]),
        ("lesson", "readinessProbe на /healthz убивал поды при медленной БД", ["kubernetes", "postgres"]),
        ("lesson", "Terraform plan показал пересоздание RDS из-за смены параметра шифрования", ["terraform", "postgres", "aws"]),
        ("lesson", "Алерты шумели: порог по p99 без минимального числа запросов", ["observability", "alerts"]),
        ("lesson", "Кэш Docker-слоёв в CI сбрасывался из-за меняющегося ARG", ["ci", "docker"]),
        ("note", "Ранбук: под в CrashLoopBackOff", ["kubernetes", "runbook"]),
        ("note", "Ранбук: заканчивается место на диске Postgres", ["postgres", "runbook"]),
        ("snippet", "kubectl: найти поды с рестартами за последний час", ["kubernetes"]),
        ("decision", "Миграции БД — отдельный Job перед деплоем, не в init-контейнере", ["migrations", "kubernetes", "postgres"]),
    ]),
    "recsys": ("Recommendations", "Рекомендательная система: признаки, обучение, онлайн-скоринг", [
        ("decision", "Двухэтапная схема: кандидаты из ANN, ранжирование бустингом", ["ranking", "ann"]),
        ("decision", "Фичестор на Redis для онлайна и Parquet для обучения", ["features", "redis"]),
        ("decision", "Офлайн-метрика — NDCG@20, онлайн — CTR и удержание 7 дней", ["metrics"]),
        ("rule", "Любая модель в прод — только после A/B с заранее заданной метрикой", ["ab", "process"]),
        ("rule", "Признаки считаются одним кодом для обучения и онлайна", ["features"]),
        ("fact", "Холодный старт: 38 % новых пользователей без истории в первую сессию", ["cold-start"]),
        ("fact", "p99 онлайн-скоринга — 42 мс при 1500 RPS", ["performance", "ranking"]),
        ("lesson", "Утечка таргета: признак популярности считался с будущим окном", ["features", "leakage"]),
        ("lesson", "A/B показал рост CTR и падение удержания — кликбейт в топе", ["ab", "metrics"]),
        ("lesson", "Индекс ANN деградировал без периодической перестройки", ["ann", "performance"]),
        ("note", "Список признаков ранжирования и их источники", ["features"]),
        ("decision", "Разнообразие выдачи через MMR после ранжирования", ["ranking", "diversity"]),
        ("lesson", "Разные версии sklearn при обучении и скоринге дали сдвиг предсказаний", ["features", "python"]),
        ("snippet", "Подсчёт NDCG@k на pandas", ["metrics", "python"]),
    ]),
    "site": ("Website", "Маркетинговый сайт и документация на Next.js", [
        ("decision", "Статическая генерация с ревалидацией раз в 10 минут", ["nextjs", "performance"]),
        ("decision", "Документация в MDX рядом с кодом сайта", ["docs", "nextjs"]),
        ("rule", "Изображения — только через next/image с размерами", ["performance", "nextjs"]),
        ("fact", "LCP главной — 1,4 с на 4G", ["performance"]),
        ("lesson", "Ревалидация не срабатывала за CDN без заголовка Cache-Control", ["nextjs", "cdn"]),
        ("lesson", "Шрифты вызывали CLS: нужен font-display и предзагрузка", ["performance", "fonts"]),
        ("note", "Структура разделов документации", ["docs"]),
        ("decision", "Поиск по документации — Pagefind на этапе сборки", ["docs", "search"]),
        ("lesson", "Ссылки на якоря ломались после переименования заголовков", ["docs"]),
    ]),
}

SHARED = [
    ("rule", "Коммиты — в повелительном наклонении, одна логическая правка на коммит", ["git", "process"]),
    ("rule", "Секреты не попадают ни в код, ни в базу знаний, ни в логи", ["security"]),
    ("rule", "Перед архитектурным решением — поиск прошлых решений в kn", ["process", "kn"]),
    ("fact", "Все сервисы пишут логи в JSON с полями trace_id и user_id", ["logging", "observability"]),
    ("lesson", "Postgres: ALTER TABLE ADD COLUMN DEFAULT на большой таблице до PG11 блокировал запись", ["postgres", "migrations"]),
    ("decision", "Postgres — основная БД для всех сервисов, если нет веской причины иначе", ["postgres"]),
]

FILLER = [
    "Проверено на стенде и в проде, поведение воспроизводится стабильно.",
    "Альтернативы обсуждали на ревью; ниже — почему остановились на этом варианте.",
    "Если условия изменятся — пересмотреть и обновить запись, а не создавать новую.",
    "Затрагивает соседние сервисы: перед изменением предупредить их владельцев.",
    "Метрики до и после изменения — на дашборде сервиса.",
    "Связано с инцидентом прошлого квартала; разбор — в постмортеме.",
    "На практике это экономит час-два при каждом похожем расследовании.",
    "Правило появилось после того, как агент дважды наступил на те же грабли.",
]


def uid() -> str:
    return str(uuid.UUID(int=rnd.getrandbits(128), version=4))


def body_for(kind: str, title: str, tags: list[str]) -> tuple[str, dict]:
    n = rnd.choice([1, 2, 2, 3, 4, 6, 9])
    text = " ".join(rnd.choice(FILLER) for _ in range(n))
    topic = ", ".join(tags)
    if kind == "decision":
        attrs = {
            "context": f"Нужно было решить вопрос: {title.lower()}. Темы: {topic}.",
            "decision": title + ".",
            "alternatives": [f"Вариант {c}: отвергнут — {rnd.choice(['сложнее в поддержке', 'дороже', 'хуже по задержке', 'не покрывает краевые случаи'])}"
                             for c in "АБ"[: rnd.randint(1, 2)]],
            "consequences": rnd.choice(FILLER),
        }
        body = (f"## Контекст\n\n{attrs['context']}\n\n## Решение\n\n{title}. {text}\n\n## Альтернативы\n\n"
                + "\n".join(f"- {a}" for a in attrs["alternatives"]) + f"\n\n## Последствия\n\n{attrs['consequences']}")
        return body, attrs
    if kind == "lesson":
        return f"**Симптом.** {title}.\n\n**Причина и обход.** {text}", {}
    if kind == "snippet":
        return f"```sql\n-- {title}\nselect 1;\n```\n\n{text}", {}
    return f"{title}. {text}", {}


def entry(space: str, project: str | None, kind: str, title: str, tags: list[str]) -> dict:
    body, attrs = body_for(kind, title, tags)
    return {
        "type": "entry", "id": uid(), "space": space, "project": project, "kind": kind, "title": title,
        "summary": title + ".", "body": body, "attributes": attrs, "tags": tags, "status": "active",
        "pinned": kind == "rule" and rnd.random() < 0.3,
    }


def main() -> int:
    out = []
    entries: list[dict] = []
    for slug, (name, desc, items) in PROJECTS.items():
        out.append({"type": "project", "space": "personal", "slug": slug, "name": name,
                    "description": desc, "repo_urls": [], "metadata": {}})
        for kind, title, tags in items:
            entries.append(entry("personal", f"personal/{slug}", kind, title, tags))
        for i in range(rnd.randint(2, 4)):
            e = entry("personal", f"personal/{slug}", "session_log",
                      f"Сессия {rnd.choice(['claude-code', 'cursor', 'codex'])}: {rnd.choice(items)[1].lower()}", [])
            entries.append(e)
    for kind, title, tags in SHARED:
        entries.append(entry("shared", None, kind, title, tags))

    links = []
    by_project: dict[str | None, list[dict]] = {}
    for e in entries:
        by_project.setdefault(e["project"], []).append(e)
    seen = set()
    for group in by_project.values():
        knowledge = [e for e in group if e["kind"] != "session_log"]
        for e in knowledge:
            for _ in range(rnd.choice([0, 1, 1, 2])):
                other = rnd.choice(knowledge)
                shared_tags = set(e["tags"]) & set(other["tags"])
                if other is e or not shared_tags or (e["id"], other["id"]) in seen:
                    continue
                seen.add((e["id"], other["id"]))
                rel = "depends_on" if e["kind"] == "decision" and other["kind"] == "decision" else \
                      "derived_from" if e["kind"] == "rule" and other["kind"] == "lesson" else "relates_to"
                links.append({"type": "link", "from_id": e["id"], "to_id": other["id"], "rel": rel})
    # a few cross-project links: shared Postgres knowledge used by services
    pg = [e for e in entries if "postgres" in e["tags"]]
    for _ in range(8):
        a, b = rnd.sample(pg, 2)
        if a["project"] != b["project"] and (a["id"], b["id"]) not in seen:
            seen.add((a["id"], b["id"]))
            links.append({"type": "link", "from_id": a["id"], "to_id": b["id"], "rel": "references"})

    for rec in out + entries + links:
        sys.stdout.write(json.dumps(rec, ensure_ascii=False) + "\n")
    print(f"# {len(out)} projects, {len(entries)} entries, {len(links)} links", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
