package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"time"

	"github.com/habibmuhammad/deploymate/internal/store"
)

// SessionTTL is how long a browser session stays valid.
const SessionTTL = 30 * 24 * time.Hour

// NewSession creates a session record for a user. The caller stores the
// returned plaintext token in the cookie; only TokenHash is persisted.
func NewSession(userID string) (sess store.Session, plaintextToken string, err error) {
	token := make([]byte, 32)
	if _, err = rand.Read(token); err != nil {
		return sess, "", err
	}
	plaintextToken = base64.RawURLEncoding.EncodeToString(token)

	csrf := make([]byte, 32)
	if _, err = rand.Read(csrf); err != nil {
		return sess, "", err
	}

	sum := sha256.Sum256([]byte(plaintextToken))
	sess = store.Session{
		UserID:    userID,
		TokenHash: base64.RawURLEncoding.EncodeToString(sum[:]),
		CSRFToken: base64.RawURLEncoding.EncodeToString(csrf),
		ExpiresAt: time.Now().UTC().Add(SessionTTL).Format(time.RFC3339Nano),
	}
	return sess, plaintextToken, nil
}

// HashToken returns the SHA-256 hash used to look sessions up.
func HashToken(plaintextToken string) string {
	sum := sha256.Sum256([]byte(plaintextToken))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
