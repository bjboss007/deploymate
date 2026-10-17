package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestOwnerFromAHashNeverNeedsTheClearPassword(t *testing.T) {
	st := testStore(t)
	hash, _ := auth.HashPassword("correct horse battery")
	if err := ensureOwner(st, "owner@customer.com", "", hash); err != nil {
		t.Fatal(err)
	}
	u, err := st.GetUserByEmail("owner@customer.com")
	if err != nil || u.Role != "owner" || u.PasswordHash != hash {
		t.Fatalf("%+v %v", u, err)
	}
	if ok, _ := auth.VerifyPassword("correct horse battery", u.PasswordHash); !ok {
		t.Fatal("the owner must be able to sign in with the original password")
	}
	// A second run changes nothing.
	other, _ := auth.HashPassword("other")
	if err := ensureOwner(st, "second@x.com", "", other); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountUsers(); n != 1 {
		t.Fatalf("%d users", n)
	}
}

func TestOwnerRejectsAHashThatIsNotArgon2(t *testing.T) {
	st := testStore(t)
	for _, bad := range []string{"plaintext", "$2a$10$abcdefghijklmnopqrstuv", "$argon2id$v=19$m=1,t=1,p=1$$"} {
		if err := ensureOwner(st, "o@x.com", "", bad); err == nil || !strings.Contains(err.Error(), "hash-password") {
			t.Errorf("%q: %v, want a pointer to `deploymate hash-password`", bad, err)
		}
	}
	if n, _ := st.CountUsers(); n != 0 {
		t.Fatal("a bad hash must not create a user nobody can sign in as")
	}
}

func TestTokenCreateWorksAndRespectsScopeExpiryAndCap(t *testing.T) {
	st := testStore(t)
	if _, err := createToken(st, "", "managed", "provision", 0); err == nil || !strings.Contains(err.Error(), "setup-admin") {
		t.Fatalf("no owner yet: %v", err)
	}
	if err := ensureOwner(st, "o@x.com", "pw-pw-pw-pw", ""); err != nil {
		t.Fatal(err)
	}
	plain, err := createToken(st, "", "managed", "provision", 30)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := st.GetAPITokenByHash(auth.HashToken(plain))
	if err != nil || tok.Scope != "provision" || tok.Name != "managed" {
		t.Fatalf("%+v %v", tok, err)
	}
	exp, err := time.Parse(time.RFC3339Nano, tok.ExpiresAt)
	if err != nil || time.Until(exp) < 29*24*time.Hour || time.Until(exp) > 31*24*time.Hour {
		t.Fatalf("expiry %q", tok.ExpiresAt)
	}
	if p2, _ := createToken(st, "", "forever", "read", 0); p2 == "" {
		t.Fatal("days 0 = never expires")
	} else if t2, _ := st.GetAPITokenByHash(auth.HashToken(p2)); t2.ExpiresAt != "" {
		t.Fatalf("expires %q", t2.ExpiresAt)
	}
	for name, args := range map[string][]any{"no name": {"", "read", 0}, "bad scope": {"x", "root", 0}, "negative": {"x", "read", -1}} {
		if _, err := createToken(st, "", args[0].(string), args[1].(string), args[2].(int)); err == nil {
			t.Errorf("%s should be refused", name)
		}
	}
	for i := 0; i < maxTokens; i++ {
		_, _ = createToken(st, "", "t", "read", 0)
	}
	if _, err := createToken(st, "", "one more", "read", 0); err == nil || !strings.Contains(err.Error(), "revoke") {
		t.Fatalf("cap: %v", err)
	}
}
