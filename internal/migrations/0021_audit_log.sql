-- +goose Up
-- What API tokens did. Written for every state-changing /api/v1 call (accepted
-- or refused) so the owner can see what an agent did and when. token_name is
-- copied so the line stays readable after the token is revoked.
CREATE TABLE audit_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    ts         TEXT NOT NULL,
    token_id   TEXT NOT NULL,
    token_name TEXT NOT NULL,
    action     TEXT NOT NULL,   -- e.g. deploy, retry, create_app
    target     TEXT NOT NULL,   -- the app/service/project slug or deployment id
    detail     TEXT NOT NULL DEFAULT '',
    result     TEXT NOT NULL    -- ok | refused
);
CREATE INDEX audit_log_ts ON audit_log(ts);

-- +goose Down
DROP TABLE audit_log;
