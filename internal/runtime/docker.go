package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	imagetypes "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/go-connections/nat"
)

// Docker implements Runtime against a Docker daemon (DOCKER_HOST or the
// default local socket).
type Docker struct {
	cli *client.Client
}

// NewDocker connects to the daemon and verifies it answers. Candidate
// endpoints are tried in order: DOCKER_HOST (with TLS settings), the
// standard local socket, then Docker Desktop's macOS socket — the latter
// two because the docker CLI resolves contexts the SDK does not read.
func NewDocker() (*Docker, error) {
	optsList := [][]client.Opt{
		{client.FromEnv, client.WithAPIVersionNegotiation()},
		{client.WithHost("unix:///var/run/docker.sock"), client.WithAPIVersionNegotiation()},
	}
	if home, err := os.UserHomeDir(); err == nil {
		optsList = append(optsList, []client.Opt{
			client.WithHost("unix://" + filepath.Join(home, ".docker/run/docker.sock")),
			client.WithAPIVersionNegotiation(),
		})
	}

	var lastErr error
	for _, opts := range optsList {
		cli, err := tryClient(opts...)
		if err == nil {
			return &Docker{cli: cli}, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("docker daemon unreachable (is docker running?): %w", lastErr)
}

// tryClient builds a client with opts and pings it; on failure the client
// is closed and the error returned.
func tryClient(opts ...client.Opt) (*client.Client, error) {
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err = cli.Ping(ctx)
	cancel()
	if err != nil {
		cli.Close()
		return nil, err
	}
	return cli, nil
}

func (d *Docker) Close() error { return d.cli.Close() }

func (d *Docker) EnsureNetwork(ctx context.Context, name string) error {
	found, err := d.cli.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return fmt.Errorf("list networks: %w", err)
	}
	for _, n := range found {
		if n.Name == name {
			return nil
		}
	}
	_, err = d.cli.NetworkCreate(ctx, name, network.CreateOptions{Driver: "bridge"})
	if err != nil {
		return fmt.Errorf("create network %s: %w", name, err)
	}
	return nil
}

func (d *Docker) PullImage(ctx context.Context, image string) error {
	rd, err := d.cli.ImagePull(ctx, image, imagetypes.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	// Drain the progress stream; the pull only completes when the stream ends.
	if _, err := io.Copy(io.Discard, rd); err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	return rd.Close()
}

func (d *Docker) HasImage(ctx context.Context, image string) (bool, error) {
	_, _, err := d.cli.ImageInspectWithRaw(ctx, image)
	if errdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect image %s: %w", image, err)
	}
	return true, nil
}

func (d *Docker) RemoveImage(ctx context.Context, tag string) (bool, error) {
	removed, err := d.cli.ImageRemove(ctx, tag, image.RemoveOptions{Force: false, PruneChildren: true})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("remove image %s: %w", tag, err)
	}
	return len(removed) > 0, nil
}

