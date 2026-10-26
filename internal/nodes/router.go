package nodes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"

	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// Router is a runtime.Runtime that sends each call to the machine the container lives on. Callers keep using one
// Runtime: containers are addressed by name, and the router remembers which node each name is on (set when a container is
// created for a project pinned to a node). Anything it does not know is on this server. Image and disk calls that do not
// name a container stay local.
type Router struct {
	local runtime.Runtime
	st    *store.Store

	// dial connects to a node; tests replace it.
	dial func(ctx context.Context, n store.Node) (runtime.Runtime, error)

	mu      sync.Mutex
	remotes map[string]*remote // node id -> connection
	where   sync.Map           // container name -> node id ("" = local), a cache of container_nodes
}

type remote struct {
	rt   runtime.Runtime
	host string // address the node's published ports are reached at
	name string
}

var _ runtime.Runtime = (*Router)(nil)
var _ runtime.Locator = (*Router)(nil)

// NewRouter wraps the local runtime. dataDir holds the SSH key used for ssh:// nodes.
func NewRouter(local runtime.Runtime, st *store.Store, dataDir string) *Router {
	r := &Router{local: local, st: st, remotes: map[string]*remote{}}
	r.dial = func(ctx context.Context, n store.Node) (runtime.Runtime, error) {
		key := ""
		if strings.HasPrefix(n.Endpoint, "ssh://") {
			key = KeyFile(dataDir)
		}
		return runtime.NewRemote(ctx, n.Endpoint, key)
	}
	return r
}

// nodeAddress is the address a node's published ports are reached at: the endpoint's host, as an IP.
func nodeAddress(endpoint string) (string, error) {
	h := strings.TrimPrefix(strings.TrimPrefix(endpoint, "ssh://"), "tcp://")
	if i := strings.LastIndex(h, "@"); i >= 0 {
		h = h[i+1:]
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.String(), nil
	}
	ips, err := net.LookupIP(h)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", h, err)
	}
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			return v4.String(), nil
		}
	}
	return "", fmt.Errorf("%s has no IPv4 address", h)
}

func (r *Router) remoteFor(ctx context.Context, nodeID string) (*remote, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rem, ok := r.remotes[nodeID]; ok {
		return rem, nil
	}
	n, err := r.st.GetNode(nodeID)
	if err != nil {
		return nil, fmt.Errorf("node %s: %w", nodeID, err)
	}
	host, err := nodeAddress(n.Endpoint)
	if err != nil {
		return nil, err
	}
	rt, err := r.dial(ctx, n)
	if err != nil {
		return nil, fmt.Errorf("node %s unreachable: %w", n.Name, err)
	}
	rem := &remote{rt: rt, host: host, name: n.Name}
	r.remotes[nodeID] = rem
	return rem, nil
}

// nodeOf is the node a container lives on ("" = this server).
func (r *Router) nodeOf(name string) string {
	if v, ok := r.where.Load(name); ok {
		return v.(string)
	}
	id, _ := r.st.ContainerNode(name)
	r.where.Store(name, id)
	return id
}

func (r *Router) runtimeFor(ctx context.Context, name string) (runtime.Runtime, error) {
	id := r.nodeOf(name)
	if id == "" {
		return r.local, nil
	}
	rem, err := r.remoteFor(ctx, id)
	if err != nil {
		return nil, err
	}
	return rem.rt, nil
}

// HostFor implements runtime.Locator.
func (r *Router) HostFor(name string) string {
	if id := r.nodeOf(name); id != "" {
		if rem, err := r.remoteFor(context.Background(), id); err == nil {
			return rem.host
		}
	}
	return "127.0.0.1"
}

// nodeForLabels is the node a new container belongs on, from the app or service it is labelled with.
func (r *Router) nodeForLabels(labels map[string]string) string {
	if slug := labels["deploymate.app"]; slug != "" {
		return r.st.NodeForApp(slug)
	}
	if slug := labels["deploymate.service"]; slug != "" {
		return r.st.NodeForService(slug)
	}
	return ""
}

