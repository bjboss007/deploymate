package httpserver

import (
	"net/http"
	"strings"

	"github.com/habibmuhammad/deploymate/internal/appspec"
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

// handleAppGPU switches GPU access for the app's containers; it applies from the next deploy.
func (s *Server) handleAppGPU(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	back := "/apps/" + app.Slug
	on := r.FormValue("gpu") == "on"
	if on {
		if why := s.gpuRefusal(app); why != "" {
			http.Redirect(w, r, back+"?flash="+flashURL(why), http.StatusSeeOther)
			return
		}
	}
	if on != app.GPU {
		if err := s.store.SetAppGPU(app.ID, on); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		msg := "GPU access off"
		if on {
			msg = "GPU access on"
		}
		_ = s.store.RecordEvent(app.ID, store.EventGPUChanged, msg)
	}
	http.Redirect(w, r, back+"?flash="+flashURL("GPU access saved. It applies from the next deploy."), http.StatusSeeOther)
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
		return n.Name + ": " + n.GPUInfo + "."
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
