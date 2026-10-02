-- +goose Up
-- Prebuilt deploys (docs/specs/prebuilt-deploys.md): an app can deploy a
-- JAR that GitHub Actions built, instead of building on this server.
-- apps.deploy_mode: 'build' (default — today's behavior, untouched) or
-- 'artifact'. workflow_path/artifact_name say which workflow's runs deploy
-- this app and which uploaded artifact holds the JAR.
ALTER TABLE apps ADD COLUMN deploy_mode TEXT NOT NULL DEFAULT 'build';
ALTER TABLE apps ADD COLUMN workflow_path TEXT NOT NULL DEFAULT '.github/workflows/deploymate.yml';
ALTER TABLE apps ADD COLUMN artifact_name TEXT NOT NULL DEFAULT 'deploymate-app';

-- A fine-grained GitHub token (Actions: read) used to list runs and download
-- artifacts. Encrypted like the other credentials (ADR 0008). Separate from
-- pat_enc: that one is a (currently unused) CLONE credential; an API token
-- is a different thing and conflating them would make a future HTTPS-clone
-- feature ambiguous.
ALTER TABLE git_sources ADD COLUMN api_token_enc TEXT NOT NULL DEFAULT '';

-- CI-driven deployments remember which GitHub run produced them: ci_run is
-- the run id (idempotency: one deployment per run), ci_run_number is the
-- workflow's monotonic counter (ordering: an older run finishing late must
-- not roll a newer one back). 0 = not a CI deployment.
ALTER TABLE deployments ADD COLUMN ci_run INTEGER NOT NULL DEFAULT 0;
ALTER TABLE deployments ADD COLUMN ci_run_number INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_deployments_app_ci_run ON deployments(app_id, ci_run);

-- +goose Down
DROP INDEX idx_deployments_app_ci_run;
ALTER TABLE deployments DROP COLUMN ci_run_number;
ALTER TABLE deployments DROP COLUMN ci_run;
ALTER TABLE git_sources DROP COLUMN api_token_enc;
ALTER TABLE apps DROP COLUMN artifact_name;
ALTER TABLE apps DROP COLUMN workflow_path;
ALTER TABLE apps DROP COLUMN deploy_mode;
