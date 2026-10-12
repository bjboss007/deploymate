// Package security audits how exposed this server is. Every check is read-only and
// runs as the unprivileged service user: it reads world-readable files (sshd config,
// /proc/net/tcp, ufw's settings, apt's status) and asks the caller for what only the
// database or Docker knows. A check says what it found, why it matters, and the exact
// command to fix it; nothing here changes the machine.
package security

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Level is how worried a check should make you.
type Level int

const (
	OK   Level = iota // nothing to do
	Info              // worth knowing, not a problem
	Warn              // should be fixed
	Crit              // fix now
)

func (l Level) String() string {
	return [...]string{"ok", "info", "warning", "critical"}[l]
}

// Check is one finding.
type Check struct {
	ID     string
	Title  string
	Level  Level
	Detail string // what was found, in plain words
	Fix    string // the command or step that fixes it ("" when there is nothing to do)
}

// ContainerInfo is what the audit needs to know about one DeployMate container.
type ContainerInfo struct {
	Name            string
	PublicBindings  []string
	Privileged      bool
	NoNewPrivileges bool
	DockerSocket    bool
}

// ServiceInfo is one database or cache service.
type ServiceInfo struct {
	Slug        string
	Type        string
	HasPassword bool
}

// Env is everything the audit reads. Root lets tests point at a fixture tree.
type Env struct {
	Root          string
	DashboardAddr string // DEPLOYMATE_ADDR
	DataDir       string
	TunnelMode    bool // DEPLOYMATE_LE_MODE=off: the server is reached through a tunnel
	RebootNeeded  bool
	Containers    []ContainerInfo
	ContainersErr string // "" or why the containers could not be inspected
	Services      []ServiceInfo
	// Run runs a command and returns its output (systemctl only). nil = exec.
	Run func(ctx context.Context, name string, args ...string) (string, error)
}

func (e Env) path(rel string) string {
	root := e.Root
	if root == "" {
		root = "/"
	}
	return filepath.Join(root, rel)
}

func (e Env) read(rel string) (string, bool) {
	b, err := os.ReadFile(e.path(rel))
	return string(b), err == nil
}

func (e Env) run(ctx context.Context, name string, args ...string) (string, error) {
	if e.Run != nil {
		return e.Run(ctx, name, args...)
	}
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Report is the result of an audit.
type Report struct {
	Level    Level
	Headline string
	Checks   []Check
}

// Audit runs every check.
func Audit(ctx context.Context, e Env) Report {
	var cs []Check
	cs = append(cs, firewall(e), listeners(e))
	cs = append(cs, tunnelProfile(e)...)
	cs = append(cs, sshChecks(e)...)
	cs = append(cs, fail2ban(ctx, e), updates(e), dockerGroup(e), dashboard(e), dataDir(e))
	cs = append(cs, redis(e))
	cs = append(cs, containers(e)...)
	var kept []Check
	for _, c := range cs {
		if c.ID != "" { // a check that has nothing to say returns the zero value
			kept = append(kept, c)
		}
	}
	cs = kept

	r := Report{Checks: cs}
	var worst *Check
	for i := range cs {
		if cs[i].Level > r.Level {
			r.Level = cs[i].Level
		}
		if cs[i].Level >= Warn && (worst == nil || cs[i].Level > worst.Level) {
			worst = &cs[i]
		}
	}
	switch {
	case worst != nil:
		r.Headline = worst.Title
	default:
		r.Headline = "No problems found"
	}
	sort.SliceStable(r.Checks, func(i, j int) bool { return r.Checks[i].Level > r.Checks[j].Level })
	return r
}

// ---- firewall and listening ports ---------------------------------------------

func firewall(e Env) Check {
	c := Check{ID: "firewall", Title: "Firewall"}
	conf, ok := e.read("etc/ufw/ufw.conf")
	if !ok {
		c.Level, c.Detail = Warn, "ufw is not installed here. If another firewall is in use that is fine; otherwise nothing filters incoming connections."
		c.Fix = "sudo apt install ufw && sudo ufw allow OpenSSH && sudo ufw allow 80/tcp && sudo ufw allow 443/tcp && sudo ufw enable"
		return c
	}
	if !strings.Contains(conf, "ENABLED=yes") {
		c.Level, c.Detail, c.Fix = Crit, "ufw is installed but switched off.", "sudo ufw enable"
		return c
	}
	if def, ok := e.read("etc/default/ufw"); ok && !strings.Contains(def, `DEFAULT_INPUT_POLICY="DROP"`) {
		c.Level, c.Detail, c.Fix = Warn, "ufw is on but does not drop incoming connections by default.", "sudo ufw default deny incoming"
		return c
	}
	c.Detail = "ufw is on and drops incoming connections by default. (The rules themselves can only be read as root: run `sudo ufw status verbose`.)"
	return c
}

// Listener is a TCP socket in the LISTEN state.
type Listener struct {
	Addr     string
	Port     int
	Loopback bool
}

// ParseListeners reads /proc/net/tcp or /proc/net/tcp6 content.
func ParseListeners(proc string, v6 bool) []Listener {
	var out []Listener
	sc := bufio.NewScanner(strings.NewReader(proc))
	sc.Scan() // header
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || f[3] != "0A" { // 0A = LISTEN
			continue
		}
		addr, portHex, ok := strings.Cut(f[1], ":")
		if !ok {
			continue
		}
		port, err := strconv.ParseInt(portHex, 16, 32)
		if err != nil {
			continue
		}
		out = append(out, Listener{Addr: hexAddr(addr, v6), Port: int(port), Loopback: isLoopbackHex(addr, v6)})
	}
	return out
}

