// Package logos holds the technology marks shown on app and service cards
// (React, Java, PostgreSQL, Redis, …): inline SVG paths on a 24x24 grid, so
// the dashboard makes no external requests. The data is generated from the
// Simple Icons set (CC0); see data.go.
package logos

import "sort"

// Logo is one mark.
type Logo struct {
	Key   string // stable id stored on apps ("react", "spring", "postgres")
	Name  string // display name
	Color string // brand colour as #RRGGBB; "" = draw in the text colour (marks that are black or white by brand)
	Path  string // SVG path data, viewBox 0 0 24 24
}

// Get returns the logo for key, or the generic container mark for unknown keys.
func Get(key string) Logo {
	if l, ok := all[key]; ok {
		return l
	}
	return all["docker"]
}

// Has reports whether key names a known logo.
func Has(key string) bool { _, ok := all[key]; return ok }

// Choices lists the logos an owner may pick for an app, by display name.
func Choices() []Logo {
	out := make([]Logo, 0, len(all))
	for _, l := range all {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
