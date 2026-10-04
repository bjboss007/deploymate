-- +goose Up
-- Personal access tokens for programmatic access (the MCP server, scripts).
-- Only the SHA-256 of a token is stored; the plaintext is shown once at
-- creation. scope is 'read' (GET/HEAD only) or 'write' (may act). A token acts
-- as the user that created it and dies with that user.
CREATE TABLE api_tokens (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    prefix       TEXT NOT NULL,                -- first characters, to recognise it in the list
    scope        TEXT NOT NULL DEFAULT 'read',
    created_at   TEXT NOT NULL,
    last_used_at TEXT NOT NULL DEFAULT '',
    expires_at   TEXT NOT NULL DEFAULT ''      -- '' = never
);
CREATE INDEX api_tokens_user ON api_tokens(user_id);

-- +goose Down
DROP TABLE api_tokens;
