package services

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
)

// GenPassword returns a 24-byte random password (48 hex chars).
func GenPassword() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on supported platforms.
		slog.Error("services: crypto/rand failed", "err", err)
		return "dm-insecure-password"
	}
	return hex.EncodeToString(b[:])
}