func hexAddr(h string, v6 bool) string {
	b := hexBytes(h)
	if len(b) == 0 {
		return h
	}
	if !v6 && len(b) == 4 {
		return net.IPv4(b[3], b[2], b[1], b[0]).String()
	}
	// /proc prints IPv6 as four little-endian 32-bit words.
	if len(b) == 16 {
		var ip [16]byte
		for w := 0; w < 4; w++ {
			for i := 0; i < 4; i++ {
				ip[w*4+i] = b[w*4+3-i]
			}
		}
		return net.IP(ip[:]).String()
	}
	return h
}

func hexBytes(h string) []byte {
	var out []byte
	for i := 0; i+1 < len(h); i += 2 {
		v, err := strconv.ParseUint(h[i:i+2], 16, 8)
		if err != nil {
			return nil
		}
		out = append(out, byte(v))
	}
	return out
}

func isLoopbackHex(h string, v6 bool) bool {
	ip := net.ParseIP(hexAddr(h, v6))
	return ip != nil && ip.IsLoopback()
}

var portNames = map[int]string{
	22: "SSH", 80: "web (Traefik)", 443: "web (Traefik)", 631: "printing (CUPS)", 3389: "Remote Desktop",
	5900: "VNC", 445: "file sharing (Samba)", 139: "file sharing (Samba)", 111: "rpcbind", 2049: "NFS",
	3306: "MySQL", 5432: "PostgreSQL", 6379: "Redis", 27017: "MongoDB", 9200: "Elasticsearch", 11211: "memcached",
}

var databasePorts = map[int]bool{3306: true, 5432: true, 6379: true, 27017: true, 9200: true, 11211: true}

func listeners(e Env) Check {
	c := Check{ID: "listeners", Title: "Open ports"}
	var ls []Listener
	for _, f := range []struct {
		rel string
		v6  bool
	}{{"proc/net/tcp", false}, {"proc/net/tcp6", true}} {
		if body, ok := e.read(f.rel); ok {
			ls = append(ls, ParseListeners(body, f.v6)...)
		}
	}
	if len(ls) == 0 {
		c.Level, c.Detail = Info, "Could not read the list of listening ports (not Linux?)."
		return c
	}
	seen := map[int]bool{}
	var unexpected []string
	level := OK
	for _, l := range ls {
		if l.Loopback || seen[l.Port] {
			continue
		}
		seen[l.Port] = true
		if l.Port == 22 || l.Port == 80 || l.Port == 443 {
			continue
		}
		name := portNames[l.Port]
		if name == "" {
			name = "unknown service"
		}
		unexpected = append(unexpected, fmt.Sprintf("%d (%s)", l.Port, name))
		if databasePorts[l.Port] {
			level = Crit
		} else if level < Warn {
			level = Warn
		}
	}
	sort.Strings(unexpected)
	if len(unexpected) == 0 {
		c.Detail = "Only SSH, web (80, 443) and loopback-only services listen for connections."
		return c
	}
	c.Level = level
	c.Detail = "These ports listen on every network interface: " + strings.Join(unexpected, ", ") +
		". The firewall may be the only thing keeping them private, and a firewall rule or a Docker setting can change that."
	c.Fix = "Turn the service off if you don't use it (for example Settings → Sharing for Remote Desktop, or `sudo systemctl disable --now <service>`), or make it listen on 127.0.0.1 only."
	return c
}

