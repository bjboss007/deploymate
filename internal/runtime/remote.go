package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
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
	if m := sshEndpointRe.FindStringSubmatch(endpoint); m != nil {
		opts = append(opts, client.WithHost("http://docker.example.com"), client.WithDialContext(sshDialer(m[1], m[2], m[3], keyFile)))
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

func sshDialer(user, host, port, keyFile string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
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
		return &stdioConn{cmd: cmd, in: in, out: out, stderr: &stderr}, nil
	}
}

// stdioConn presents a command's stdin/stdout as a net.Conn.
type stdioConn struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    io.ReadCloser
	stderr *limitedBuffer
}

func (c *stdioConn) Read(p []byte) (int, error) {
	n, err := c.out.Read(p)
	if err == io.EOF && n == 0 && c.stderr.Len() > 0 {
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
