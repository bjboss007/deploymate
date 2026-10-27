package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/nodes"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/web/templates"
)

var nodeNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)
var nodeAddrRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)

// SetNodeProbe replaces how nodes are checked (tests).
func (s *Server) SetNodeProbe(p nodes.ProbeFunc) { s.nodeProbe = p }

func (s *Server) probe() nodes.ProbeFunc {
	if s.nodeProbe != nil {
		return s.nodeProbe
	}
	return nodes.RealProbe(s.dataDir)
}

func (s *Server) handleNodesPage(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListNodes()
	if err != nil {
		slog.Error("nodes: list", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	v := templates.NodesView{}
	if raw, err := os.ReadFile(nodes.KeyFile(s.dataDir) + ".pub"); err == nil {
		v.PublicKey = strings.TrimSpace(string(raw))
	}
	for _, n := range list {
		row := templates.NodeRow{ID: n.ID, Name: n.Name, Endpoint: n.Endpoint, Address: n.Address, State: n.State, Draining: n.State == store.NodeDraining, Error: n.LastError,
			CPUs: n.CPUs, MemBytes: n.MemBytes, OS: n.OS, DockerVersion: n.DockerVersion, GPU: n.GPURuntime, GPUInfo: n.GPUInfo, LastSeenAt: n.LastSeenAt}
		v.Nodes = append(v.Nodes, row)
	}
	render(w, r, http.StatusOK, templates.NodesPage(s.viewCtx(r), v))
}

func (s *Server) nodeFlash(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/nodes?flash="+flashURL(msg), http.StatusSeeOther)
}

func (s *Server) handleNodeKey(w http.ResponseWriter, r *http.Request) {
	if _, err := nodes.EnsureKey(s.dataDir); err != nil {
		slog.Error("nodes: key", "err", err)
		s.nodeFlash(w, r, "Could not create the key: "+err.Error())
		return
	}
	s.nodeFlash(w, r, "Key created. Add it to the machine's authorized_keys.")
}

func (s *Server) handleNodeCreate(w http.ResponseWriter, r *http.Request) {
	name, endpoint := strings.TrimSpace(r.FormValue("name")), strings.TrimSpace(r.FormValue("endpoint"))
	if !nodeNameRe.MatchString(name) {
		s.nodeFlash(w, r, "Name the node with lowercase letters, digits and dashes (up to 31).")
		return
	}
	if err := runtime.ValidEndpoint(endpoint); err != nil {
		s.nodeFlash(w, r, err.Error())
		return
	}
	address := strings.TrimSpace(r.FormValue("address"))
	if address != "" && !nodeAddrRe.MatchString(address) {
		s.nodeFlash(w, r, "The address for ports must be an IP address or a host name.")
		return
	}
	n, err := s.store.CreateNode(store.Node{Name: name, Endpoint: endpoint, Address: address})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			s.nodeFlash(w, r, "A node called "+name+" already exists.")
			return
		}
		slog.Error("nodes: create", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.checkNode(w, r, n, "Added "+name)
}

// checkNode probes n and redirects with what happened.
func (s *Server) checkNode(w http.ResponseWriter, r *http.Request, n store.Node, prefix string) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := nodes.Check(ctx, s.store, n, s.probe()); err != nil {
		msg := err.Error()
		if errors.Is(err, nodes.ErrNoKey) {
			msg = nodes.ErrNoKey.Error()
		}
		s.nodeFlash(w, r, prefix+", but it is not reachable yet: "+msg)
		return
	}
	s.nodeFlash(w, r, prefix+": reachable.")
}

func (s *Server) handleNodeCheck(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.GetNode(chi.URLParam(r, "id"))
	if err != nil {
		s.nodeFlash(w, r, "That node no longer exists.")
		return
	}
	s.checkNode(w, r, n, "Checked "+n.Name)
}

func (s *Server) handleNodeDrain(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.GetNode(chi.URLParam(r, "id"))
	if err != nil {
		s.nodeFlash(w, r, "That node no longer exists.")
		return
	}
	draining := n.State != store.NodeDraining
	_ = s.store.SetNodeDraining(n.ID, draining)
	if draining {
		s.nodeFlash(w, r, n.Name+" is draining: no new work will be placed on it.")
		return
	}
	s.nodeFlash(w, r, n.Name+" resumed.")
}

func (s *Server) handleNodeDelete(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.GetNode(chi.URLParam(r, "id"))
	if err != nil {
		s.nodeFlash(w, r, "Node removed.")
		return
	}
	if used, _ := s.store.NodeInUse(n.ID); used {
		s.nodeFlash(w, r, n.Name+" still has a project pinned to it or containers on it. Delete that project first; removing the node would orphan them.")
		return
	}
	_ = s.store.DeleteNode(n.ID)
	s.nodeFlash(w, r, "Node removed.")
}

// placement builds the "where it runs" panel of a project page.
func (s *Server) placement(p store.Project) templates.Placement {
	pl := templates.Placement{}
	cur, _ := s.store.ProjectNodeID(p.ID)
	pl.Locked, _ = s.store.ProjectHasWorkloads(p.ID)
	_, pl.AnyGPU = s.pickGPUNode()
	list, _ := s.store.ListNodes()
	for _, n := range list {
		if n.ID == cur {
			pl.Current = n.Name
		}
		if n.State == store.NodeReady {
			pl.Nodes = append(pl.Nodes, templates.NodeOption{ID: n.ID, Name: n.Name, Selected: n.ID == cur})
		}
	}
	return pl
}

// handleProjectNode pins a project to a node (or back to this server). Only a project with nothing deployed can be
// pinned: its containers would otherwise end up on two machines whose networks cannot see each other.
func (s *Server) handleProjectNode(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	p, err := s.store.GetProjectBySlug(user.ID, chi.URLParam(r, "slug"))
	if err != nil {
		notFoundPage(w, r)
		return
	}
	back := func(msg string) {
		http.Redirect(w, r, "/projects/"+p.Slug+"?flash="+flashURL(msg), http.StatusSeeOther)
	}
	if locked, _ := s.store.ProjectHasWorkloads(p.ID); locked {
		back("This project already has services or deploys, so it cannot move. Create a new project on the node instead.")
		return
	}
	id := r.FormValue("node")
	if id == autoGPUNode {
		n, ok := s.pickGPUNode()
		if !ok {
			back("No ready node has an unclaimed GPU.")
			return
		}
		id = n.ID
	}
	if id != "" {
		n, err := s.store.GetNode(id)
		if err != nil {
			back("That node no longer exists.")
			return
		}
		if n.State != store.NodeReady {
			back(n.Name + " is " + n.State + ", so nothing can be placed on it now.")
			return
		}
	}
	if err := s.store.SetProjectNode(p.ID, id); err != nil {
		slog.Error("projects: set node", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if id == "" {
		back("This project runs on this server.")
		return
	}
	back("This project will run on the node. Add a database or deploy an app to start there.")
}
