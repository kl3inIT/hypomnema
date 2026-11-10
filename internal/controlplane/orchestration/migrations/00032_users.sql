-- +goose Up
-- The web's built-in account (issue #114): a user signs in with a name and a password, and the browser holds a
-- session in a cookie. Tokens stay for programs: the CLI, the coordinator, nodes.
CREATE TABLE users (
    id            TEXT    NOT NULL PRIMARY KEY,
    name          TEXT    NOT NULL UNIQUE CHECK (length(name) > 0),
    -- bcrypt; the password itself is kept nowhere.
    password_hash TEXT    NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
) STRICT;

CREATE TABLE web_sessions (
    id          TEXT    NOT NULL PRIMARY KEY,
    user_id     TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- SHA-256 of the cookie's secret, hex, as a client token's.
    secret_hash TEXT    NOT NULL UNIQUE,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX web_sessions_user ON web_sessions (user_id);

-- +goose Down
DROP INDEX web_sessions_user;
DROP TABLE web_sessions;
DROP TABLE users;