// tunnelProfile: a server reached through a tunnel needs no inbound ports at all.
func tunnelProfile(e Env) []Check {
	if !e.TunnelMode {
		return nil
	}
	return []Check{{
		ID: "tunnel", Title: "Tunnel mode", Level: Info,
		Detail: "DeployMate is set to run behind a tunnel (DEPLOYMATE_LE_MODE=off). A tunnel connects out to its provider, so the firewall can refuse every incoming connection. fail2ban cannot see real client addresses behind a tunnel: protect SSH and the dashboard with the tunnel provider's access policy instead.",
		Fix:    "sudo ufw delete allow 80/tcp && sudo ufw delete allow 443/tcp && sudo ufw delete allow OpenSSH   # keep SSH reachable from your home network: sudo ufw allow from 192.168.0.0/16 to any port 22 proto tcp",
	}}
}

// ---- SSH ----------------------------------------------------------------------------

// SSHSettings are the effective values of the options the audit cares about.
type SSHSettings struct {
	PasswordAuthentication string
	PermitRootLogin        string
	PubkeyAuthentication   string
}

// ParseSSHD applies OpenSSH's rule (the first value of an option wins) to config
// files given in the order sshd reads them. Match blocks are ignored: they only
// apply to some connections.
func ParseSSHD(files ...string) SSHSettings {
	s := SSHSettings{PasswordAuthentication: "yes", PermitRootLogin: "prohibit-password", PubkeyAuthentication: "yes"} // OpenSSH defaults
	got := map[string]bool{}
	for _, body := range files {
		inMatch := false
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			key := strings.ToLower(f[0])
			if key == "match" {
				inMatch = true
			}
			if inMatch || got[key] {
				continue
			}
			val := strings.ToLower(f[1])
			switch key {
			case "passwordauthentication":
				s.PasswordAuthentication, got[key] = val, true
			case "permitrootlogin":
				if val == "without-password" {
					val = "prohibit-password"
				}
				s.PermitRootLogin, got[key] = val, true
			case "pubkeyauthentication":
				s.PubkeyAuthentication, got[key] = val, true
			}
		}
	}
	return s
}

func sshChecks(e Env) []Check {
	main, ok := e.read("etc/ssh/sshd_config")
	if !ok {
		return nil // no SSH server installed: nothing to audit
	}
	// Ubuntu's sshd_config starts with `Include /etc/ssh/sshd_config.d/*.conf`, so those drop-ins win.
	var files []string
	if dropins, _ := filepath.Glob(e.path("etc/ssh/sshd_config.d/*.conf")); len(dropins) > 0 {
		sort.Strings(dropins)
		for _, d := range dropins {
			if b, err := os.ReadFile(d); err == nil {
				files = append(files, string(b))
			}
		}
	}
	files = append(files, main)
	s := ParseSSHD(files...)

	pw := Check{ID: "ssh-passwords", Title: "SSH password login"}
	if s.PasswordAuthentication == "yes" {
		pw.Level = Warn
		pw.Detail = "SSH accepts passwords, so anyone who can reach the SSH port can try to guess one. Keys cannot be guessed."
		if e.TunnelMode {
			pw.Level = Info
			pw.Detail += " Behind a tunnel with an access policy in front of SSH this is a second layer, not the only one."
		}
		pw.Fix = "Install your key first (`ssh-copy-id user@server`), confirm you can log in with it, then set `PasswordAuthentication no` in /etc/ssh/sshd_config.d/10-no-passwords.conf and `sudo systemctl reload ssh`."
	} else {
		pw.Detail = "SSH only accepts keys."
	}

	root := Check{ID: "ssh-root", Title: "SSH root login"}
	switch s.PermitRootLogin {
	case "yes":
		root.Level, root.Detail = Crit, "Root can log in over SSH with a password."
		root.Fix = "Set `PermitRootLogin no` in /etc/ssh/sshd_config.d/10-no-root.conf and `sudo systemctl reload ssh`."
	case "no":
		root.Detail = "Root cannot log in over SSH."
	default:
		root.Detail = "Root can log in over SSH only with a key. Logging in as a normal user and using sudo is safer."
	}
	return []Check{pw, root}
}

