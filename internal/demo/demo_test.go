package demo

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/store"
)

func TestSeedBuildsACoherentFleet(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := Seed(st, [32]byte{1}, time.Now(), "x"); err != nil {
		t.Fatal(err)
	}
	u, err := st.GetUserByEmail("demo@deploymate.dev")
	if err != nil {
		t.Fatal(err)
	}
	ps, _ := st.ListProjects(u.ID)
	if len(ps) != 3 {
		t.Fatalf("projects = %d, want 3", len(ps))
	}
	apps := 0
	var failing, stopped, prebuilt int
	for _, p := range ps {
		as, _ := st.ListApps(p.ID)
		apps += len(as)
		for _, a := range as {
			switch {
			case a.Health == "unhealthy":
				failing++
			case a.Status == "stopped":
				stopped++
			}
			if a.DeployMode == store.DeployModeArtifact {
				prebuilt++
			}
			if a.Status == "running" && a.CurrentDeploymentID == "" {
				t.Errorf("%s is running but has no current deployment", a.Slug)
			}
		}
	}
	if apps != 8 || failing != 1 || stopped != 1 || prebuilt != 3 {
		t.Errorf("apps=%d failing=%d stopped=%d prebuilt=%d", apps, failing, stopped, prebuilt)
	}
	// The failing app's last deployment failed with an explainable error and has a log.
	inv, _ := st.GetAppBySlug("invoicer")
	ds, _ := st.ListDeployments(inv.ID, 5)
	if len(ds) == 0 || ds[0].Status != "failed" || ds[0].Error == "" {
		t.Fatalf("invoicer deployments = %+v", ds)
	}
	if lines, _ := st.ListBuildLogs(ds[0].ID, 0); len(lines) < 8 {
		t.Errorf("failure log has %d lines", len(lines))
	}
	// Nothing real: every repository is on the fictional org and every domain is .example.
	gsApp, _ := st.GetAppBySlug("web")
	gs, _ := st.GetGitSource(gsApp.GitSourceID)
	if gs.RepoURL != "https://github.com/acme-demo/storefront-web.git" {
		t.Errorf("repo = %q", gs.RepoURL)
	}
	doms, _ := st.ListDomains(gsApp.ID)
	if len(doms) != 1 || doms[0].TLSStatus != "active" {
		t.Errorf("domains = %+v", doms)
	}
}
