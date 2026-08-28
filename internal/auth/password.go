// Package auth handles passwords, browser sessions, and CSRF protection.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (OWASP-recommended tier).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// HashPassword hashes a password with argon2id and returns a PHC-format
// string so parameters can evolve without breaking stored hashes.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword checks a password against a PHC hash produced by
// HashPassword, in constant time.
func VerifyPassword(password, encoded string) (bool, error) {
	salt, params, key, err := parsePHC(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, params.t, params.m, params.p, uint32(len(key)))
	return subtle.ConstantTimeCompare(got, key) == 1, nil
}

type phcParams struct{ m, t uint32; p uint8 }

func parsePHC(encoded string) (salt []byte, params phcParams, key []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return nil, params, nil, errors.New("auth: unsupported hash format")
	}
	var version int
	if _, err = fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return nil, params, nil, errors.New("auth: bad hash version")
	}
	if _, err = fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &params.m, &params.t, &params.p); err != nil {
		return nil, params, nil, errors.New("auth: bad hash params")
	}
	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return nil, params, nil, errors.New("auth: bad salt")
	}
	if key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return nil, params, nil, errors.New("auth: bad key")
	}
	return salt, params, key, nil
}
