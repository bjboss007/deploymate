package store

// User is a DeployMate account. Single-owner for now; role exists so
// multi-user stays a migration-only change.
type User struct {
	ID           string
	Email        string
	PasswordHash string
	Role         string
	CreatedAt    string
}

// Session is a logged-in browser session. TokenHash stores SHA-256 of the
// cookie value; CSRFToken is embedded in forms and checked on POST.
type Session struct {
	ID        string
	UserID    string
	TokenHash string
	CSRFToken string
	ExpiresAt string
	CreatedAt string
}

// Project groups apps and services.
type Project struct {
	ID        string
	UserID    string
	Name      string
	Slug      string
	CreatedAt string
}
