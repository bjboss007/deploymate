package httpserver

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/store"
)

func TestAnOmittedEnvironmentFollowsTheProjectSoThingsCreatedTogetherAreWired(t *testing.T) {
	e := newNodesEnv(t)
	owner, _ := e.st.GetUserByEmail("o@test.dev")
	ctx := context.Background()
	mkProject := func(slug string) store.Project {
		p, _ := e.st.CreateProject(store.Project{UserID: owner.ID, Name: slug, Slug: slug})
		return p
	}

	// An empty project: dev, the same for an app and for a database.
	p1 := mkProject("p1")
	app, _, refusal, err := e.s.createAppCore(ctx, p1, "web", "")
	if err != nil || refusal != "" || app.Environment != store.EnvDev {
		t.Fatalf("app: %+v %q %v", app, refusal, err)
	}
	svc, refusal, err := e.s.createServiceCore(p1, "cache", "redis", "")
	if err != nil || refusal != "" || svc.Environment != store.EnvDev {
		t.Fatalf("a database created next to a dev app must be in dev, not production: %+v %q %v", svc, refusal, err)
	}

	// A project that already lives in production stays there.
	p2 := mkProject("p2")
	first, _, _, _ := e.s.createAppCore(ctx, p2, "api", store.EnvProduction)
	if first.Environment != store.EnvProduction {
		t.Fatal("explicit environments are honoured")
	}
	svc2, _, _ := e.s.createServiceCore(p2, "db", "postgres", "")
	app2, _, _, _ := e.s.createAppCore(ctx, p2, "worker", "")
	if svc2.Environment != store.EnvProduction || app2.Environment != store.EnvProduction {
		t.Fatalf("an omitted environment joins the one in use: service %s, app %s", svc2.Environment, app2.Environment)
	}

	// Mixed environments: nothing to follow, so dev.
	p3 := mkProject("p3")
	_, _, _, _ = e.s.createAppCore(ctx, p3, "a", store.EnvStaging)
	_, _, _, _ = e.s.createAppCore(ctx, p3, "b", store.EnvProduction)
	if svc3, _, _ := e.s.createServiceCore(p3, "c", "redis", ""); svc3.Environment != store.EnvDev {
		t.Fatalf("mixed: %s", svc3.Environment)
	}
	// An explicit environment always wins.
	if svc4, _, _ := e.s.createServiceCore(p1, "pg", "postgres", store.EnvStaging); svc4.Environment != store.EnvStaging {
		t.Fatalf("explicit: %s", svc4.Environment)
	}
}

func TestTheProjectPageExplainsADatabaseInAnotherEnvironment(t *testing.T) {
	e := newNodesEnv(t)
	owner, _ := e.st.GetUserByEmail("o@test.dev")
	proj, _ := e.st.CreateProject(store.Project{UserID: owner.ID, Name: "P", Slug: "p"})
	_, _ = e.st.CreateApp(store.App{ProjectID: proj.ID, Name: "web", Slug: "web", Port: 8080, Status: "running", Environment: store.EnvDev})
	_, _ = e.st.CreateService(store.Service{ProjectID: proj.ID, Type: "redis", Name: "cache", Slug: "cache", Image: "redis:7", Status: "running", Environment: store.EnvProduction, Port: 6379})

	page := e.do(http.MethodGet, "/projects/p", nil).Body.String()
	for _, want := range []string{"Not used by any app in this project", "Your apps run in dev", "cache (production) is in another one", "create the database in the app&#39;s environment"} {
		if !strings.Contains(page, want) {
			t.Errorf("page missing %q", want)
		}
	}

	// In the same environment there is nothing to explain.
	_ = e.st.UpdateAppEnvironment(appBySlug(t, e, "web").ID, store.EnvProduction)
	if page := e.do(http.MethodGet, "/projects/p", nil).Body.String(); strings.Contains(page, "is in another one") {
		t.Error("no hint when the environments match")
	}
}

func appBySlug(t *testing.T, e *nodesEnv, slug string) store.App {
	t.Helper()
	a, err := e.st.GetAppBySlug(slug)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
