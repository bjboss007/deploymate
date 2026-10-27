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

// Persistent folders are validated, saved, shown in the API, and settable through configure_app together with the GPU.
func TestPersistentFoldersAndAgentConfig(t *testing.T) {
	e := newPrebuiltEnv(t)
	tok := e.createToken(t, "agent", "provision", "90")
	code, _, body := e.send(t, http.MethodPost, "/api/v1/projects/test/apps", tok, map[string]string{"name": "Llm"})
	if code != http.StatusCreated {
		t.Fatalf("create app = %d %s", code, body)
	}
	for _, c := range []struct {
		in   map[string]any
		code int
		want string
	}{
		{map[string]any{"volumes": []string{"relative"}}, http.StatusConflict, "not an absolute path"},
		{map[string]any{"volumes": []string{"/proc/x"}}, http.StatusConflict, "used by the container itself"},
		{map[string]any{"volumes": []string{"/root/.ollama"}, "gpu": true}, http.StatusOK, ""},
	} {
		code, _, body := e.send(t, http.MethodPatch, "/api/v1/apps/llm/config", tok, c.in)
		if code != c.code || !strings.Contains(body, c.want) {
			t.Errorf("%v = %d %s", c.in, code, body)
		}
	}
	_, res, _ := e.send(t, http.MethodGet, "/api/v1/apps/llm", tok, nil)
	vols, _ := res["volumes"].([]any)
	if res["gpu"] != true || len(vols) != 1 || vols[0] != "/root/.ollama" {
		t.Errorf("app = gpu %v volumes %v", res["gpu"], res["volumes"])
	}
	// An empty list clears them.
	e.send(t, http.MethodPatch, "/api/v1/apps/llm/config", tok, map[string]any{"volumes": []string{}})
	if _, res, _ := e.send(t, http.MethodGet, "/api/v1/apps/llm", tok, nil); len(res["volumes"].([]any)) != 0 {
		t.Errorf("volumes not cleared: %v", res["volumes"])
	}
}

// An app can ask for some of a node's GPUs; asking for more than the node has is refused, claimed GPUs are not offered
// again, and "any node with a free GPU" picks the ready node with the most left.
func TestGPUCountAndAutomaticNodeChoice(t *testing.T) {
	st, app := replicaTestEnv(t, store.App{Name: "Llm", Slug: "llm", Port: 11434})
	s := &Server{store: st}
	two, _ := st.CreateNode(store.Node{Name: "two-cards", Endpoint: "ssh://u@100.64.0.1", State: store.NodeReady})
	st.SetNodeProbe(two.ID, true, store.Node{GPURuntime: true, GPUInfo: "NVIDIA RTX 3060 12 GB, NVIDIA RTX 3060 12 GB"}, "")
	one, _ := st.CreateNode(store.Node{Name: "one-card", Endpoint: "ssh://u@100.64.0.2", State: store.NodeReady})
	st.SetNodeProbe(one.ID, true, store.Node{GPURuntime: true, GPUInfo: "NVIDIA GTX 1060 6 GB"}, "")
	off, _ := st.CreateNode(store.Node{Name: "off-card", Endpoint: "ssh://u@100.64.0.3", State: store.NodeReady})
	st.SetNodeProbe(off.ID, false, store.Node{GPURuntime: true, GPUInfo: "NVIDIA RTX 4090 24 GB, NVIDIA RTX 4090 24 GB, NVIDIA RTX 4090 24 GB"}, "gone")

	two, _ = st.GetNode(two.ID) // the probe filled in its GPUs
	if n, ok := s.pickGPUNode(); !ok || n.Name != "two-cards" {
		t.Fatalf("pick = %q %v, want two-cards (offline nodes are never chosen)", n.Name, ok)
	}
	if err := st.SetProjectNode(app.ProjectID, two.ID); err != nil {
		t.Fatal(err)
	}
	if why, _ := s.applyGPU(app, true, 3); !strings.Contains(why, "has 2 GPU(s)") {
		t.Errorf("3 of 2: %q", why)
	}
	if why, err := s.applyGPU(app, true, 1); why != "" || err != nil {
		t.Fatalf("1 of 2: %q %v", why, err)
	}
	if got := s.gpusFree(two); got != 1 {
		t.Errorf("free after claiming 1 = %d, want 1", got)
	}
	a, _ := st.GetAppByID(app.ID)
	if !a.GPU || a.GPUCount != 1 {
		t.Fatalf("app = gpu %v count %d", a.GPU, a.GPUCount)
	}
	// Taking every GPU claims the whole node, so the next pick moves on.
	if why, _ := s.applyGPU(a, true, 0); why != "" {
		t.Fatal(why)
	}
	if got := s.gpusFree(two); got != 0 {
		t.Errorf("free after claiming all = %d, want 0", got)
	}
	if n, ok := s.pickGPUNode(); !ok || n.Name != "one-card" {
		t.Errorf("pick = %q %v, want one-card", n.Name, ok)
	}
	if why, _ := s.applyGPU(a, false, 5); why != "" {
		t.Fatal(why)
	}
	if b, _ := st.GetAppByID(app.ID); b.GPU || b.GPUCount != 0 {
		t.Errorf("after off: gpu %v count %d", b.GPU, b.GPUCount)
	}
}
