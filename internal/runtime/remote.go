package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/client"
)

// A remote Docker is another machine's daemon, reached either over SSH (`ssh user@host docker system dial-stdio`,
// the same transport `docker -H ssh://` uses: the node needs only sshd and Docker, and the SSH user must be allowed
// to use Docker) or over plain TCP (tests, or a daemon already protected by a private network).
var (
	sshEndpointRe = regexp.MustCompile(`^ssh://([a-z_][a-z0-9_-]*)@([A-Za-z0-9][A-Za-z0-9.-]*)(:[0-9]{1,5})?$`)
	tcpEndpointRe = regexp.MustCompile(`^tcp://[A-Za-z0-9][A-Za-z0-9.-]*:[0-9]{1,5}$`)
)

// ValidEndpoint reports whether e is an endpoint NewRemote accepts. The strict pattern also keeps a value that
// starts with "-" from ever being read by ssh as an option.
func ValidEndpoint(e string) error {
	if sshEndpointRe.MatchString(e) || tcpEndpointRe.MatchString(e) {
		return nil
	}
	return fmt.Errorf("endpoint must look like ssh://user@100.64.0.5 or tcp://100.64.0.5:2375 (got %q)", e)
}

// NewRemote connects to a remote Docker and checks it answers. keyFile (optional) is the SSH identity to use.
func NewRemote(ctx context.Context, endpoint, keyFile string) (*Docker, error) {
	if err := ValidEndpoint(endpoint); err != nil {
		return nil, err
	}
	opts := []client.Opt{client.WithAPIVersionNegotiation()}
	var why func() string // what ssh said last: the Docker client hides it behind "is the daemon running?"
	if m := sshEndpointRe.FindStringSubmatch(endpoint); m != nil {
		dial, last := sshDialer(m[1], m[2], m[3], keyFile)
		why = last
		opts = append(opts, client.WithHost("http://docker.example.com"), client.WithDialContext(dial))
	} else {
		opts = append(opts, client.WithHost(endpoint))
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := cli.Ping(pctx); err != nil {
		cli.Close()
		if why != nil {
			if msg := why(); msg != "" {
				return nil, fmt.Errorf("%s: %s%s", endpoint, msg, sshHint(msg))
			}
		}
		return nil, fmt.Errorf("%s: %w", endpoint, err)
	}
	return &Docker{cli: cli}, nil
}

// sshCommand is the ssh binary; DEPLOYMATE_SSH overrides it (tests put a stand-in there).
func sshCommand() string {
	if p := os.Getenv("DEPLOYMATE_SSH"); p != "" {
		return p
	}
	return "ssh"
}

// sshArgs builds the ssh command line. BatchMode means a missing key fails instead of asking for a password;
// accept-new trusts a host the first time it is seen (a tailnet address) and refuses it if its key ever changes.
func sshArgs(user, host, port, keyFile string) []string {
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=accept-new", "-o", "ServerAliveInterval=15"}
	if keyFile != "" {
		args = append(args, "-i", keyFile, "-o", "IdentitiesOnly=yes")
	}
	if port != "" {
		args = append(args, "-p", port[1:])
	}
	return append(args, "-l", user, "--", host, "docker", "system", "dial-stdio")
}

// sshHint says what to do about the common failures.
func sshHint(msg string) string {
	l := strings.ToLower(msg)
	switch {
	case strings.Contains(l, "permission denied (publickey"):
		return " (this server's key is not in that user's ~/.ssh/authorized_keys)"
	case strings.Contains(l, "permission denied") && strings.Contains(l, "docker"):
		return " (the SSH user must be allowed to use Docker: add it to the docker group on that machine)"
	case strings.Contains(l, "host key verification failed"):
		return " (the machine's SSH host key changed since it was first trusted)"
	case strings.Contains(l, "connection timed out"), strings.Contains(l, "no route to host"):
		return " (is Tailscale up on both machines?)"
	}
	return ""
}

// sshDialer returns the dial function and a way to read the last thing ssh complained about.
func sshDialer(user, host, port, keyFile string) (func(context.Context, string, string) (net.Conn, error), func() string) {
	var mu sync.Mutex
	var lastMsg string
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		cmd := exec.Command(sshCommand(), sshArgs(user, host, port, keyFile)...)
		in, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		var stderr limitedBuffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("start ssh: %w", err)
		}
		return &stdioConn{cmd: cmd, in: in, out: out, stderr: &stderr, report: func(m string) { mu.Lock(); lastMsg = m; mu.Unlock() }}, nil
	}
	return dial, func() string { mu.Lock(); defer mu.Unlock(); return strings.TrimSpace(lastMsg) }
}

// stdioConn presents a command's stdin/stdout as a net.Conn.
type stdioConn struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    io.ReadCloser
	stderr *limitedBuffer
	report func(string) // tells the dialer what ssh said, once the stream ends
}

