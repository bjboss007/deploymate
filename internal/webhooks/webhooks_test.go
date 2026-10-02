package webhooks

import (
	"testing"
	"time"
)

// TestDeliveryCachePerSource: one event delivered to several sources' hooks
// carries one GUID — each source must process it once, none twice.
func TestDeliveryCachePerSource(t *testing.T) {
	c := NewDeliveryCache()
	if c.Seen("github", "src-dev", "guid-1") {
		t.Error("first delivery to dev must be new")
	}
	if c.Seen("github", "src-stage", "guid-1") {
		t.Error("the same GUID on another source must be new (fan-out)")
	}
	if !c.Seen("github", "src-dev", "guid-1") {
		t.Error("a retry on the same source must be a duplicate")
	}
	if !c.Seen("github", "src-stage", "guid-1") {
		t.Error("a retry on stage must be a duplicate too")
	}
	if c.Seen("gitlab", "src-dev", "guid-1") {
		t.Error("providers are keyed separately")
	}
}

func TestDeliveryCacheEmptyIDNeverDeduped(t *testing.T) {
	c := NewDeliveryCache()
	if c.Seen("github", "src", "") || c.Seen("github", "src", "") {
		t.Error("deliveries without an ID cannot be deduped and must never be reported seen")
	}
}

func TestDeliveryCacheExpires(t *testing.T) {
	c := NewDeliveryCache()
	c.Seen("github", "src", "old")
	c.items["github:src:old"] = time.Now().Add(-25 * time.Hour)
	if c.Seen("github", "src", "old") {
		t.Error("entries older than 24h must be forgotten")
	}
}
