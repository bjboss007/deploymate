package httpserver

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/habibmuhammad/deploymate/internal/appspec"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// gpuRefusal says why GPU access cannot be switched on for an app, or "" when it can. Only a node is known to
// lack a GPU; for this server the deploy itself checks (runtime.Docker.Create) and explains.
func (s *Server) gpuRefusal(app store.App) string {
	project, err := s.store.GetProjectByID(app.ProjectID)
	if err != nil {
		return ""
	}
	id, _ := s.store.ProjectNodeID(project.ID)
	if id == "" {
		return ""
	}
	n, err := s.store.GetNode(id)
	if err != nil || n.GPURuntime {
		return ""
	}
	return n.Name + " has no NVIDIA GPU runtime. Install the NVIDIA Container Toolkit on it (docs: Worker nodes), then check the node again."
}

// autoGPUNode is the node name an agent or the picker sends to mean "any ready node with an unclaimed GPU".
const autoGPUNode = "gpu"

// nodeGPUs is how many NVIDIA cards a node has, as its last probe reported (0 = none or unknown).
func nodeGPUs(n store.Node) int { return runtime.GPUCount(n.GPUInfo) }

// applyGPU saves the app's GPU setting (count 0 = every GPU) after checking it against the node, and returns a plain
// sentence when it cannot be done. It applies from the next deploy.
func (s *Server) applyGPU(app store.App, on bool, count int) (refusal string, err error) {
	if !on {
		count = 0
	} else {
		if why := s.gpuRefusal(app); why != "" {
			return why, nil
		}
		if count < 0 {
			return "The GPU count cannot be negative. Leave it empty for every GPU.", nil
		}
		if n, ok := s.appNode(app); ok && nodeGPUs(n) > 0 && count > nodeGPUs(n) {
			return fmt.Sprintf("%s has %d GPU(s); the app asked for %d.", n.Name, nodeGPUs(n), count), nil
		}
	}
	if on == app.GPU && count == app.GPUCount {
		return "", nil
	}
	if err := s.store.SetAppGPU(app.ID, on, count); err != nil {
		return "", err
	}
	msg := "GPU access off"
	switch {
	case on && count == 0:
		msg = "GPU access on (every GPU)"
	case on:
		msg = fmt.Sprintf("GPU access on (%d)", count)
	}
	_ = s.store.RecordEvent(app.ID, store.EventGPUChanged, msg)
	return "", nil
}

// appNode is the worker node the app's project is pinned to; ok is false for this server.
func (s *Server) appNode(app store.App) (store.Node, bool) {
	id, _ := s.store.ProjectNodeID(app.ProjectID)
	if id == "" {
		return store.Node{}, false
	}
	n, err := s.store.GetNode(id)
	return n, err == nil
}

// handleAppGPU switches GPU access for the app's containers; it applies from the next deploy.
func (s *Server) handleAppGPU(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	back := "/apps/" + app.Slug
	count := 0
	if v := strings.TrimSpace(r.FormValue("gpu_count")); v != "" {
		var err error
		if count, err = strconv.Atoi(v); err != nil {
			http.Redirect(w, r, back+"?flash="+flashURL("The GPU count must be a whole number, or empty for every GPU."), http.StatusSeeOther)
			return
		}
	}
	refusal, err := s.applyGPU(app, r.FormValue("gpu") == "on", count)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if refusal != "" {
		http.Redirect(w, r, back+"?flash="+flashURL(refusal), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back+"?flash="+flashURL("GPU access saved. It applies from the next deploy."), http.StatusSeeOther)
}

// gpusFree is how many of a node's GPUs no app pinned to it has claimed; an app that asks for "every GPU" claims all.
func (s *Server) gpusFree(n store.Node) int {
	total, used := nodeGPUs(n), 0
	counts, _ := s.store.NodeGPUClaims(n.ID)
	for _, c := range counts {
		if c == 0 {
			return 0
		}
		used += c
	}
	if used > total {
		return 0
	}
	return total - used
}

// pickGPUNode chooses the ready node with the most unclaimed GPUs (ties go to the first by name); false when none has one.
func (s *Server) pickGPUNode() (store.Node, bool) {
	nodes, _ := s.store.ListNodes()
	var best store.Node
	bestFree := 0
	for _, n := range nodes {
		if n.State != store.NodeReady || !n.GPURuntime {
			continue
		}
		if f := s.gpusFree(n); f > bestFree || (f == bestFree && f > 0 && n.Name < best.Name) {
			best, bestFree = n, f
		}
	}
	return best, bestFree > 0
}

// gpuNote is the line under the GPU switch: where the GPUs are, or why there are none.
func (s *Server) gpuNote(app store.App) string {
	project, err := s.store.GetProjectByID(app.ProjectID)
	if err != nil {
		return ""
	}
	id, _ := s.store.ProjectNodeID(project.ID)
	if id == "" {
		return "Runs on this server; the deploy says so if it has no NVIDIA GPU."
	}
	n, err := s.store.GetNode(id)
	switch {
	case err != nil:
		return ""
	case !n.GPURuntime:
		return n.Name + " has no NVIDIA GPU runtime."
	case n.GPUInfo != "":
		return fmt.Sprintf("%s: %s (%d unclaimed).", n.Name, n.GPUInfo, s.gpusFree(n))
	}
	return n.Name + " has an NVIDIA runtime."
}

// applyVolumes validates and saves the app's persistent folders; refusal is a plain sentence when the text is wrong.
func (s *Server) applyVolumes(app store.App, raw string) (refusal string, err error) {
	paths, perr := appspec.ParseVolumes(raw)
	if perr != nil {
		return strings.ToUpper(perr.Error()[:1]) + perr.Error()[1:] + ".", nil
	}
	joined := strings.Join(paths, "\n")
	if joined == app.Volumes {
		return "", nil
	}
	if err := s.store.SetAppVolumes(app.ID, joined); err != nil {
		return "", err
	}
	_ = s.store.RecordEvent(app.ID, store.EventVolumesChanged, "persistent folders: "+orNone(strings.Join(paths, ", ")))
	return "", nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// handleAppVolumes saves the persistent folders; they apply from the next deploy.
func (s *Server) handleAppVolumes(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	back := "/apps/" + app.Slug
	refusal, err := s.applyVolumes(app, r.FormValue("paths"))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if refusal != "" {
		http.Redirect(w, r, back+"?flash="+flashURL(refusal), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back+"?flash="+flashURL("Persistent folders saved. They apply from the next deploy."), http.StatusSeeOther)
}
