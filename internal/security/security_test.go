package security

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const tcpHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

func row(n int, local string) string {
	return "   " + string(rune('0'+n)) + ": " + local + " 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0\n"
}

// The listening sockets of the owner's server (2026-10-12): SSH, Traefik, and
// loopback services, plus Remote Desktop on every interface.
func ownerProc(t *testing.T, extra4 ...string) (root string) {
	root = t.TempDir()
	v4 := tcpHeader + row(0, "00000000:0016") + row(1, "00000000:0050") + row(2, "00000000:01BB") + row(3, "0100007F:1F90") + row(4, "0100007F:0277")
	for i, e := range extra4 {
		v4 += row(5+i, e)
	}
	write(t, root, "proc/net/tcp", v4)
	write(t, root, "proc/net/tcp6", tcpHeader+row(0, "00000000000000000000000000000000:0D3D")+row(1, "00000000000000000000000001000000:0277"))
	return root
}

func find(t *testing.T, cs []Check, id string) Check {
	t.Helper()
	for _, c := range cs {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no %q check in %+v", id, cs)
	return Check{}
}

func TestParseListenersReadsProcNetTcp(t *testing.T) {
	ls := ParseListeners(tcpHeader+row(0, "0100007F:1F90")+row(1, "00000000:0016"), false)
	if len(ls) != 2 || ls[0].Port != 8080 || !ls[0].Loopback || ls[0].Addr != "127.0.0.1" || ls[1].Port != 22 || ls[1].Loopback {
		t.Errorf("v4: %+v", ls)
	}
	ls6 := ParseListeners(tcpHeader+row(0, "00000000000000000000000001000000:0277")+row(1, "00000000000000000000000000000000:0D3D"), true)
	if len(ls6) != 2 || !ls6[0].Loopback || ls6[0].Addr != "::1" || ls6[1].Loopback || ls6[1].Port != 3389 {
		t.Errorf("v6: %+v", ls6)
	}
	// Sockets that are not listening are not listeners.
	if got := ParseListeners(tcpHeader+"   0: 0100007F:1F90 0100007F:9999 01 00000000:00000000 00:00000000 00000000 0 0 1 1 0\n", false); len(got) != 0 {
		t.Errorf("an established connection is not a listener: %+v", got)
	}
}

func TestOpenPortsFlagsWhatIsNotSSHOrWebOrLoopback(t *testing.T) {
	root := ownerProc(t) // 3389 listens on every interface
	c := listeners(Env{Root: root})
	if c.Level != Warn || !strings.Contains(c.Detail, "3389 (Remote Desktop)") {
		t.Errorf("remote desktop: %+v", c)
	}
	for _, ok := range []string{"22", "80", "443", "8080", "631"} {
		if strings.Contains(c.Detail, ok+" (") {
			t.Errorf("port %s must not be reported: %s", ok, c.Detail)
		}
	}
	// A database on every interface is critical.
	db := listeners(Env{Root: ownerProc(t, "00000000:18EB")}) // 6379
	if db.Level != Crit || !strings.Contains(db.Detail, "6379 (Redis)") {
		t.Errorf("redis: %+v", db)
	}
	// Only the expected ports: all clear.
	clean := t.TempDir()
	write(t, clean, "proc/net/tcp", tcpHeader+row(0, "00000000:0016")+row(1, "00000000:01BB")+row(2, "0100007F:1F90"))
	if c := listeners(Env{Root: clean}); c.Level != OK {
		t.Errorf("expected ports: %+v", c)
	}
}

func TestParseSSHDFirstValueWinsAndMatchBlocksAreIgnored(t *testing.T) {
	s := ParseSSHD("")
	if s.PasswordAuthentication != "yes" || s.PermitRootLogin != "prohibit-password" {
		t.Errorf("defaults: %+v", s)
	}
	// Ubuntu: the drop-in is read before the main file, and the first value wins.
	dropin := "PasswordAuthentication no\n"
	main := "PasswordAuthentication yes\nPermitRootLogin without-password\n# comment\nMatch User backup\n    PasswordAuthentication yes\n    PermitRootLogin yes\n"
	s = ParseSSHD(dropin, main)
	if s.PasswordAuthentication != "no" || s.PermitRootLogin != "prohibit-password" {
		t.Errorf("effective: %+v", s)
	}
	if got := ParseSSHD("Match Address 10.0.0.1\n  PermitRootLogin yes\n"); got.PermitRootLogin != "prohibit-password" {
		t.Errorf("a Match block must not change the global value: %+v", got)
	}
	if got := ParseSSHD("permitrootlogin YES\n"); got.PermitRootLogin != "yes" {
		t.Errorf("keys and values are case-insensitive: %+v", got)
	}
}

func TestSSHChecks(t *testing.T) {
	root := t.TempDir()
	if cs := sshChecks(Env{Root: root}); cs != nil {
		t.Error("no sshd_config: no SSH checks")
	}
	write(t, root, "etc/ssh/sshd_config", "Include /etc/ssh/sshd_config.d/*.conf\nPermitRootLogin yes\n")
	cs := sshChecks(Env{Root: root})
	if find(t, cs, "ssh-passwords").Level != Warn || find(t, cs, "ssh-root").Level != Crit {
		t.Errorf("open SSH: %+v", cs)
	}
	// A cloud-init style drop-in turns both off.
	write(t, root, "etc/ssh/sshd_config.d/50-hardening.conf", "PasswordAuthentication no\nPermitRootLogin no\n")
	cs = sshChecks(Env{Root: root})
	if find(t, cs, "ssh-passwords").Level != OK || find(t, cs, "ssh-root").Level != OK {
		t.Errorf("hardened SSH: %+v", cs)
	}
	// Behind a tunnel with passwords on, it is information, not a warning.
	root2 := t.TempDir()
	write(t, root2, "etc/ssh/sshd_config", "PasswordAuthentication yes\n")
	if c := find(t, sshChecks(Env{Root: root2, TunnelMode: true}), "ssh-passwords"); c.Level != Info {
		t.Errorf("tunnel + passwords: %+v", c)
	}
}

func TestFirewallStates(t *testing.T) {
	root := t.TempDir()
	if c := firewall(Env{Root: root}); c.Level != Warn || !strings.Contains(c.Fix, "apt install ufw") {
		t.Errorf("no ufw: %+v", c)
	}
	write(t, root, "etc/ufw/ufw.conf", "ENABLED=no\n")
	if c := firewall(Env{Root: root}); c.Level != Crit {
		t.Errorf("ufw off: %+v", c)
	}
	write(t, root, "etc/ufw/ufw.conf", "ENABLED=yes\n")
	write(t, root, "etc/default/ufw", `DEFAULT_INPUT_POLICY="ACCEPT"`+"\n")
	if c := firewall(Env{Root: root}); c.Level != Warn {
		t.Errorf("accept policy: %+v", c)
	}
	write(t, root, "etc/default/ufw", `DEFAULT_INPUT_POLICY="DROP"`+"\n")
	if c := firewall(Env{Root: root}); c.Level != OK {
		t.Errorf("good ufw: %+v", c)
	}
}

func TestUpdatesChecks(t *testing.T) {
	root := t.TempDir()
	write(t, root, "etc/apt/apt.conf.d/20auto-upgrades", "APT::Periodic::Update-Package-Lists \"1\";\nAPT::Periodic::Unattended-Upgrade \"1\";\n")
	write(t, root, "var/lib/update-notifier/updates-available", "\n12 updates can be applied immediately.\n4 of these updates are standard security updates.\n")
	if c := updates(Env{Root: root}); c.Level != Warn || !strings.Contains(c.Detail, "4 security updates") {
		t.Errorf("pending: %+v", c)
	}
	write(t, root, "var/lib/update-notifier/updates-available", "\n0 updates can be applied immediately.\n")
	if c := updates(Env{Root: root}); c.Level != OK {
		t.Errorf("up to date: %+v", c)
	}
	if c := updates(Env{Root: root, RebootNeeded: true}); c.Level != Warn || !strings.Contains(c.Detail, "reboot") {
		t.Errorf("reboot: %+v", c)
	}
	write(t, root, "etc/apt/apt.conf.d/20auto-upgrades", "APT::Periodic::Unattended-Upgrade \"0\";\n")
	if c := updates(Env{Root: root}); c.Level != Warn || !strings.Contains(c.Fix, "unattended-upgrades") {
		t.Errorf("no auto updates: %+v", c)
	}
}

func TestFail2banAndTunnelProfile(t *testing.T) {
	ctx := context.Background()
	off := func(context.Context, string, ...string) (string, error) { return "inactive", errors.New("exit 3") }
	on := func(context.Context, string, ...string) (string, error) { return "active", nil }
	if c := fail2ban(ctx, Env{Run: off}); c.Level != Warn || !strings.Contains(c.Fix, "apt install fail2ban") {
		t.Errorf("not running: %+v", c)
	}
	if c := fail2ban(ctx, Env{Run: on}); c.Level != OK {
		t.Errorf("running: %+v", c)
	}
	// Behind a tunnel fail2ban cannot see real addresses: say so, no warning, no fix to push.
	if c := fail2ban(ctx, Env{Run: off, TunnelMode: true}); c.Level != Info || c.Fix != "" || !strings.Contains(c.Detail, "tunnel") {
		t.Errorf("tunnel: %+v", c)
	}
	if got := tunnelProfile(Env{}); got != nil {
		t.Error("the tunnel advice must only appear in tunnel mode")
	}
	if got := tunnelProfile(Env{TunnelMode: true}); len(got) != 1 || !strings.Contains(got[0].Fix, "ufw delete allow 443/tcp") {
		t.Errorf("tunnel profile: %+v", got)
	}
}

func TestDockerGroupDashboardAndDataDir(t *testing.T) {
	root := t.TempDir()
	write(t, root, "etc/group", "root:x:0:\ndocker:x:998:deploymate,habib\n")
	if c := dockerGroup(Env{Root: root}); c.Level != Info || !strings.Contains(c.Detail, "deploymate, habib") {
		t.Errorf("docker group: %+v", c)
	}
	if c := dashboard(Env{DashboardAddr: "127.0.0.1:8080"}); c.Level != OK {
		t.Errorf("loopback dashboard: %+v", c)
	}
	if c := dashboard(Env{DashboardAddr: "0.0.0.0:8080"}); c.Level != Warn {
		t.Errorf("public dashboard: %+v", c)
	}
	if c := dashboard(Env{DashboardAddr: ":8080"}); c.Level != Warn {
		t.Errorf("wildcard dashboard: %+v", c)
	}
	dir := t.TempDir()
	os.Chmod(dir, 0o755)
	if c := dataDir(Env{DataDir: dir}); c.Level != Warn || !strings.Contains(c.Fix, "chmod 700") {
		t.Errorf("open data dir: %+v", c)
	}
	os.Chmod(dir, 0o700)
	if c := dataDir(Env{DataDir: dir}); c.Level != OK {
		t.Errorf("private data dir: %+v", c)
	}
}

func TestRedisAndContainerChecks(t *testing.T) {
	if c := redis(Env{Services: []ServiceInfo{{Slug: "a", Type: "redis"}, {Slug: "b", Type: "redis", HasPassword: true}, {Slug: "pg", Type: "postgres"}}}); c.Level != Warn || !strings.Contains(c.Detail, "a") || strings.Contains(c.Detail, "pg") {
		t.Errorf("redis without a password: %+v", c)
	}
	if c := redis(Env{Services: []ServiceInfo{{Slug: "b", Type: "redis", HasPassword: true}}}); c.Level != OK {
		t.Errorf("redis with a password: %+v", c)
	}
	cs := containers(Env{Containers: []ContainerInfo{
		{Name: "dm-web", NoNewPrivileges: true},
		{Name: "dm-bad", PublicBindings: []string{"0.0.0.0:8080 (8080/tcp)"}, Privileged: true},
		{Name: "dm-sock", DockerSocket: true, NoNewPrivileges: true},
	}})
	if find(t, cs, "containers-public").Level != Crit || find(t, cs, "containers-privileged").Level != Crit {
		t.Errorf("bad containers: %+v", cs)
	}
	if nn := find(t, cs, "containers-no-new-privileges"); nn.Level != Info || !strings.Contains(nn.Detail, "1 of 3") {
		t.Errorf("no-new-privileges: %+v", nn)
	}
	clean := containers(Env{Containers: []ContainerInfo{{Name: "dm-ok", NoNewPrivileges: true}}})
	for _, c := range clean {
		if c.Level != OK {
			t.Errorf("a hardened container raised %+v", c)
		}
	}
	if got := containers(Env{ContainersErr: "docker down"}); len(got) != 1 || got[0].Level != Info {
		t.Errorf("docker unavailable: %+v", got)
	}
}

func TestAuditOrdersWorstFirstAndSummarises(t *testing.T) {
	root := ownerProc(t)
	write(t, root, "etc/ufw/ufw.conf", "ENABLED=yes\n")
	write(t, root, "etc/default/ufw", `DEFAULT_INPUT_POLICY="DROP"`+"\n")
	write(t, root, "etc/ssh/sshd_config", "PasswordAuthentication yes\n")
	r := Audit(context.Background(), Env{Root: root, DashboardAddr: "127.0.0.1:8080",
		Run: func(context.Context, string, ...string) (string, error) { return "inactive", errors.New("3") }})
	if r.Level != Warn || r.Headline == "No problems found" {
		t.Fatalf("report: %v %q", r.Level, r.Headline)
	}
	for i := 1; i < len(r.Checks); i++ {
		if r.Checks[i].Level > r.Checks[i-1].Level {
			t.Fatalf("checks are not worst-first: %+v", r.Checks)
		}
	}
	for _, c := range r.Checks {
		if c.ID == "" || c.Title == "" || c.Detail == "" {
			t.Errorf("an empty check leaked into the report: %+v", c)
		}
	}
	// A server that is fine says so.
	clean := t.TempDir()
	write(t, clean, "proc/net/tcp", tcpHeader+row(0, "00000000:0016")+row(1, "0100007F:1F90"))
	write(t, clean, "etc/ufw/ufw.conf", "ENABLED=yes\n")
	write(t, clean, "etc/ssh/sshd_config", "PasswordAuthentication no\nPermitRootLogin no\n")
	write(t, clean, "etc/apt/apt.conf.d/20auto-upgrades", `APT::Periodic::Unattended-Upgrade "1";`+"\n")
	ok := Audit(context.Background(), Env{Root: clean, Run: func(context.Context, string, ...string) (string, error) { return "active", nil }})
	if ok.Level > Info || ok.Headline != "No problems found" {
		t.Errorf("clean server: %v %q %+v", ok.Level, ok.Headline, ok.Checks)
	}
}
