package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewID returns a random 128-bit ID as 32 lowercase hex characters.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on supported platforms; panic keeps the
		// invariant that every ID is unique.
		panic(fmt.Sprintf("store: crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b[:])
}
