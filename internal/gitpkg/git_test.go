package gitpkg

import (
	"strings"
	"testing"
)

func TestDeployKeyRoundTrip(t *testing.T) {
	k, err := GenerateDeployKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	pub2, err := PublicKeyFromPEM(k.PrivateKeyPEM)
	if err != nil {
		t.Fatalf("re-derive: %v", err)
	}
	// ssh-keygen appends a comment; the key material is what must match.
	key1 := strings.Fields(k.PublicKey)[1]
	key2 := strings.Fields(pub2)[1]
	if key2 != key1 {
		t.Fatalf("re-derived key mismatch:\n got %q\nwant %q", key2, key1)
	}
}
