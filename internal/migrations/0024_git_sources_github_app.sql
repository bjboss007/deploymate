-- +goose Up
-- A git source created through "Connect GitHub" clones with an installation token
-- instead of a deploy key (clone_method 'github_app'). The installation and the
-- repository's full name let later steps find the sources a GitHub event concerns.
ALTER TABLE git_sources ADD COLUMN installation_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE git_sources ADD COLUMN repo_full_name TEXT NOT NULL DEFAULT '';
CREATE INDEX git_sources_repo ON git_sources(repo_full_name);

-- +goose Down
DROP INDEX git_sources_repo;
ALTER TABLE git_sources DROP COLUMN repo_full_name;
ALTER TABLE git_sources DROP COLUMN installation_id;
