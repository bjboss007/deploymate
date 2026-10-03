package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/store"
)

// lowMemRT is the fake runtime plus a memory report.
type lowMemRT struct {
	*fakeRT
	total uint64
}

func (l lowMemRT) TotalMemory(context.Context) (uint64, error) { return l.total, nil }

func TestMemoryPreflightAdvisesOnlyForJVMOnSmallDocker(t *testing.T) {
	w, fake, st := newTestWorker(t)
	app := seedApp(t, st)
	d, err := st.CreateDeployment(store.Deployment{AppID: app.ID, Kind: "deploy", Status: "building", Trigger: "dashboard"})
	if err != nil {
		t.Fatal(err)
	}
	jvm, plain := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(jvm, "build.gradle.kts"), []byte("plugins{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	logs := func() string {
		ls, _ := st.ListBuildLogs(d.ID, 0)
		return strings.Join(ls, "\n")
	}

	w.rt = lowMemRT{fake, 3 << 30}
	w.memoryPreflight(context.Background(), d, app, plain)
	if logs() != "" {
		t.Errorf("a non-Java project got advice: %q", logs())
	}
	w.memoryPreflight(context.Background(), d, app, jvm)
	if !strings.Contains(logs(), "Prebuilt mode") {
		t.Errorf("Java on 3 GiB should be advised, log = %q", logs())
	}

	d2, _ := st.CreateDeployment(store.Deployment{AppID: app.ID, Kind: "deploy", Status: "building", Trigger: "dashboard"})
	w.rt = lowMemRT{fake, 8 << 30}
	w.memoryPreflight(context.Background(), d2, app, jvm)
	if ls, _ := st.ListBuildLogs(d2.ID, 0); len(ls) != 0 {
		t.Errorf("8 GiB should say nothing, got %v", ls)
	}
	w.rt = fake // an engine that cannot report memory stays silent
	w.memoryPreflight(context.Background(), d2, app, jvm)
	if ls, _ := st.ListBuildLogs(d2.ID, 0); len(ls) != 0 {
		t.Errorf("unknown memory should say nothing, got %v", ls)
	}
}