func fail2ban(ctx context.Context, e Env) Check {
	c := Check{ID: "fail2ban", Title: "fail2ban"}
	out, err := e.run(ctx, "systemctl", "is-active", "fail2ban")
	if err == nil && strings.TrimSpace(out) == "active" {
		c.Detail = "fail2ban is running."
		return c
	}
	c.Level = Warn
	c.Detail = "fail2ban is not running. It blocks addresses that keep failing to log in over SSH."
	c.Fix = "sudo apt install fail2ban && sudo systemctl enable --now fail2ban"
	if e.TunnelMode {
		c.Level = Info
		c.Detail = "fail2ban is not running. Behind a tunnel every SSH connection looks like it comes from this machine, so fail2ban could not tell attackers apart; the tunnel's access policy is the protection."
		c.Fix = ""
	}
	return c
}

// ---- updates ------------------------------------------------------------------------

func updates(e Env) Check {
	c := Check{ID: "updates", Title: "Security updates"}
	auto, _ := e.read("etc/apt/apt.conf.d/20auto-upgrades")
	autoOn := strings.Contains(auto, `Unattended-Upgrade "1"`)
	pending := -1
	if body, ok := e.read("var/lib/update-notifier/updates-available"); ok {
		for _, line := range strings.Split(body, "\n") {
			if i := strings.Index(line, "of these updates are standard security updates"); i > 0 {
				if n, err := strconv.Atoi(strings.TrimSpace(line[:i])); err == nil {
					pending = n
				}
			}
		}
		if pending < 0 && strings.Contains(body, "0 updates can be applied") {
			pending = 0
		}
	}
	switch {
	case pending > 0:
		c.Level = Warn
		c.Detail = fmt.Sprintf("%d security updates are waiting to be installed.", pending)
		c.Fix = "sudo apt update && sudo apt upgrade"
	case !autoOn:
		c.Level = Warn
		c.Detail = "Security updates are not installed automatically."
		c.Fix = "sudo apt install unattended-upgrades && sudo dpkg-reconfigure -plow unattended-upgrades"
	default:
		c.Detail = "Security updates install automatically and none are waiting."
	}
	if e.RebootNeeded {
		if c.Level < Warn {
			c.Level = Warn
		}
		c.Detail += " A reboot is needed to finish an update."
		c.Fix = strings.TrimSpace(c.Fix + "  # then: sudo reboot")
	}
	return c
}

func dockerGroup(e Env) Check {
	c := Check{ID: "docker-group", Title: "Who controls Docker"}
	grp, ok := e.read("etc/group")
	if !ok {
		return Check{}
	}
	var members string
	for _, line := range strings.Split(grp, "\n") {
		f := strings.Split(line, ":")
		if len(f) >= 4 && f[0] == "docker" {
			members = f[3]
		}
	}
	if members == "" {
		c.Detail = "No user is in the docker group."
		return c
	}
	c.Level = Info
	c.Detail = "Members of the docker group have full control of this machine (root, in effect): " + strings.ReplaceAll(members, ",", ", ") + ". DeployMate's own service user has to be one of them; anyone else should not be."
	return c
}

// ---- DeployMate itself -------------------------------------------------------------

func dashboard(e Env) Check {
	c := Check{ID: "dashboard", Title: "Dashboard address"}
	host, _, err := net.SplitHostPort(e.DashboardAddr)
	if e.DashboardAddr == "" || err != nil {
		return Check{}
	}
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) {
		c.Detail = "The dashboard listens on this machine only; Traefik or a tunnel puts it on a domain."
		return c
	}
	c.Level = Warn
	c.Detail = "The dashboard (" + e.DashboardAddr + ") listens on every network interface and is reachable without Traefik, on plain HTTP."
	c.Fix = "Set DEPLOYMATE_ADDR=127.0.0.1:8080 in the service environment and restart it."
	return c
}

