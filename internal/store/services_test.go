package store

import (
	"errors"
	"testing"
)

// A service slug names a docker container/volume, which are global, so it is
// unique across projects (the column has no UNIQUE index — older databases
// may hold duplicates — so CreateService enforces it).
func TestCreateServiceSlugUniqueAcrossProjects(t *testing.T) {
	st, app := newReplicaTestApp(t)
	owner, err := st.GetUserByEmail("o@test.dev")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := st.CreateProject(Project{UserID: owner.ID, Name: "Two", Slug: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateService(Service{ProjectID: app.ProjectID, Type: "redis", Name: "Dev Redis", Slug: "dev-redis", Image: "redis:7"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateService(Service{ProjectID: p2.ID, Type: "redis", Name: "Dev Redis", Slug: "dev-redis", Image: "redis:7"}); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("same slug in another project: err = %v, want ErrSlugTaken", err)
	}
	if _, err := st.CreateService(Service{ProjectID: p2.ID, Type: "redis", Name: "Two Redis", Slug: "two-dev-redis", Image: "redis:7"}); err != nil {
		t.Fatalf("distinct slug: %v", err)
	}
}