func (c *stdioConn) Read(p []byte) (int, error) {
	n, err := c.out.Read(p)
	if err == io.EOF && n == 0 && c.stderr.Len() > 0 {
		if c.report != nil {
			c.report(c.stderr.String())
		}
		return 0, fmt.Errorf("%w: %s", io.EOF, c.stderr.String())
	}
	return n, err
}
func (c *stdioConn) Write(p []byte) (int, error) { return c.in.Write(p) }
func (c *stdioConn) Close() error {
	_ = c.in.Close()
	_ = c.cmd.Process.Kill()
	_, _ = c.cmd.Process.Wait()
	return nil
}
func (c *stdioConn) LocalAddr() net.Addr              { return stdioAddr{} }
func (c *stdioConn) RemoteAddr() net.Addr             { return stdioAddr{} }
func (c *stdioConn) SetDeadline(time.Time) error      { return nil }
func (c *stdioConn) SetReadDeadline(time.Time) error  { return nil }
func (c *stdioConn) SetWriteDeadline(time.Time) error { return nil }

type stdioAddr struct{}

func (stdioAddr) Network() string { return "stdio" }
func (stdioAddr) String() string  { return "stdio" }

// limitedBuffer keeps the first 2 KB of ssh's complaints (so an error can say why the login failed).
type limitedBuffer struct{ b []byte }

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := 2048 - len(l.b); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		l.b = append(l.b, p[:room]...)
	}
	return len(p), nil
}
func (l *limitedBuffer) Len() int       { return len(l.b) }
func (l *limitedBuffer) String() string { return string(l.b) }

// Capacity is what a Docker daemon reports about its machine.
type Capacity struct {
	CPUs          int
	MemBytes      uint64
	OS, Arch      string
	DockerVersion string
	GPURuntime    bool // the NVIDIA container runtime is installed (GPU containers can start)
	GPUInfo       string
}

// Capacity asks the daemon about its machine.
func (d *Docker) Capacity(ctx context.Context) (Capacity, error) {
	info, err := d.cli.Info(ctx)
	if err != nil {
		return Capacity{}, fmt.Errorf("docker info: %w", err)
	}
	if info.NCPU <= 0 {
		return Capacity{}, errors.New("docker info: no CPU count")
	}
	_, gpu := info.Runtimes["nvidia"]
	return Capacity{CPUs: info.NCPU, MemBytes: uint64(info.MemTotal), OS: info.OperatingSystem, Arch: info.Architecture, DockerVersion: info.ServerVersion, GPURuntime: gpu}, nil
}

func hostIP(spec Spec) string {
	if spec.HostIP != "" {
		return spec.HostIP
	}
	return "127.0.0.1"
}

// Locator is implemented by runtimes whose containers may live on other machines: HostFor is the address at which a
// container's published ports can be reached ("127.0.0.1" for a local one).
type Locator interface {
	HostFor(name string) string
}

// HostOf is the address to use for a container's published ports on rt.
func HostOf(rt Runtime, name string) string {
	if l, ok := rt.(Locator); ok {
		return l.HostFor(name)
	}
	return "127.0.0.1"
}

// SaveImage streams an image as a tar (docker save).
func (d *Docker) SaveImage(ctx context.Context, ref string) (io.ReadCloser, error) {
	return d.cli.ImageSave(ctx, []string{ref})
}

// LoadImage loads a tar produced by SaveImage (docker load).
func (d *Docker) LoadImage(ctx context.Context, tar io.Reader) error {
	resp, err := d.cli.ImageLoad(ctx, tar)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

// ImageArch is the CPU architecture an image was built for, as Go names it ("amd64", "arm64").
func (d *Docker) ImageArch(ctx context.Context, ref string) (string, error) {
	in, _, err := d.cli.ImageInspectWithRaw(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("inspect image %s: %w", ref, err)
	}
	return in.Architecture, nil
}

// HostArch is the daemon's CPU architecture in the same names as ImageArch.
func (d *Docker) HostArch(ctx context.Context) (string, error) {
	info, err := d.cli.Info(ctx)
	if err != nil {
		return "", fmt.Errorf("docker info: %w", err)
	}
	return NormalizeArch(info.Architecture), nil
}

// NormalizeArch maps uname-style names to Go's.
func NormalizeArch(a string) string {
	switch a {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	case "armv7l", "armv6l":
		return "arm"
	}
	return a
}

// GPUInfoOver asks an ssh:// machine what its GPUs are ("NVIDIA GeForce RTX 4090 24 GB", several joined with ", ").
// Docker's API cannot say, so this runs nvidia-smi over the same SSH login. "" when it cannot be told.
func GPUInfoOver(ctx context.Context, endpoint, keyFile string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "ssh" {
		return ""
	}
	port := ""
	if u.Port() != "" {
		port = ":" + u.Port()
	}
	args := sshArgs(u.User.Username(), u.Hostname(), port, keyFile)
	args = append(args[:len(args)-3], "nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader,nounits")
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, sshCommand(), args...).Output()
	if err != nil {
		return ""
	}
	return ParseGPUInfo(string(out))
}

// ParseGPUInfo turns nvidia-smi's "name, MiB" lines into one readable line.
func ParseGPUInfo(out string) string {
	var gpus []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name, mib, ok := strings.Cut(line, ",")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(mib))
		if err != nil || strings.TrimSpace(name) == "" {
			continue
		}
		gpus = append(gpus, fmt.Sprintf("%s %d GB", strings.TrimSpace(name), (n+512)/1024))
	}
	return strings.Join(gpus, ", ")
}
