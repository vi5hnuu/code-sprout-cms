SHELL := /bin/bash
BIN   := $(CURDIR)/bin

# Load .env when present so make targets and the binaries share one config source.
ifneq (,$(wildcard .env))
include .env
export
endif

DATABASE_URL ?= postgres://sprout:sprout@localhost:5442/sprout?sslmode=disable
MIGRATIONS   := db/migrations

.PHONY: build api worker run-api run-worker test lint generate \
        infra-up infra-down migrate migrate-down migrate-status migration

build: api worker

api:
	@mkdir -p $(BIN)
	go build -o $(BIN)/sprout-api ./cmd/api

worker:
	@mkdir -p $(BIN)
	go build -o $(BIN)/sprout-worker ./cmd/worker

run-api:
	go run ./cmd/api

run-worker:
	go run ./cmd/worker

test:
	go test ./... -race -count=1

lint:
	go vet ./...

# sqlc turns db/queries/*.sql into type-safe Go in internal/platform/db/sqlc.
generate:
	sqlc generate

# ── Infra ─────────────────────────────────────────────────────────────────────

# Postgres + Redis always; object storage and search are opt-in:
#   make infra-up PROFILES="storage search"
PROFILES ?=
COMPOSE  := docker compose -f docker-compose.dev.yml $(foreach p,$(PROFILES),--profile $(p))

infra-up:
	$(COMPOSE) up -d --wait

infra-down:
	$(COMPOSE) down

# ── Migrations (goose, plain SQL) ─────────────────────────────────────────────

migrate:
	goose -dir $(MIGRATIONS) postgres "$(DATABASE_URL)" up

migrate-down:
	goose -dir $(MIGRATIONS) postgres "$(DATABASE_URL)" down

migrate-status:
	goose -dir $(MIGRATIONS) postgres "$(DATABASE_URL)" status

# make migration name=add_quiz_tables
migration:
	goose -dir $(MIGRATIONS) -s create $(name) sql
