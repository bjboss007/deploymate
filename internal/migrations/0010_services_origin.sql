-- +goose Up
-- Manifest teardown, surface-only: a service remembers whether a human made
-- it (origin 'manual') or a deploymate.yml declaration did ('manifest'), and
-- carries an 'orphaned' flag a manifest deploy raises when it created the
-- service but the manifest no longer declares that type. Orphans are only
-- surfaced (badge + build log) — never auto-deleted; deleting infra stays a
-- human decision. Existing rows become 'manual', un-orphaned: the safe
-- direction, since manual services are never flagged.
ALTER TABLE services ADD COLUMN origin TEXT NOT NULL DEFAULT 'manual';
ALTER TABLE services ADD COLUMN orphaned INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE services DROP COLUMN origin;
ALTER TABLE services DROP COLUMN orphaned;
