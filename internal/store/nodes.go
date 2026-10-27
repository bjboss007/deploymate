package store

import (
	"database/sql"
	"errors"
)

// Node states. "draining" is set by the owner: no new work is placed there.
const (
	NodeReady    = "ready"
	NodeDraining = "draining"
	NodeOffline  = "offline"
)

// Node is another machine that can run containers for this DeployMate.
type Node struct {
	ID, Name, Endpoint, Address, State string
	CPUs                               int
	MemBytes                           uint64
	OS, Arch, DockerVersion            string
	GPURuntime                         bool
	GPUInfo                            string // e.g. "NVIDIA GeForce RTX 4090 24 GB"; "" = none or unknown
	LastSeenAt, LastError, CreatedAt   string
}

const nodeColumns = `id, name, endpoint, address, state, cpus, mem_bytes, os, arch, docker_version, gpu_runtime, gpu_info, last_seen_at, last_error, created_at`

func (n *Node) scan(r interface{ Scan(...any) error }) error {
	var mem int64
	var gpu int
	if err := r.Scan(&n.ID, &n.Name, &n.Endpoint, &n.Address, &n.State, &n.CPUs, &mem, &n.OS, &n.Arch, &n.DockerVersion, &gpu, &n.GPUInfo, &n.LastSeenAt, &n.LastError, &n.CreatedAt); err != nil {
		return err
	}
	n.MemBytes, n.GPURuntime = uint64(mem), gpu != 0
	return nil
}

// CreateNode adds a node (offline until its first successful check).
func (s *Store) CreateNode(n Node) (Node, error) {
	n.ID, n.CreatedAt = NewID(), Now()
	if n.State == "" {
		n.State = NodeOffline
	}
	_, err := s.db.Exec(`INSERT INTO nodes (`+nodeColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.ID, n.Name, n.Endpoint, n.Address, n.State, n.CPUs, int64(n.MemBytes), n.OS, n.Arch, n.DockerVersion, boolInt(n.GPURuntime), n.GPUInfo, n.LastSeenAt, n.LastError, n.CreatedAt)
	return n, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ListNodes returns every node, by name.
func (s *Store) ListNodes() ([]Node, error) {
	rows, err := s.db.Query(`SELECT ` + nodeColumns + ` FROM nodes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var n Node
		if err := n.scan(rows); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// GetNode fetches a node by id.
func (s *Store) GetNode(id string) (Node, error) {
	var n Node
	err := n.scan(s.db.QueryRow(`SELECT `+nodeColumns+` FROM nodes WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return n, ErrNotFound
	}
	return n, err
}

// SetNodeProbe records a check: capacity and ready on success (when the owner has not drained it), offline
// and the reason on failure. A draining node stays draining while it answers.
func (s *Store) SetNodeProbe(id string, ok bool, n Node, errMsg string) error {
	if ok {
		_, err := s.db.Exec(`UPDATE nodes SET state = CASE WHEN state = 'draining' THEN state ELSE 'ready' END,
			cpus = ?, mem_bytes = ?, os = ?, arch = ?, docker_version = ?, gpu_runtime = ?, gpu_info = ?, last_seen_at = ?, last_error = '' WHERE id = ?`,
			n.CPUs, int64(n.MemBytes), n.OS, n.Arch, n.DockerVersion, boolInt(n.GPURuntime), n.GPUInfo, Now(), id)
		return err
	}
	_, err := s.db.Exec(`UPDATE nodes SET state = CASE WHEN state = 'draining' THEN state ELSE 'offline' END, last_error = ? WHERE id = ?`, errMsg, id)
	return err
}

// SetNodeDraining turns draining on or off.
func (s *Store) SetNodeDraining(id string, draining bool) error {
	state := NodeReady
	if draining {
		state = NodeDraining
	}
	_, err := s.db.Exec(`UPDATE nodes SET state = ? WHERE id = ?`, state, id)
	return err
}

// DeleteNode removes a node.
func (s *Store) DeleteNode(id string) error {
	_, err := s.db.Exec(`DELETE FROM nodes WHERE id = ?`, id)
	return err
}

// ProjectNodeID is the node a project is pinned to ("" = this server).
func (s *Store) ProjectNodeID(projectID string) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT node_id FROM projects WHERE id = ?`, projectID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

// SetProjectNode pins a project to a node ("" = this server).
func (s *Store) SetProjectNode(projectID, nodeID string) error {
	_, err := s.db.Exec(`UPDATE projects SET node_id = ? WHERE id = ?`, nodeID, projectID)
	return err
}

// NodeForApp is the node an app's project is pinned to ("" when none, or no such app).
func (s *Store) NodeForApp(slug string) string {
	var id string
	_ = s.db.QueryRow(`SELECT p.node_id FROM apps a JOIN projects p ON p.id = a.project_id WHERE a.slug = ?`, slug).Scan(&id)
	return id
}

// NodeForService is NodeForApp for a service.
func (s *Store) NodeForService(slug string) string {
	var id string
	_ = s.db.QueryRow(`SELECT p.node_id FROM services v JOIN projects p ON p.id = v.project_id WHERE v.slug = ?`, slug).Scan(&id)
	return id
}

// ProjectHasWorkloads reports whether a project has any service, or any app that was ever deployed: such a project's
// containers already live somewhere, so it cannot be moved to another node.
func (s *Store) ProjectHasWorkloads(projectID string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM services WHERE project_id = ?) +
		(SELECT COUNT(*) FROM deployments d JOIN apps a ON a.id = d.app_id WHERE a.project_id = ?)`, projectID, projectID).Scan(&n)
	return n > 0, err
}

// ContainerNode returns the node a container lives on ("" = this server).
func (s *Store) ContainerNode(name string) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT node_id FROM container_nodes WHERE name = ?`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// SetContainerNode records where a container lives.
func (s *Store) SetContainerNode(name, nodeID string) error {
	_, err := s.db.Exec(`INSERT INTO container_nodes (name, node_id) VALUES (?, ?) ON CONFLICT(name) DO UPDATE SET node_id = excluded.node_id`, name, nodeID)
	return err
}

// DeleteContainerNode forgets a container.
func (s *Store) DeleteContainerNode(name string) error {
	_, err := s.db.Exec(`DELETE FROM container_nodes WHERE name = ?`, name)
	return err
}

// ListNodeApps returns the apps of every project pinned to a node.
func (s *Store) ListNodeApps() ([]App, error) {
	rows, err := s.db.Query(`SELECT a.id FROM apps a JOIN projects p ON p.id = a.project_id WHERE p.node_id <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []App
	for _, id := range ids {
		a, err := s.GetAppByID(id)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// NodeInUse reports whether any project is pinned to the node or any container is recorded on it.
func (s *Store) NodeInUse(nodeID string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM projects WHERE node_id = ?) + (SELECT COUNT(*) FROM container_nodes WHERE node_id = ?)`, nodeID, nodeID).Scan(&n)
	return n > 0, err
}

// NodeGPUClaims lists the GPU counts of the GPU apps in projects pinned to the node (0 = an app that takes every GPU).
func (s *Store) NodeGPUClaims(nodeID string) ([]int, error) {
	rows, err := s.db.Query(`SELECT a.gpu_count FROM apps a JOIN projects p ON p.id = a.project_id WHERE p.node_id = ? AND a.gpu = 1`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var c int
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
