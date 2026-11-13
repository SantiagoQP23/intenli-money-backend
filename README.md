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
