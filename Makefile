COMPOSE := docker compose -f deploy/docker-compose.yml
SECRETS := deploy/secrets
BIN     := $(CURDIR)/bin
PG_PORT ?= 5433
HOST_OS   := $(shell uname -s | tr A-Z a-z)
HOST_ARCH := $(shell uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')

.PHONY: help setup connect secrets up down restart ui ui-login ui-build ui-dev logs ps migrate build install test test-unit lint \
        admin-token token backup restore restore-test

help: ## Show targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-14s %s\n", $$1, $$2}'

setup: up build admin-token ## First run: start the stack, build kn/kn-mcp, mint your admin token
	@echo
	@echo "Ready. Add kn to PATH:  export PATH=\"$(BIN):\$$PATH\"   (and put it into ~/.zshrc)"
	@echo "Connect an agent:       make connect AGENT=claude-code   (cursor, codex)"
	@echo "Open the web UI:        make ui"

secrets: ## Generate database passwords in deploy/secrets (only if missing)
	@mkdir -p $(SECRETS)
	@for f in pg_owner_password pg_app_password ui_session_key; do \
	  if [ ! -s $(SECRETS)/$$f ]; then openssl rand -base64 33 | tr -d '/+=\n' > $(SECRETS)/$$f; echo "created $(SECRETS)/$$f"; fi; \
	done
	@chmod 600 $(SECRETS)/pg_* $(SECRETS)/ui_session_key

up: secrets ## Build and start the stack (postgres → migrate → knowledge-api, web-ui, backup)
	$(COMPOSE) up -d --build --wait postgres knowledge-api web-ui backup

down: ## Stop the stack (data volume is kept)
	$(COMPOSE) down

restart: ## Rebuild and restart the API and the web UI
	$(COMPOSE) up -d --build --wait knowledge-api web-ui

ui: ## Open the web UI already signed in (kn ui)
	@$(BIN)/kn ui --ui-url http://127.0.0.1:$${KN_UI_PORT:-8790}

ui-login: ui

ui-build: ## Build the frontend into the kn-ui embed directory (for running kn-ui outside Docker)
	cd services/web-ui/web && npm ci --no-audit --no-fund && npm run build

ui-dev: ## Frontend dev server with hot reload on :5173 (needs the stack running)
	cd services/web-ui/web && npm run dev

logs: ## Follow service logs
	$(COMPOSE) logs -f --tail=100

ps: ## Show service status
	$(COMPOSE) ps

migrate: ## Apply migrations
	$(COMPOSE) run --rm migrate

build: ## Build kn and kn-mcp into ./bin (with local Go, or in Docker when Go is not installed)
	@mkdir -p $(BIN)
	@if command -v go >/dev/null 2>&1 && [ -z "$(GO_DOCKER)" ]; then \
	  (cd cli/kn && go build -o $(BIN)/kn .) && (cd services/mcp-server && go build -o $(BIN)/kn-mcp ./cmd/kn-mcp); \
	else \
	  echo "building kn and kn-mcp in Docker for $(HOST_OS)/$(HOST_ARCH)…"; \
	  docker run --rm -v "$(CURDIR)":/src -v kn-gocache:/root/go -e GOOS=$(HOST_OS) -e GOARCH=$(HOST_ARCH) -e CGO_ENABLED=0 \
	    -w /src golang:1.27-alpine sh -c 'cd cli/kn && go build -buildvcs=false -o /src/bin/kn . && cd ../../services/mcp-server && go build -buildvcs=false -o /src/bin/kn-mcp ./cmd/kn-mcp'; \
	fi

install: ## Install kn and kn-mcp into $GOBIN (or ~/go/bin)
	cd cli/kn && go install .
	cd services/mcp-server && go install ./cmd/kn-mcp

test: ## Unit and integration tests (integration needs `make up`)
	cd services/knowledge-api && KN_TEST_DB_URL="postgres://kn_owner:$$(cat ../../$(SECRETS)/pg_owner_password)@127.0.0.1:$(PG_PORT)/knowledge?sslmode=disable" \
	  KN_TEST_APP_PASSWORD="$$(cat ../../$(SECRETS)/pg_app_password)" go test -count=1 ./...
	cd services/mcp-server && go test -count=1 ./...
	cd services/web-ui && go test -count=1 ./...
	cd cli/kn && go test -count=1 ./...

test-unit: ## Unit tests only
	cd services/knowledge-api && go test -count=1 ./...
	cd services/mcp-server && go test -count=1 ./...

lint: ## go vet all modules and lint the OpenAPI contract
	cd api && npx -y @redocly/cli@1.34.5 lint openapi.yaml --config redocly.yaml
	cd services/knowledge-api && go vet ./...
	cd services/mcp-server && go vet ./...
	cd services/web-ui && go vet ./...
	cd services/web-ui/web && npm run typecheck
	cd pkg/kclient && go vet ./...
	cd cli/kn && go vet ./...

admin-token: ## Mint the first admin token for "me" and store it in the Keychain (skipped if a working one is already there)
	@if [ -x $(BIN)/kn ] && $(BIN)/kn me >/dev/null 2>&1; then echo "admin token for 'me' is already in Keychain and works"; exit 0; fi; \
	tok=$$($(COMPOSE) exec -T knowledge-api /kn-api token create --principal me --scope admin --name bootstrap) && \
	  security add-generic-password -U -s kn-token -a me -w "$$tok" && echo "admin token for 'me' stored in Keychain (service kn-token)"

connect: ## Connect an AI tool: make connect AGENT=claude-code|cursor|codex [PROFILE=personal]
	@test -n "$(AGENT)" || (echo 'usage: make connect AGENT=claude-code|cursor|codex [PROFILE=personal]' && exit 2)
	$(BIN)/kn connect $(AGENT) --profile $${PROFILE:-personal}

token: ## Issue an agent token: make token PRINCIPAL=claude-code@work SCOPES="space:work:write space:shared:read"
	@test -n "$(PRINCIPAL)" -a -n "$(SCOPES)" || (echo 'usage: make token PRINCIPAL=... SCOPES="..."' && exit 2)
	$(BIN)/kn token create $(PRINCIPAL) $(SCOPES) --keychain

backup: ## Make a backup now
	$(COMPOSE) exec -T backup /bin/sh /backup.sh once

restore: ## Restore a dump: make restore DUMP=deploy/backups/knowledge-....dump
	@test -n "$(DUMP)" || (echo 'usage: make restore DUMP=path' && exit 2)
	scripts/restore.sh "$(DUMP)"

restore-test: ## Restore the latest dump into a throwaway container and compare row counts
	scripts/restore-test.sh
