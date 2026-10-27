package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/store"
)

func postGPU(t *testing.T, s *Server, slug string, on bool) string {
	t.Helper()
	form := url.Values{"csrf_token": {"csrf"}}
	if on {
		form.Set("gpu", "on")
	}
	req := httptest.NewRequest(http.MethodPost, "/apps/"+slug+"/gpu", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST gpu: status %d", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	return loc.Query().Get("flash")
}

// GPU access can be switched on and off (with a history event); on a node without the NVIDIA runtime it is refused
// and nothing is saved.
func TestGPUSwitchAndNodeWithoutRuntime(t *testing.T) {
	st, app := replicaTestEnv(t, store.App{Name: "Llm", Slug: "llm", Port: 11434})
	s := &Server{store: st}

	postGPU(t, s, "llm", true)
	if a, _ := st.GetAppByID(app.ID); !a.GPU {
		t.Fatal("GPU was not switched on")
	}
	evs, _ := st.ListEvents(app.ID, 5)
	if len(evs) == 0 || evs[0].Kind != store.EventGPUChanged {
		t.Errorf("events = %+v", evs)
	}
	postGPU(t, s, "llm", false)
	if a, _ := st.GetAppByID(app.ID); a.GPU {
		t.Fatal("GPU was not switched off")
	}

	// A project pinned to a node that has no GPU runtime cannot turn it on.
	node, _ := st.CreateNode(store.Node{Name: "garage-nuc", Endpoint: "ssh://u@100.64.0.1", State: store.NodeReady})
	if err := st.SetProjectNode(app.ProjectID, node.ID); err != nil {
		t.Fatal(err)
	}
	if flash := postGPU(t, s, "llm", true); !strings.Contains(flash, "garage-nuc has no NVIDIA GPU runtime") {
		t.Errorf("flash = %q", flash)
	}
	if a, _ := st.GetAppByID(app.ID); a.GPU {
		t.Fatal("GPU was switched on for a node without a runtime")
	}

	// With the runtime it works, and the note names the card.
	st.SetNodeProbe(node.ID, true, store.Node{CPUs: 8, GPURuntime: true, GPUInfo: "NVIDIA GeForce RTX 4090 24 GB"}, "")
	postGPU(t, s, "llm", true)
	a, _ := st.GetAppByID(app.ID)
	if !a.GPU {
		t.Fatal("GPU was not switched on for a GPU node")
	}
	if note := s.gpuNote(a); !strings.Contains(note, "RTX 4090 24 GB") {
		t.Errorf("note = %q", note)
	}
}
