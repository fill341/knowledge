# Contributing

Thanks for looking! kn is a small project; issues and pull requests are welcome.

## Setup

```bash
make setup     # Docker stack + kn/kn-mcp binaries + your admin token
make test      # unit and integration tests (integration tests create and drop their own database)
make lint      # go vet, OpenAPI lint, TypeScript typecheck
```

Frontend with hot reload: `make ui-dev` (http://127.0.0.1:5173, needs the stack running).

## Ground rules

- **Never experiment on your working knowledge base.** The service has no `DELETE` on entries, so test data stays
  forever. Integration tests use a throwaway database; for manual end-to-end checks create one too
  (see [docs/project-knowledge.md](docs/project-knowledge.md), rule «Тестовые записи не писать в рабочую базу»).
- **API first.** `api/openapi.yaml` is the contract; server, client and SQL are written by hand, no codegen.
- **Agents never touch the database** — only the API. New features go through the API and are covered by tests.
- Keep the UI bilingual: every string goes through `src/i18n.tsx` with both `en` and `ru`.
- Commits: imperative mood, one logical change per commit.

## Where to start

- [docs/architecture.md](docs/architecture.md) — components and key decisions (in Russian);
- [docs/project-knowledge.md](docs/project-knowledge.md) — decisions, rules and lessons accumulated while building kn;
- [docs/roadmap.md](docs/roadmap.md) — what is done and what is next.