type imageSaver interface {
	SaveImage(ctx context.Context, ref string) (io.ReadCloser, error)
}
type imageLoader interface {
	LoadImage(ctx context.Context, tar io.Reader) error
}

// ensureImage makes sure the node has an image: it is copied from this server when this server has it (every image
// DeployMate builds or pulls lands here first), and pulled by the node otherwise.
func (r *Router) ensureImage(ctx context.Context, rem *remote, ref string) error {
	if has, _ := rem.rt.HasImage(ctx, ref); has {
		return nil
	}
	if has, _ := r.local.HasImage(ctx, ref); has {
		src, ok1 := r.local.(imageSaver)
		dst, ok2 := rem.rt.(imageLoader)
		if ok1 && ok2 {
			rc, err := src.SaveImage(ctx, ref)
			if err != nil {
				return fmt.Errorf("read image %s: %w", ref, err)
			}
			defer rc.Close()
			if err := dst.LoadImage(ctx, rc); err != nil {
				return fmt.Errorf("copy image %s to node %s: %w", ref, rem.name, err)
			}
			return nil
		}
	}
	if err := rem.rt.PullImage(ctx, ref); err != nil {
		return fmt.Errorf("node %s could not get image %s: %w", rem.name, ref, err)
	}
	return nil
}

// ErrNodeNotAvailable means a node is draining or offline, so nothing new can be placed on it.
var ErrNodeNotAvailable = errors.New("node is not available for new work")

func (r *Router) Create(ctx context.Context, spec runtime.Spec) (string, error) {
	id := r.nodeForLabels(spec.Labels)
	if id == "" {
		return r.local.Create(ctx, spec)
	}
	n, err := r.st.GetNode(id)
	if err != nil {
		return "", fmt.Errorf("the node this project is pinned to is gone: %w", err)
	}
	if n.State != store.NodeReady {
		return "", fmt.Errorf("%w: %s is %s", ErrNodeNotAvailable, n.Name, n.State)
	}
	rem, err := r.remoteFor(ctx, id)
	if err != nil {
		return "", err
	}
	if spec.Network != "" {
		if err := rem.rt.EnsureNetwork(ctx, spec.Network); err != nil {
			return "", fmt.Errorf("network on node %s: %w", n.Name, err)
		}
	}
	if err := r.ensureImage(ctx, rem, spec.Image); err != nil {
		return "", err
	}
	if spec.HostPort > 0 && spec.HostIP == "" {
		spec.HostIP = rem.host // the node's private-network address, never its public one
	}
	cid, err := rem.rt.Create(ctx, spec)
	if err != nil {
		return "", err
	}
	if err := r.st.SetContainerNode(spec.Name, id); err != nil {
		return "", err
	}
	r.where.Store(spec.Name, id)
	return cid, nil
}

func (r *Router) Start(ctx context.Context, name string) error {
	rt, err := r.runtimeFor(ctx, name)
	if err != nil {
		return err
	}
	return rt.Start(ctx, name)
}

func (r *Router) Stop(ctx context.Context, name string, timeoutSec int) error {
	rt, err := r.runtimeFor(ctx, name)
	if err != nil {
		return err
	}
	return rt.Stop(ctx, name, timeoutSec)
}

func (r *Router) Remove(ctx context.Context, name string) error {
	rt, err := r.runtimeFor(ctx, name)
	if err != nil {
		return err
	}
	if err := rt.Remove(ctx, name); err != nil {
		return err
	}
	_ = r.st.DeleteContainerNode(name)
	r.where.Store(name, "")
	return nil
}

func (r *Router) Rename(ctx context.Context, oldName, newName string) error {
	rt, err := r.runtimeFor(ctx, oldName)
	if err != nil {
		return err
	}
	if err := rt.Rename(ctx, oldName, newName); err != nil {
		return err
	}
	id := r.nodeOf(oldName)
	_ = r.st.DeleteContainerNode(oldName)
	r.where.Store(oldName, "")
	if id != "" {
		_ = r.st.SetContainerNode(newName, id)
	} else {
		_ = r.st.DeleteContainerNode(newName)
	}
	r.where.Store(newName, id)
	return nil
}