func (d *Docker) Create(ctx context.Context, spec Spec) (string, error) {
	// Network via HostConfig.NetworkMode ONLY — exactly what the docker CLI
	// sends for `--network X -p ...`. Passing NetworkingConfig alongside it
	// (or instead of it) makes the daemon silently drop PortBindings.
	hostCfg := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		NetworkMode:   container.NetworkMode(spec.Network),
		Binds:         spec.Binds,
	}
	if spec.HostPort > 0 && spec.Port > 0 {
		hostCfg.PortBindings = nat.PortMap{
			nat.Port(fmt.Sprintf("%d/tcp", spec.Port)): {{HostIP: "127.0.0.1", HostPort: fmt.Sprintf("%d", spec.HostPort)}},
		}
	}
	if spec.MemLimitMB > 0 {
		hostCfg.Memory = spec.MemLimitMB << 20
		hostCfg.MemorySwap = hostCfg.Memory * 2 // swap doubles the limit; OOM kills otherwise-fast
	}
	if spec.CPULimit > 0 {
		hostCfg.NanoCPUs = int64(spec.CPULimit * 1e9)
	}
	// ExposedPorts must include the published port: Docker Desktop's image
	// store only materializes PortBindings for ports present here (the CLI
	// always adds them from -p, masking the requirement).
	exposed := nat.PortSet{}
	if spec.Port > 0 {
		exposed[nat.Port(fmt.Sprintf("%d/tcp", spec.Port))] = struct{}{}
	}
	cfg := &container.Config{
		Image:        spec.Image,
		Env:          spec.Env,
		Labels:       spec.Labels,
		ExposedPorts: exposed,
	}
	// Command overrides are optional: an unset (nil) Entrypoint/Cmd leaves
	// the image's own values intact. This len-guard is the single
	// "empty = image default" boundary for the whole chain.
	if len(spec.Entrypoint) > 0 {
		cfg.Entrypoint = spec.Entrypoint
	}
	if len(spec.Cmd) > 0 {
		cfg.Cmd = spec.Cmd
	}
	resp, err := d.cli.ContainerCreate(ctx,
		cfg,
		hostCfg,
		nil, // no NetworkingConfig — it silently disables PortBindings
		nil,
		spec.Name,
	)
	if err != nil {
		return "", fmt.Errorf("create container %s: %w", spec.Name, err)
	}
	return resp.ID, nil
}

func (d *Docker) Start(ctx context.Context, name string) error {
	if err := d.cli.ContainerStart(ctx, name, container.StartOptions{}); err != nil {
		return mapNotFound(fmt.Errorf("start container %s: %w", name, err))
	}
	return nil
}

func (d *Docker) Stop(ctx context.Context, name string, timeoutSec int) error {
	timeout := timeoutSec
	err := d.cli.ContainerStop(ctx, name, container.StopOptions{Timeout: &timeout})
	if err != nil {
		return mapNotFound(fmt.Errorf("stop container %s: %w", name, err))
	}
	return nil
}

func (d *Docker) Remove(ctx context.Context, name string) error {
	err := d.cli.ContainerRemove(ctx, name, container.RemoveOptions{Force: true})
	if err != nil {
		return mapNotFound(fmt.Errorf("remove container %s: %w", name, err))
	}
	return nil
}

// Rename renames a container; labels, env, and restart policy carry over.
func (d *Docker) Rename(ctx context.Context, oldName, newName string) error {
	err := d.cli.ContainerRename(ctx, oldName, newName)
	if err != nil {
		return mapNotFound(fmt.Errorf("rename container %s: %w", oldName, err))
	}
	return nil
}

func (d *Docker) Inspect(ctx context.Context, name string) (Info, error) {
	ctr, err := d.cli.ContainerInspect(ctx, name)
	if err != nil {
		return Info{}, mapNotFound(fmt.Errorf("inspect container %s: %w", name, err))
	}
	ports := make([]string, 0, len(ctr.HostConfig.PortBindings))
	for p := range ctr.HostConfig.PortBindings {
		ports = append(ports, string(p))
	}
	sort.Strings(ports)
	return Info{
		ID:             ctr.ID,
		Name:           name,
		Image:          ctr.Image,
		Running:        ctr.State.Running,
		State:          ctr.State.Status,
		Restarts:       ctr.RestartCount,
		PublishedPorts: ports,
		MemLimitMB:     ctr.HostConfig.Memory >> 20,
		CPULimit:       float64(ctr.HostConfig.NanoCPUs) / 1e9,
	}, nil
}

func (d *Docker) Logs(ctx context.Context, name string, follow bool, tail int) (io.ReadCloser, error) {
	rd, err := d.cli.ContainerLogs(ctx, name, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       fmt.Sprintf("%d", tail),
	})
	if err != nil {
		return nil, mapNotFound(fmt.Errorf("logs container %s: %w", name, err))
	}
	return rd, nil
}

func (d *Docker) Exec(ctx context.Context, name string, cmd []string) (string, error) {
	return d.exec(ctx, name, cmd, nil)
}

