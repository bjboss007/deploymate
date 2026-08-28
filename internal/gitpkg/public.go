package gitpkg

import (
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// PublicKeyFromPEM derives the authorized-keys line from an OpenSSH private
// key PEM, so the UI can show the key the user must add to their forge.
func PublicKeyFromPEM(privateKeyPEM string) (string, error) {
	signer, err := ssh.ParsePrivateKey([]byte(privateKeyPEM))
	if err != nil {
		return "", fmt.Errorf("parse private key: %w", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), nil
}
