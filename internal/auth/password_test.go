package auth

import (
	"strings"
	"testing"
)

func TestHashVerifyRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("hash is not PHC argon2id format: %q", hash)
	}

	ok, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil || !ok {
		t.Fatalf("correct password rejected: ok=%v err=%v", ok, err)
	}

	ok, err = VerifyPassword("wrong password", hash)
	if err != nil || ok {
		t.Fatalf("wrong password accepted: ok=%v err=%v", ok, err)
	}
}

func TestHashIsSalted(t *testing.T) {
	a, err := HashPassword("same password")
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword("same password")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("identical hashes for the same password: salt missing")
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"", "plaintext", "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA", "not-a-hash"} {
		if _, err := VerifyPassword("whatever", bad); err == nil {
			t.Fatalf("malformed hash %q accepted", bad)
		}
	}
}
