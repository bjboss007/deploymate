// Package crypto provides authenticated encryption for secrets at rest.
//
// Secrets (env var values, git credentials, database passwords) are encrypted
// with XChaCha20-Poly1305 under a master key stored in a 0600 file. The
// envelope format is "v1:<base64 nonce>:<base64 ciphertext>", which leaves
// room for key rotation (a future v2 can reference a different key).
package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"

	"golang.org/x/crypto/chacha20poly1305"
)

const keySize = chacha20poly1305.KeySize

// LoadOrCreateKey reads the master key from path, creating it with 0600
// permissions if it does not exist yet.
func LoadOrCreateKey(path string) ([keySize]byte, error) {
	var key [keySize]byte
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if _, err := rand.Read(key[:]); err != nil {
			return key, fmt.Errorf("generate key: %w", err)
		}
		if err := os.WriteFile(path, key[:], 0o600); err != nil {
			return key, fmt.Errorf("write key file: %w", err)
		}
		return key, nil
	}
	if err != nil {
		return key, fmt.Errorf("read key file: %w", err)
	}
	if len(b) != keySize {
		return key, fmt.Errorf("key file %s has wrong size %d, want %d", path, len(b), keySize)
	}
	copy(key[:], b)
	return key, nil
}

// Encrypt seals plaintext with the master key. The nonce is random per call,
// so repeated encryptions of the same value never produce the same envelope.
func Encrypt(key [keySize]byte, plaintext string) (string, error) {
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return "", err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	ct := aead.Seal(nil, nonce, []byte(plaintext), nil)
	return "v1:" + base64.RawStdEncoding.EncodeToString(nonce) + ":" + base64.RawStdEncoding.EncodeToString(ct), nil
}

// Decrypt opens an envelope produced by Encrypt. It returns an error for
// tampered ciphertext, unknown versions, or malformed input.
func Decrypt(key [keySize]byte, envelope string) (string, error) {
	version, rest, ok := cut(envelope, ':')
	if !ok || version != "v1" {
		return "", fmt.Errorf("unsupported envelope version %q", version)
	}
	nonceStr, ctStr, ok := cut(rest, ':')
	if !ok {
		return "", errors.New("malformed envelope")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(nonceStr)
	if err != nil || len(nonce) != chacha20poly1305.NonceSizeX {
		return "", errors.New("malformed nonce")
	}
	ct, err := base64.RawStdEncoding.DecodeString(ctStr)
	if err != nil {
		return "", errors.New("malformed ciphertext")
	}
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return "", err
	}
	plaintext, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", errors.New("decryption failed: wrong key or tampered ciphertext")
	}
	return string(plaintext), nil
}

// EncryptBytes seals a binary payload (e.g. a database dump) with the key.
// The result is nonce‖ciphertext in raw bytes — unlike the string envelopes
// above, which base64 the same parts into a "v1:" string. Strings are for
// SQLite text columns; bytes are for file-shaped payloads where base64 would
// waste a third of every byte.
func EncryptBytes(key [keySize]byte, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	return aead.Seal(nonce, nonce, plaintext, nil), nil
}

// DecryptBytes opens a payload produced by EncryptBytes. It returns an
// error for tampered ciphertext or malformed input.
func DecryptBytes(key [keySize]byte, blob []byte) ([]byte, error) {
	if len(blob) < chacha20poly1305.NonceSizeX {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := blob[:chacha20poly1305.NonceSizeX], blob[chacha20poly1305.NonceSizeX:]
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, errors.New("decryption failed: wrong key or tampered ciphertext")
	}
	return plaintext, nil
}

func cut(s string, sep byte) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}
