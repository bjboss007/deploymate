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
	ID, Name, Endpoint, State        string
	CPUs                             int
	MemBytes                         uint64
	OS, Arch, DockerVersion          string
	GPURuntime                       bool
	LastSeenAt, LastError, CreatedAt string
}

const nodeColumns = `id, name, endpoint, state, cpus, mem_bytes, os, arch, docker_version, gpu_runtime, last_seen_at, last_error, created_at`

func (n *Node) scan(r interface{ Scan(...any) error }) error {
	var mem int64
	var gpu int
	if err := r.Scan(&n.ID, &n.Name, &n.Endpoint, &n.State, &n.CPUs, &mem, &n.OS, &n.Arch, &n.DockerVersion, &gpu, &n.LastSeenAt, &n.LastError, &n.CreatedAt); err != nil {
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
	_, err := s.db.Exec(`INSERT INTO nodes (`+nodeColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.ID, n.Name, n.Endpoint, n.State, n.CPUs, int64(n.MemBytes), n.OS, n.Arch, n.DockerVersion, boolInt(n.GPURuntime), n.LastSeenAt, n.LastError, n.CreatedAt)
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
			cpus = ?, mem_bytes = ?, os = ?, arch = ?, docker_version = ?, gpu_runtime = ?, last_seen_at = ?, last_error = '' WHERE id = ?`,
			n.CPUs, int64(n.MemBytes), n.OS, n.Arch, n.DockerVersion, boolInt(n.GPURuntime), Now(), id)
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
