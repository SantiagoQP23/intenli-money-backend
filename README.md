# Intenli Money API

Go + Chi backend for Intenli Finance: one account, multiple workspaces, one financial engine.

## Requirements

- Go (version in `go.mod`)
- Docker (for local PostgreSQL)

## Run locally

```sh
cp .env.example .env
make db-up     # Postgres 16 on localhost:5432 (also creates intenli_test)
make run       # API on http://localhost:8080, applies migrations in development
```

Check it: `curl localhost:8080/healthz` (process is up) and `curl localhost:8080/readyz` (database reachable).

## Commands

| Command | What it does |
| --- | --- |
| `make run` | Start the API |
| `make migrate` | Apply pending migrations and exit (`api migrate` in production) |
| `make test` | Unit tests |
| `make test-db` | Unit tests plus tests against the local test database |
| `make lint` | `go vet` and `gofmt` check |
| `make build` | Build `bin/api` |

## Configuration

| Variable | Default | Notes |
| --- | --- | --- |
| `DATABASE_URL` | (required) | PostgreSQL connection string |
| `APP_ENV` | `development` | `development` or `production` (JSON logs) |
| `HTTP_ADDR` | `:8080` | |
| `AUTO_MIGRATE` | `true` in development, `false` in production | Apply migrations on startup |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

## Layout

```
cmd/api/            entrypoint (server and `migrate` command)
internal/config/    environment configuration
internal/database/  pgx connection pool
internal/httpapi/   Chi router, middleware, handlers, JSON/error helpers
migrations/         SQL migrations (goose), embedded in the binary
```

API errors always use `{"error": {"code": "...", "message": "..."}}`. Feature routes are mounted under `/v1`.

## Money rules

Never use `float64` for money. Amounts are stored as PostgreSQL `NUMERIC` with an explicit currency, and transfers are never counted as income or expenses.
