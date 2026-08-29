package httpserver

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/services"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// TestAppEnvFiltersByEnvironment proves URL injection is environment-scoped:
// a production app sees only production services and a staging app only
// staging ones.
func TestAppEnvFiltersByEnvironment(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	encKey := [32]byte{7}
	s := &Server{store: st, encKey: encKey}

	owner, err := st.CreateUser(store.User{Email: "owner@test.dev", PasswordHash: "x", Role: "owner"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	proj, err := st.CreateProject(store.Project{UserID: owner.ID, Name: "Test", Slug: "test"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	prodApp, err := st.CreateApp(store.App{ProjectID: proj.ID, Name: "Web", Slug: "web", Environment: store.EnvProduction})
	if err != nil {
		t.Fatalf("create prod app: %v", err)
	}
	stagingApp, err := st.CreateApp(store.App{ProjectID: proj.ID, Name: "Web", Slug: "web-staging", Environment: store.EnvStaging})
	if err != nil {
		t.Fatalf("create staging app: %v", err)
	}

	mkService := func(slug, env string) {
		t.Helper()
		tpl, _ := services.ForType("postgres")
		svc, err := st.CreateService(store.Service{
			ProjectID: proj.ID, Type: "postgres", Name: slug, Slug: slug,
			Image: tpl.Image, Status: "running", VolumeName: services.VolumeName(slug), Port: tpl.Port,
			Environment: env,
		})
		if err != nil {
			t.Fatalf("create service %s: %v", slug, err)
		}
		enc := map[string]string{}
		for k, v := range map[string]string{"user": "dm", "password": "pw", "db": "app"} {
			enc[k], err = crypto.Encrypt(encKey, v)
			if err != nil {
				t.Fatalf("encrypt cred: %v", err)
			}
		}
		if err := st.SetServiceCredentials(svc.ID, enc); err != nil {
			t.Fatalf("set creds: %v", err)
		}
	}
	mkService("postgres", store.EnvProduction)
	mkService("staging-postgres", store.EnvStaging)

	prodEnv := strings.Join(s.AppEnv(prodApp), "\n")
	if !strings.Contains(prodEnv, "DATABASE_URL=postgres://dm:pw@dm-svc-postgres:5432/app") {
		t.Fatalf("production app env missing production URL:\n%s", prodEnv)
	}
	if strings.Contains(prodEnv, "dm-svc-staging-postgres") {
		t.Fatalf("production app env leaked a staging URL:\n%s", prodEnv)
	}

	stagingEnv := strings.Join(s.AppEnv(stagingApp), "\n")
	if !strings.Contains(stagingEnv, "DATABASE_URL=postgres://dm:pw@dm-svc-staging-postgres:5432/app") {
		t.Fatalf("staging app env missing staging URL:\n%s", stagingEnv)
	}
	if strings.Contains(stagingEnv, "dm-svc-postgres:5432") {
		t.Fatalf("staging app env leaked a production URL:\n%s", stagingEnv)
	}
}
