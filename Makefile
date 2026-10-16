SHELL := /bin/bash
BIN   := $(CURDIR)/bin

# Load .env when present so make targets and the binaries share one config source.
ifneq (,$(wildcard .env))
include .env
export
endif

DATABASE_URL ?= postgres://sprout:sprout@localhost:5442/sprout?sslmode=disable
MIGRATIONS   := db/migrations

.PHONY: build api worker run-api run-worker test test-db lint lint-api generate \
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

# Integration tests run against a separate database so they never touch dev data.
TEST_DATABASE_URL ?= postgres://sprout:sprout@localhost:5442/sprout_test?sslmode=disable
TEST_REDIS_ADDR   ?= localhost:6389

test: test-db
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" TEST_REDIS_ADDR="$(TEST_REDIS_ADDR)" go test ./... -race -count=1

test-db:
	@docker compose -f docker-compose.dev.yml exec -T postgres \
		psql -U sprout -d sprout -tAc "SELECT 1 FROM pg_database WHERE datname='sprout_test'" | grep -q 1 || \
		docker compose -f docker-compose.dev.yml exec -T postgres createdb -U sprout sprout_test
	goose -dir $(MIGRATIONS) postgres "$(TEST_DATABASE_URL)" up >/dev/null

lint:
	go vet ./...

# The OpenAPI file is the contract the web and Android clients are generated from.
lint-api:
	npx -y @redocly/cli@1.25.11 lint api/openapi.yaml

# sqlc turns db/queries/<domain>.sql into type-safe Go in internal/<domain>/<domain>db.
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