// ExecEnv is Exec with environment overrides for the command.
func (d *Docker) ExecEnv(ctx context.Context, name string, cmd, env []string) (string, error) {
	return d.exec(ctx, name, cmd, env)
}

func (d *Docker) exec(ctx context.Context, name string, cmd, env []string) (string, error) {
	execResp, err := d.cli.ContainerExecCreate(ctx, name, container.ExecOptions{
		Cmd:          cmd,
		Env:          env,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return "", mapNotFound(fmt.Errorf("exec create in %s: %w", name, err))
	}
	attach, err := d.cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("exec attach in %s: %w", name, err)
	}
	out, readErr := io.ReadAll(attach.Reader)
	attach.Close()
	if readErr != nil {
		return "", fmt.Errorf("exec read in %s: %w", name, readErr)
	}
	insp, err := d.cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return "", fmt.Errorf("exec inspect in %s: %w", name, err)
	}
	if insp.ExitCode != 0 {
		return string(out), fmt.Errorf("exec in %s exited %d: %s", name, insp.ExitCode, out)
	}
	return string(out), nil
}

// maxReadFile caps a ReadFile result — dumps are single-digit-GiB at most
// and the backup path handles blobs in memory (see the backup spec's
// streaming note). Errors, not silent truncation, above the cap.
const maxReadFile = 4 << 30

// WriteFile writes content to a file inside a running container. The docker
// copy API moves tar archives, so the file is packed alone, destination
// directory and all (the daemon creates intermediate paths).
func (d *Docker) WriteFile(ctx context.Context, name, containerPath string, content []byte) error {
	dir, file := path.Split(containerPath)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: file, Mode: 0o644, Size: int64(len(content)),
		ModTime: time.Now(), Typeflag: tar.TypeReg,
	}); err != nil {
		return fmt.Errorf("write tar header: %w", err)
	}
	if _, err := tw.Write(content); err != nil {
		return fmt.Errorf("write tar body: %w", err)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("close tar: %w", err)
	}
	err := d.cli.CopyToContainer(ctx, name, dir, &buf, container.CopyToContainerOptions{})
	if err != nil {
		return mapNotFound(fmt.Errorf("copy into %s: %w", name, err))
	}
	return nil
}

// ReadFile reads a file out of a running container. The returned archive is
// unwrapped and must contain exactly one regular file.
func (d *Docker) ReadFile(ctx context.Context, name, containerPath string) ([]byte, error) {
	rd, _, err := d.cli.CopyFromContainer(ctx, name, containerPath)
	if err != nil {
		return nil, mapNotFound(fmt.Errorf("copy out of %s: %w", name, err))
	}
	defer rd.Close()
	tr := tar.NewReader(rd)
	hdr, err := tr.Next()
	if err == io.EOF {
		return nil, fmt.Errorf("read file %s from %s: empty archive", containerPath, name)
	}
	if err != nil {
		return nil, fmt.Errorf("read file %s from %s: %w", containerPath, name, err)
	}
	if hdr.Size < 0 || hdr.Size > maxReadFile {
		return nil, fmt.Errorf("read file %s from %s: size %d out of range", containerPath, name, hdr.Size)
	}
	if hdr.Size == 0 {
		return nil, fmt.Errorf("read file %s from %s: file is empty", containerPath, name)
	}
	content, err := io.ReadAll(io.LimitReader(tr, maxReadFile+1))
	if err != nil {
		return nil, fmt.Errorf("read file %s from %s: %w", containerPath, name, err)
	}
	if len(content) > maxReadFile {
		return nil, fmt.Errorf("read file %s from %s: exceeds %d bytes", containerPath, name, maxReadFile)
	}
	return content, nil
}