func dataDir(e Env) Check {
	c := Check{ID: "data-dir", Title: "Data folder permissions"}
	if e.DataDir == "" {
		return Check{}
	}
	fi, err := os.Stat(e.DataDir)
	if err != nil {
		return Check{}
	}
	if m := fi.Mode().Perm(); m&0o077 != 0 {
		c.Level = Warn
		c.Detail = fmt.Sprintf("%s has mode %o: other users on this machine can read DeployMate's database and keys.", e.DataDir, m)
		c.Fix = "sudo chmod 700 " + e.DataDir
		return c
	}
	c.Detail = "Only DeployMate's own user can read its database and keys."
	return c
}

func redis(e Env) Check {
	c := Check{ID: "redis", Title: "Redis passwords"}
	var open []string
	for _, s := range e.Services {
		if s.Type == "redis" && !s.HasPassword {
			open = append(open, s.Slug)
		}
	}
	if len(open) == 0 {
		c.Detail = "Every Redis service requires a password (or there are none)."
		return c
	}
	c.Level = Warn
	c.Detail = "These Redis services have no password, so any container on DeployMate's Docker network can read and change their data: " + strings.Join(open, ", ") + "."
	c.Fix = "Update DeployMate: new and restarted Redis services get a password, and apps receive it in REDIS_URL."
	return c
}

func containers(e Env) []Check {
	var out []Check
	if e.ContainersErr != "" {
		return []Check{{ID: "containers", Title: "Containers", Level: Info, Detail: "Could not inspect containers: " + e.ContainersErr}}
	}
	if len(e.Containers) == 0 {
		return nil
	}
	var public, priv, sock, noNew []string
	for _, c := range e.Containers {
		if len(c.PublicBindings) > 0 {
			public = append(public, c.Name+" → "+strings.Join(c.PublicBindings, ", "))
		}
		if c.Privileged {
			priv = append(priv, c.Name)
		}
		if c.DockerSocket {
			sock = append(sock, c.Name)
		}
		if !c.NoNewPrivileges {
			noNew = append(noNew, c.Name)
		}
	}
	pub := Check{ID: "containers-public", Title: "Published container ports"}
	if len(public) > 0 {
		pub.Level = Crit
		pub.Detail = "Docker publishes these ports on every interface and bypasses the host firewall for them: " + strings.Join(public, "; ") + "."
		pub.Fix = "Redeploy the app (DeployMate publishes preview ports on 127.0.0.1 only), or remove the -p / ports setting that publishes it."
	} else {
		pub.Detail = "No container publishes a port beyond this machine."
	}
	pv := Check{ID: "containers-privileged", Title: "Privileged containers"}
	if len(priv)+len(sock) > 0 {
		pv.Level = Crit
		var parts []string
		if len(priv) > 0 {
			parts = append(parts, "run privileged: "+strings.Join(priv, ", "))
		}
		if len(sock) > 0 {
			parts = append(parts, "can use the Docker socket: "+strings.Join(sock, ", "))
		}
		pv.Detail = "These containers control the whole machine: " + strings.Join(parts, "; ") + "."
		pv.Fix = "Remove the privileged flag or the docker.sock mount unless the container is meant to manage Docker."
	} else {
		pv.Detail = "No app or service container runs privileged or holds the Docker socket."
	}
	nn := Check{ID: "containers-no-new-privileges", Title: "Containers can't gain privileges"}
	if len(noNew) > 0 {
		nn.Level = Info
		nn.Detail = fmt.Sprintf("%d of %d containers can still gain privileges inside (no-new-privileges is not set): %s.", len(noNew), len(e.Containers), strings.Join(firstN(noNew, 6), ", "))
		nn.Fix = "Redeploy after updating DeployMate, which sets it by default."
	} else {
		nn.Detail = "Every container has no-new-privileges set."
	}
	return append(out, pub, pv, nn)
}

func firstN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return append(append([]string{}, s[:n]...), fmt.Sprintf("+%d more", len(s)-n))
}
