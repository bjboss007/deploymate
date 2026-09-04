// Package dns manages per-app preview DNS records so Cloudflare's free
// plan can issue an edge certificate for each hostname (no wildcard
// certs). Auto-DNS is best-effort: failures never block app creation or
// deletion.
package dns

import "context"

// Creator ensures a per-app preview DNS record exists for host
// (e.g. "my-app.dm.getmerchanttech.com"). Implementations must be
// idempotent: creating a record that already exists is success.
type Creator interface {
	EnsurePreviewRecord(ctx context.Context, host string) error
}

// Deleter removes a per-app preview DNS record for host. Implementations
// must be idempotent: removing a record that does not exist is success.
type Deleter interface {
	RemovePreviewRecord(ctx context.Context, host string) error
}

// Manager is what the dashboard holds: create the preview CNAME when an
// app is created, remove it when the app is deleted so records don't
// linger. A nil Manager disables auto-DNS entirely.
type Manager interface {
	Creator
	Deleter
}
