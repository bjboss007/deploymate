package crypto

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := randomKey(t)

	for _, plaintext := range []string{"", "hello", "postgres://u:p@host:5432/db", "multi\nline\tvalue ✓"} {
		env, err := Encrypt(key, plaintext)
		if err != nil {
			t.Fatalf("Encrypt(%q): %v", plaintext, err)
		}
		if !strings.HasPrefix(env, "v1:") {
			t.Fatalf("envelope missing version prefix: %q", env)
		}
		got, err := Decrypt(key, env)
		if err != nil {
			t.Fatalf("Decrypt(%q): %v", env, err)
		}
		if got != plaintext {
			t.Fatalf("round trip mismatch: got %q want %q", got, plaintext)
		}
	}
}

func TestEncryptUsesRandomNonce(t *testing.T) {
	key := randomKey(t)
	a, err := Encrypt(key, "same value")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Encrypt(key, "same value")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two encryptions of the same value produced identical envelopes")
	}
}

func TestDecryptRejectsTampering(t *testing.T) {
	key := randomKey(t)
	env, err := Encrypt(key, "secret")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(env, ":")
	ct := []byte(parts[2])
	ct[len(ct)-1] ^= 0xff
	tampered := parts[0] + ":" + parts[1] + ":" + string(ct)
	if _, err := Decrypt(key, tampered); err == nil {
		t.Fatal("tampered ciphertext decrypted without error")
	}
}

func TestDecryptRejectsWrongKey(t *testing.T) {
	env, err := Encrypt(randomKey(t), "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(randomKey(t), env); err == nil {
		t.Fatal("wrong key decrypted without error")
	}
}

func TestLoadOrCreateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "root.key")

	key, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("key file perms = %o, want 600", perm)
	}

	again, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if key != again {
		t.Fatal("reloaded key differs from created key")
	}
}

func randomKey(t *testing.T) [keySize]byte {
	t.Helper()
	var key [keySize]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	return key
}