func (r *Router) Inspect(ctx context.Context, name string) (runtime.Info, error) {
	rt, err := r.runtimeFor(ctx, name)
	if err != nil {
		return runtime.Info{}, err
	}
	return rt.Inspect(ctx, name)
}

func (r *Router) Logs(ctx context.Context, name string, follow bool, tail int) (io.ReadCloser, error) {
	rt, err := r.runtimeFor(ctx, name)
	if err != nil {
		return nil, err
	}
	return rt.Logs(ctx, name, follow, tail)
}

func (r *Router) Exec(ctx context.Context, name string, cmd []string) (string, error) {
	rt, err := r.runtimeFor(ctx, name)
	if err != nil {
		return "", err
	}
	return rt.Exec(ctx, name, cmd)
}

func (r *Router) ExecEnv(ctx context.Context, name string, cmd, env []string) (string, error) {
	rt, err := r.runtimeFor(ctx, name)
	if err != nil {
		return "", err
	}
	return rt.ExecEnv(ctx, name, cmd, env)
}

func (r *Router) WriteFile(ctx context.Context, name, containerPath string, content []byte) error {
	rt, err := r.runtimeFor(ctx, name)
	if err != nil {
		return err
	}
	return rt.WriteFile(ctx, name, containerPath, content)
}

func (r *Router) ReadFile(ctx context.Context, name, containerPath string) ([]byte, error) {
	rt, err := r.runtimeFor(ctx, name)
	if err != nil {
		return nil, err
	}
	return rt.ReadFile(ctx, name, containerPath)
}

func (r *Router) Stats(ctx context.Context, name string) (runtime.Stats, error) {
	rt, err := r.runtimeFor(ctx, name)
	if err != nil {
		return runtime.Stats{}, err
	}
	return rt.Stats(ctx, name)
}

// Calls that name no container concern this server's own daemon.
func (r *Router) EnsureNetwork(ctx context.Context, name string) error {
	return r.local.EnsureNetwork(ctx, name)
}
func (r *Router) PullImage(ctx context.Context, image string) error {
	return r.local.PullImage(ctx, image)
}
func (r *Router) HasImage(ctx context.Context, image string) (bool, error) {
	return r.local.HasImage(ctx, image)
}
func (r *Router) RemoveImage(ctx context.Context, tag string) (bool, error) {
	return r.local.RemoveImage(ctx, tag)
}
func (r *Router) StorageUsed(ctx context.Context) (uint64, error) { return r.local.StorageUsed(ctx) }
func (r *Router) DiskUsage(ctx context.Context) (runtime.DiskUsage, error) {
	return r.local.DiskUsage(ctx)
}
func (r *Router) ImageSize(ctx context.Context, ref string) (uint64, error) {
	return r.local.ImageSize(ctx, ref)
}

// TotalMemory and PruneUnused are optional features of the local runtime that callers look for with a type assertion,
// so the router must offer them too.
func (r *Router) TotalMemory(ctx context.Context) (uint64, error) {
	if m, ok := r.local.(runtime.MemoryReporter); ok {
		return m.TotalMemory(ctx)
	}
	return 0, errors.New("the runtime cannot report memory")
}

func (r *Router) PruneUnused(ctx context.Context) (runtime.PruneResult, error) {
	if p, ok := r.local.(runtime.Pruner); ok {
		return p.PruneUnused(ctx)
	}
	return runtime.PruneResult{}, errors.New("the runtime cannot prune")
}

// Close closes every node connection and the local runtime.
func (r *Router) Close() error {
	r.mu.Lock()
	for _, rem := range r.remotes {
		_ = rem.rt.Close()
	}
	r.remotes = map[string]*remote{}
	r.mu.Unlock()
	return r.local.Close()
}
