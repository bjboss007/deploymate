-- +goose Up
-- GitHub's numeric repository id for sources created through Connect GitHub. Unlike the
-- name, it survives a rename or a transfer, so pushes keep finding the right apps and
-- tokens can be limited to the one repository. 0 = not known yet (sources made before
-- this column; filled in the first time GitHub tells us about the repository).
ALTER TABLE git_sources ADD COLUMN repo_id INTEGER NOT NULL DEFAULT 0;
CREATE INDEX git_sources_repo_id ON git_sources(repo_id);

-- +goose Down
DROP INDEX git_sources_repo_id;
ALTER TABLE git_sources DROP COLUMN repo_id;
