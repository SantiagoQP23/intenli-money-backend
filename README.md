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
| `JWT_SECRET` | dev-only value in development; **required** in production | 32+ characters; signs access tokens |
| `ACCESS_TOKEN_TTL` / `REFRESH_TOKEN_TTL` | `15m` / `720h` | |
| `INVITATION_TTL` | `168h` | How long a workspace invitation stays valid |
| `GOOGLE_CLIENT_IDS` | (empty: Google sign-in off) | Comma-separated OAuth client IDs whose ID tokens are accepted |
| `AUTH_RATE_LIMIT` | `20` | Requests per minute per IP on `/v1/auth` |
| `CORS_ALLOWED_ORIGINS` | `http://localhost:8081` in development, none in production | Comma-separated browser origins (Expo web); native apps don't need CORS |

## API

All `/v1` routes except `/v1/auth/*` need `Authorization: Bearer <access_token>`.

| Route | Who | What |
| --- | --- | --- |
| `POST /v1/auth/register` | anyone | `{email, password, name?, base_currency?}`: creates the user and their personal workspace, returns a session |
| `POST /v1/auth/login` | anyone | `{email, password}` |
| `POST /v1/auth/google` | anyone | `{id_token}` from Google Sign-In on the app |
| `POST /v1/auth/refresh` | anyone | `{refresh_token}`: rotates it and returns a new session |
| `POST /v1/auth/logout` | anyone | `{refresh_token}`: ends that session |
| `GET`, `PATCH /v1/users/me` | signed in | Profile (`{name}`) |
| `GET`, `POST /v1/workspaces` | signed in | List yours (personal first) / create a `BUSINESS` or `SHARED` one |
| `GET /v1/workspaces/{id}` | member | Includes your `role` |
| `PATCH`, `DELETE /v1/workspaces/{id}` | owner | Rename / archive (personal can't be archived) |
| `GET /v1/workspaces/{id}/members` | member | |
| `PATCH /v1/workspaces/{id}/members/{userID}` | owner | `{role}` |
| `DELETE /v1/workspaces/{id}/members/{userID}` | owner, or yourself to leave | A workspace always keeps one owner |
| `GET`, `POST /v1/workspaces/{id}/invitations` | owner | `{email, role}`: returns a one-time `token` |
| `DELETE /v1/workspaces/{id}/invitations/{invitationID}` | owner | Revoke |
| `POST /v1/invitations/accept` | signed in | `{token}`: only works for the invited email |

Financial data (every member reads; editors and owners write):

| Route | What |
| --- | --- |
| `GET /v1/currencies` | Supported currencies with their decimal places |
| `GET`, `POST /v1/workspaces/{id}/accounts` | `{name, type: CASH\|BANK\|SAVINGS\|WALLET\|CREDIT_CARD, currency?, opening_balance?}`. Each account includes its `balance` |
| `GET`, `PATCH /v1/workspaces/{id}/accounts/{accountID}` | `{name?, type?, opening_balance?, archived?}`. Currency is fixed |
| `GET`, `POST /v1/workspaces/{id}/categories` | `{kind: INCOME\|EXPENSE, name}`. New workspaces start with defaults |
| `PATCH`, `DELETE /v1/workspaces/{id}/categories/{categoryID}` | Rename / archive |
| `GET`, `PUT /v1/workspaces/{id}/exchange-rates` | Latest rate per currency / `{currency, rate, effective_on?}` |
| `GET /v1/workspaces/{id}/transactions` | Newest first. Filters: `account_id`, `category_id`, `type`, `from`, `to`. Paging: `limit`, `cursor` (`next_cursor`) |
| `POST /v1/workspaces/{id}/transactions` | See below |
| `GET`, `PUT`, `DELETE /v1/workspaces/{id}/transactions/{transactionID}` | PUT replaces it (same body as POST). DELETE is a soft delete |

Transaction bodies:

```jsonc
// INCOME or EXPENSE
{"type": "EXPENSE", "occurred_on": "2026-11-13", "account_id": "…", "amount": "12.50",
 "category_id": "…", "description": "Almuerzo", "exchange_rate": "1.08"}
// TRANSFER (to_amount only when the currencies differ)
{"type": "TRANSFER", "occurred_on": "2026-11-13", "from_account_id": "…", "to_account_id": "…",
 "amount": "54.00", "to_amount": "50.00"}
```

### Money rules

- Amounts are JSON strings, stored as `NUMERIC`, and checked against the currency's decimal places (CLP and JPY have none). The code never uses floats.
- Each transaction stores its account movements (`entries`) in the account currency, the rate used, and the `base_amount` in the workspace base currency. Totals therefore never mix currencies, and old transactions keep their original rate.
- `exchange_rate` means "1 account currency = rate base currency". When it's omitted, the latest saved rate on or before the date is used, and any rate you send is saved for next time.
- A transfer gives both entries the same base value, so it never counts as income or expense. Balances are always computed and never stored.
- Archived accounts and categories keep their history but can't be used for new transactions.

A session is `{user, access_token, refresh_token, token_type, expires_in}`. Access tokens last 15 minutes. Refresh tokens rotate on every use, and reusing an old one revokes that whole session, which catches stolen tokens. Workspace routes answer 404 to non-members, so IDs can't be probed.

### Sign-in rules

- Passwords are hashed with argon2id. Login answers the same way, with the same timing, for an unknown email and a wrong password.
- Google sign-in finds the user by Google account, then by verified email (linking the two), and otherwise creates a new user. If it links to a password account whose email was never verified, the password and its sessions are removed, because the person who set it never proved they own the address.
- Not built yet: email verification and password reset. Both need an email provider.

### Google sign-in setup

1. In [Google Cloud Console](https://console.cloud.google.com/apis/credentials), create an OAuth consent screen, then OAuth client IDs of type **iOS** (bundle `com.intenli.money`), **Android** (package `com.intenli.money` plus your signing SHA-1) and **Web**.
2. Set `GOOGLE_CLIENT_IDS` to all three IDs, comma-separated.
3. Give the same IDs to the app (see its README).

## Layout

```
cmd/api/            entrypoint (server and `migrate` command)
internal/config/    environment configuration
internal/auth/      passwords, access/refresh tokens, Google ID token check
internal/database/  pgx connection pool
internal/store/     SQL per table (works inside or outside a transaction)
internal/httpapi/   Chi router, auth + workspace middleware, handlers
migrations/         SQL migrations (goose), embedded in the binary
```

API errors always use `{"error": {"code": "...", "message": "..."}}`. Feature routes are mounted under `/v1`.

## Money rules

Never use `float64` for money. Amounts are stored as PostgreSQL `NUMERIC` with an explicit currency, and transfers are never counted as income or expenses.
