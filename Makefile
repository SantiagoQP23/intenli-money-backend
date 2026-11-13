-include .env
export

TEST_DATABASE_URL ?= postgres://intenli:intenli@localhost:5432/intenli_test?sslmode=disable

.PHONY: db-up db-down run migrate test test-db lint build

db-up: ## Start Postgres in Docker
	docker compose up -d --wait postgres

db-down: ## Stop Postgres (keeps data)
	docker compose down

run: ## Run the API (applies migrations in development)
	go run ./cmd/api

migrate: ## Apply pending migrations
	go run ./cmd/api migrate

test: ## Unit tests
	go test ./...

test-db: ## Unit + database tests (needs `make db-up`)
	TEST_DATABASE_URL=$(TEST_DATABASE_URL) go test ./...

lint:
	go vet ./...
	test -z "$$(gofmt -l .)"

build:
	CGO_ENABLED=0 go build -o bin/api ./cmd/api
