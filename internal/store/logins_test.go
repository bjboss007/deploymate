package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSignInHistoryCountsFlagsNewAddressesAndPrunes(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rec := func(result, email, addr string) {
		if err := st.RecordAudit(AuditEntry{TokenName: "dashboard", Action: "login", Target: email, Detail: addr, Result: result}); err != nil {
			t.Fatal(err)
		}
	}
	rec("ok", "o@x.com", "10.0.0.1")
	rec("refused", "o@x.com", "203.0.113.5")
	rec("refused", "o@x.com", "203.0.113.5")
	rec("blocked", "o@x.com", "203.0.113.5")
	rec("ok", "o@x.com", "10.0.0.1")     // same address again: not new
	rec("ok", "o@x.com", "198.51.100.7") // first time from here: new
	if err := st.RecordAudit(AuditEntry{TokenName: "claude", Action: "deploy", Target: "api", Result: "ok"}); err != nil {
		t.Fatal(err)
	}

	c, err := st.LoginCounts(time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano))
	if err != nil || c["ok"] != 3 || c["refused"] != 2 || c["blocked"] != 1 {
		t.Fatalf("%v %v", c, err)
	}
	rows, err := st.ListLogins(10)
	if err != nil || len(rows) != 6 {
		t.Fatalf("%d rows, %v", len(rows), err)
	}
	if rows[0].Address != "198.51.100.7" || !rows[0].NewAddress {
		t.Errorf("newest row should be a new address: %+v", rows[0])
	}
	if rows[1].NewAddress {
		t.Errorf("10.0.0.1 signed in before: %+v", rows[1])
	}
	if !rows[5].NewAddress {
		t.Errorf("the very first sign-in is from a new address: %+v", rows[5])
	}
	if audit, _ := st.ListAudit(10); len(audit) != 1 || audit[0].TokenName != "claude" {
		t.Errorf("token activity must not list dashboard sign-ins: %+v", audit)
	}
	if n, err := st.PruneLogins(time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)); err != nil || n != 6 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if audit, _ := st.ListAudit(10); len(audit) != 1 {
		t.Error("pruning sign-ins must leave token activity alone")
	}
}
