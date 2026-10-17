package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/config"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// maxTokens mirrors the dashboard's per-user cap.
const maxTokens = 20

// tokenCmd: `deploymate token create --name N --scope read|deploy|provision [--days N] [--email E]`.
// The plaintext goes to stdout alone (scripts capture it); everything else goes to stderr.
func tokenCmd(args []string) error {
	if len(args) == 0 || args[0] != "create" {
		return errors.New("usage: deploymate token create --name NAME --scope read|deploy|provision [--days N] [--email OWNER]")
	}
	fs := flag.NewFlagSet("token create", flag.ExitOnError)
	name := fs.String("name", "", "what the token is for (shown on the Tokens page)")
	scope := fs.String("scope", "read", "read, deploy or provision")
	days := fs.Int("days", 0, "expire after this many days (0 = never)")
	email := fs.String("email", "", "which user owns it (default: the owner)")
	_ = fs.Parse(args[1:])

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// Opened as root, SQLite would create root-owned -wal/-shm files the service user cannot use.
	if fi, err := os.Stat(cfg.DBPath()); err == nil && os.Geteuid() == 0 {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != 0 {
			return fmt.Errorf("the database belongs to another user: run `sudo -u deploymate DEPLOYMATE_DATA_DIR=%s deploymate token create …`", cfg.DataDir)
		}
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()
	plain, err := createToken(st, *email, *name, *scope, *days)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Token created. It is shown once; it can be revoked on the Tokens page.")
	fmt.Println(plain)
	return nil
}

// createToken makes an API token for the owner (or the user with this email) and returns its plaintext.
func createToken(st *store.Store, email, name, scope string, days int) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return "", errors.New("--name is required (1-64 characters)")
	}
	if !store.ValidScope(scope) {
		return "", fmt.Errorf("--scope must be read, deploy or provision (got %q)", scope)
	}
	if days < 0 {
		return "", errors.New("--days cannot be negative")
	}
	if email == "" {
		email = ownerEmail(st)
	}
	user, err := st.GetUserByEmail(email)
	if err != nil {
		return "", fmt.Errorf("no user %q: create one with `deploymate setup-admin` first", email)
	}
	if n, err := st.CountAPITokens(user.ID); err == nil && n >= maxTokens {
		return "", fmt.Errorf("%s already has %d tokens: revoke one first", user.Email, maxTokens)
	}
	plain, hash, prefix, err := auth.NewAPIToken()
	if err != nil {
		return "", err
	}
	expires := ""
	if days > 0 {
		expires = time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour).Format(time.RFC3339Nano)
	}
	if _, err := st.CreateAPIToken(store.APIToken{UserID: user.ID, Name: name, TokenHash: hash, Prefix: prefix, Scope: scope, ExpiresAt: expires}); err != nil {
		return "", err
	}
	return plain, nil
}

// ownerEmail is the first owner's email; "" when there is none.
func ownerEmail(st *store.Store) string {
	e, _ := st.OwnerEmail()
	return e
}

// hashPasswordCmd prints an argon2id hash of a password read from stdin (no echo handling: pipe it in), for
// DEPLOYMATE_SETUP_PASSWORD_HASH.
func hashPasswordCmd() error {
	fmt.Fprintln(os.Stderr, "Password (one line):")
	pw, err := bufio.NewReader(os.Stdin).ReadString('\n')
	pw = strings.TrimRight(pw, "\r\n")
	if pw == "" {
		return errors.New("no password given")
	}
	if err != nil && err.Error() != "EOF" {
		return err
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	fmt.Println(h)
	return nil
}
