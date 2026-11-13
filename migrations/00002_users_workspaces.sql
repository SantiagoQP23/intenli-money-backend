-- +goose Up

CREATE TABLE users (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email          citext NOT NULL UNIQUE,
    email_verified boolean NOT NULL DEFAULT false,
    name           text NOT NULL DEFAULT '',
    -- NULL for users who only sign in with Google.
    password_hash  text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- External sign-in identities (Google today; Apple later).
CREATE TABLE user_identities (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    provider   text NOT NULL CHECK (provider IN ('google')),
    subject    text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, subject)
);
CREATE INDEX user_identities_user_id_idx ON user_identities (user_id);

-- Opaque refresh tokens, stored as SHA-256 hashes. Each refresh rotates the
-- token; presenting a rotated (revoked) token revokes the whole family.
CREATE TABLE refresh_tokens (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    family_id   uuid NOT NULL,
    token_hash  bytea NOT NULL UNIQUE,
    expires_at  timestamptz NOT NULL,
    revoked_at  timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_user_id_idx ON refresh_tokens (user_id);
CREATE INDEX refresh_tokens_family_id_idx ON refresh_tokens (family_id);

CREATE TABLE workspaces (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    type          text NOT NULL CHECK (type IN ('PERSONAL', 'BUSINESS', 'SHARED')),
    base_currency char(3) NOT NULL CHECK (base_currency ~ '^[A-Z]{3}$'),
    created_by    uuid NOT NULL REFERENCES users (id),
    archived_at   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
-- Exactly one personal workspace per user.
CREATE UNIQUE INDEX workspaces_one_personal_per_user
    ON workspaces (created_by) WHERE type = 'PERSONAL';

CREATE TABLE workspace_members (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role         text NOT NULL CHECK (role IN ('OWNER', 'EDITOR', 'VIEWER')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, user_id)
);
CREATE INDEX workspace_members_user_id_idx ON workspace_members (user_id);

CREATE TABLE workspace_invitations (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    email        citext NOT NULL,
    role         text NOT NULL CHECK (role IN ('EDITOR', 'VIEWER', 'OWNER')),
    token_hash   bytea NOT NULL UNIQUE,
    invited_by   uuid NOT NULL REFERENCES users (id),
    expires_at   timestamptz NOT NULL,
    accepted_at  timestamptz,
    accepted_by  uuid REFERENCES users (id),
    revoked_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);
-- At most one open invitation per email per workspace.
CREATE UNIQUE INDEX workspace_invitations_one_open
    ON workspace_invitations (workspace_id, email)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;

-- +goose Down
DROP TABLE workspace_invitations;
DROP TABLE workspace_members;
DROP TABLE workspaces;
DROP TABLE refresh_tokens;
DROP TABLE user_identities;
DROP TABLE users;
