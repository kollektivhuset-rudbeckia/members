# Everyday commands. Run `make` on its own to see them.

.DEFAULT_GOAL := help
.PHONY: help demo demo-docker demo-stop run import import-dry test race vet fmt check build image clean

BINARY  := members
IMAGE   := members-rudbeckia
COMPOSE := docker compose -f docker-compose.demo.yml

help: ## Show this help
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[1m%-12s\033[0m %s\n", $$1, $$2}'

demo: ## Try the register out: an invented association, no Google, needs only Go
	@echo "→ http://localhost:8080   pick a role on the sign-in page"
	@DB_PATH=$${DB_PATH:-data/demo.db} go run ./cmd/server -demo

demo-docker: ## Same, but in a container, with nothing installed but Docker
	@echo "→ http://localhost:8080   pick a role on the sign-in page"
	@$(COMPOSE) up --build

demo-stop: ## Stop and remove the demo container
	@$(COMPOSE) down

run: ## Run against your own .env and config.yaml
	go run ./cmd/server

import-dry: ## Rehearse the migration: read the board's contacts, write nothing
	go run ./cmd/server -import -dry-run

import: ## Do the migration. Set JOINED to the day to record, e.g. JOINED=2026-01-01
	@test -n "$(JOINED)" || { echo "Set JOINED=YYYY-MM-DD — see 'make import-dry' first"; exit 1; }
	go run ./cmd/server -import -import-joined=$(JOINED)

test: ## Run the tests
	go test ./...

race: ## Run the tests with the race detector
	go test -race -count=1 ./...

vet: ## Static checks
	go vet ./...

fmt: ## Format every Go file
	gofmt -w .

check: fmt vet race ## Format, vet and test — run this before pushing
	@go run ./cmd/server -check-config

build: ## Build the binary into ./members
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o $(BINARY) ./cmd/server

image: ## Build the container image locally, tagged :local and :latest
	docker build -t $(IMAGE):local -t $(IMAGE):latest .
	@docker images --format '  {{.Repository}}:{{.Tag}}  {{.Size}}' $(IMAGE)

clean: ## Remove build output and the local databases
	rm -rf $(BINARY) data/
