// Package dns manages per-app preview DNS records so Cloudflare's free
// plan can issue an edge certificate for each hostname (no wildcard
// certs). Auto-DNS is best-effort: failures never block app creation.
package dns

import "context"

// Creator ensures a per-app preview DNS record exists for host
// (e.g. "my-app.dm.getmerchanttech.com"). Implementations must be
// idempotent: creating a record that already exists is success.
type Creator interface {
	EnsurePreviewRecord(ctx context.Context, host string) error
}