func (d *Docker) Stats(ctx context.Context, name string) (Stats, error) {
	resp, err := d.cli.ContainerStats(ctx, name, false)
	if err != nil {
		return Stats{}, mapNotFound(fmt.Errorf("stats %s: %w", name, err))
	}
	defer resp.Body.Close()

	var s container.StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return Stats{}, fmt.Errorf("decode stats %s: %w", name, err)
	}

	// CPU percent per the docker documentation: usage delta over system
	// delta, scaled by online CPUs. OnlineCPUs is authoritative — some
	// daemons (Docker Desktop) omit percpu_usage.
	cpuPercent := 0.0
	cpuDelta := s.CPUStats.CPUUsage.TotalUsage - s.PreCPUStats.CPUUsage.TotalUsage
	sysDelta := s.CPUStats.SystemUsage - s.PreCPUStats.SystemUsage
	online := int(s.CPUStats.OnlineCPUs)
	if online == 0 {
		online = len(s.CPUStats.CPUUsage.PercpuUsage)
	}
	if sysDelta > 0 && cpuDelta > 0 && online > 0 {
		cpuPercent = (float64(cpuDelta) / float64(sysDelta)) * float64(online) * 100
	}

	var rx, tx uint64
	for _, net := range s.Networks {
		rx += net.RxBytes
		tx += net.TxBytes
	}
	return Stats{
		CPUPercent: cpuPercent,
		MemBytes:   s.MemoryStats.Usage,
		NetRx:      rx,
		NetTx:      tx,
	}, nil
}

func (d *Docker) StorageUsed(ctx context.Context) (uint64, error) {
	du, err := d.cli.DiskUsage(ctx, types.DiskUsageOptions{})
	if err != nil {
		return 0, fmt.Errorf("disk usage: %w", err)
	}
	var used uint64
	for _, img := range du.Images {
		used += uint64(img.Size)
	}
	for _, c := range du.BuildCache {
		used += uint64(c.Size)
	}
	return used, nil
}

// DiskUsage returns a docker system df snapshot. ReclaimableBytes counts
// the shared image layers plus build cache — what `docker system df`
// reports as reclaimable without touching running containers.
func (d *Docker) DiskUsage(ctx context.Context) (DiskUsage, error) {
	du, err := d.cli.DiskUsage(ctx, types.DiskUsageOptions{})
	if err != nil {
		return DiskUsage{}, fmt.Errorf("disk usage: %w", err)
	}
	out := DiskUsage{}
	for _, img := range du.Images {
		out.ImagesBytes += uint64(img.Size)
		out.ReclaimableBytes += uint64(img.SharedSize)
		out.ImageCount++
		out.Images = append(out.Images, ImageUsage{
			Tags: img.RepoTags, Size: uint64(img.Size),
			Shared: uint64(img.SharedSize), UsedBy: int(img.Containers),
		})
	}
	for _, c := range du.Containers {
		out.ContainersBytes += uint64(c.SizeRw)
		out.ContainerCount++
	}
	for _, v := range du.Volumes {
		var sz uint64
		if v.UsageData != nil {
			sz = uint64(v.UsageData.Size)
		}
		out.VolumesBytes += sz
		out.VolumeCount++
		out.Volumes = append(out.Volumes, VolumeUsage{Name: v.Name, Size: sz})
	}
	for _, bc := range du.BuildCache {
		out.BuildCacheBytes += uint64(bc.Size)
		out.ReclaimableBytes += uint64(bc.Size)
	}
	return out, nil
}

// ImageSize inspects a local image and returns its on-disk size in bytes.
func (d *Docker) ImageSize(ctx context.Context, ref string) (uint64, error) {
	in, _, err := d.cli.ImageInspectWithRaw(ctx, ref)
	if err != nil {
		return 0, fmt.Errorf("inspect image %s: %w", ref, err)
	}
	return uint64(in.Size), nil
}

// mapNotFound converts docker's not-found errors into ErrContainerNotFound
// so handlers can branch on a single sentinel.
func mapNotFound(err error) error {
	if errdefs.IsNotFound(err) {
		return ErrContainerNotFound
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return err
}
