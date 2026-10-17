package harden

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

type fake struct {
	files map[string]string
	out   map[string]string // joined argv -> output; missing key with err: see fail
	fail  map[string]bool
	ran   []string
}

func newFake() *fake {
	return &fake{files: map[string]string{}, out: map[string]string{}, fail: map[string]bool{}}
}
func (f *fake) Run(_ context.Context, argv ...string) (string, error) {
	k := strings.Join(argv, " ")
	f.ran = append(f.ran, k)
	if f.fail[k] {
		return "boom", errors.New("exit 1")
	}
	return f.out[k], nil
}
func (f *fake) Read(p string) (string, bool) { v, ok := f.files[p]; return v, ok }
func (f *fake) Write(p, c string) error      { f.files[p] = c; return nil }
func (f *fake) Remove(p string) error        { delete(f.files, p); return nil }

const ufwOpen = `Status: active
Default: allow (incoming), allow (outgoing), disabled (routed)

To                         Action      From
--                         ------      ----
OpenSSH                    ALLOW IN    Anywhere
80/tcp                     ALLOW IN    Anywhere
443/tcp                    ALLOW IN    Anywhere
OpenSSH (v6)               ALLOW IN    Anywhere (v6)
80/tcp (v6)                ALLOW IN    Anywhere (v6)
443/tcp (v6)               ALLOW IN    Anywhere (v6)
`

func stepByID(steps []Step, id string) Step {
	for _, s := range steps {
		if s.ID == id {
			return s
		}
	}
	return Step{}
}

func descs(s Step) string {
	var d []string
	for _, o := range s.Ops {
		d = append(d, o.Desc)
	}
	return strings.Join(d, "|")
}

func TestParseUFWCollapsesV6(t *testing.T) {
	if got := parseUFW(ufwOpen); len(got) != 3 || got[0].To != "OpenSSH" || got[1].Action != "ALLOW" {
		t.Fatalf("rules: %+v", got)
	}
}

func TestPublicFirewallLimitsSSH(t *testing.T) {
	f := newFake()
	f.out["ufw status verbose"] = ufwOpen
	st := firewallStep(context.Background(), f, Options{})
	if !strings.Contains(descs(st), "limit") || !strings.Contains(descs(st), "deny all other") {
		t.Fatalf("plan: %s", descs(st))
	}
	if _, err := Apply(context.Background(), f, []Step{st}, filepath.Join(t.TempDir(), "s.json"), func(string) {}); err != nil {
		t.Fatal(err)
	}
	ran := strings.Join(f.ran, "\n")
	// limit goes in before the open rule is removed, so there is never a moment without SSH.
	if strings.Index(ran, "ufw limit 22/tcp") > strings.Index(ran, "ufw delete allow OpenSSH") || !strings.Contains(ran, "ufw default deny incoming") {
		t.Fatalf("order/commands:\n%s", ran)
	}
}

func TestTunnelFirewallClosesWebPortsAndNeedsCIDRForSSH(t *testing.T) {
	f := newFake()
	f.out["ufw status verbose"] = ufwOpen
	st := firewallStep(context.Background(), f, Options{Tunnel: true})
	if len(st.Ops) != 2 || !strings.Contains(st.Note, "--ssh-from") {
		t.Fatalf("without a network 22 stays: %s / %s", descs(st), st.Note)
	}
	st = firewallStep(context.Background(), f, Options{Tunnel: true, SSHFrom: "192.168.1.0/24"})
	if !strings.Contains(descs(st), "only from 192.168.1.0/24") || !strings.Contains(descs(st), "close port 22") {
		t.Fatalf("plan: %s", descs(st))
	}
	if st = firewallStep(context.Background(), f, Options{Tunnel: true, SSHFrom: "nonsense"}); len(st.Ops) != 0 {
		t.Fatalf("bad CIDR must plan nothing: %s", descs(st))
	}
}

func TestFirewallMissingUFW(t *testing.T) {
	f := newFake()
	f.fail["ufw status verbose"] = true
	if st := firewallStep(context.Background(), f, Options{}); len(st.Ops) != 0 || st.Note == "" {
		t.Fatalf("%+v", st)
	}
}

func sshFake() *fake {
	f := newFake()
	f.files["/etc/ssh/sshd_config"] = "Include /etc/ssh/sshd_config.d/*.conf\n"
	f.files["/home/habib/.ssh/authorized_keys"] = "ssh-ed25519 AAAA key\n"
	f.out["systemctl is-active ssh"] = "active\n"
	return f
}

func TestSSHPasswordsOffOnlyAfterKeyLogin(t *testing.T) {
	f := sshFake()
	st := sshStep(context.Background(), f, Options{User: "habib"})
	if _, err := Apply(context.Background(), f, []Step{st}, filepath.Join(t.TempDir(), "s.json"), func(string) {}); err != nil {
		t.Fatal(err)
	}
	if c := f.files[sshDropin]; !strings.Contains(c, "PermitRootLogin no") || strings.Contains(c, "PasswordAuthentication") {
		t.Fatalf("no key login in the log yet, passwords must stay on:\n%s", c)
	}
	if !strings.Contains(st.Note, "log in once with your key") {
		t.Fatalf("note: %q", st.Note)
	}

	f = sshFake()
	f.out["journalctl -u ssh -u sshd --no-pager -q --since -30d -g Accepted publickey for habib "] = "Oct 16 sshd: Accepted publickey for habib from 1.2.3.4\n"
	st = sshStep(context.Background(), f, Options{User: "habib"})
	if _, err := Apply(context.Background(), f, []Step{st}, filepath.Join(t.TempDir(), "s.json"), func(string) {}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.files[sshDropin], "PasswordAuthentication no") {
		t.Fatalf("key confirmed, passwords should go off:\n%s", f.files[sshDropin])
	}
}

func TestSSHRootAndNoKeyLeaveEverythingAlone(t *testing.T) {
	f := sshFake()
	st := sshStep(context.Background(), f, Options{User: "root"})
	_, _ = Apply(context.Background(), f, []Step{st}, filepath.Join(t.TempDir(), "s.json"), func(string) {})
	if c := f.files[sshDropin]; strings.Contains(c, "PermitRootLogin") || strings.Contains(c, "PasswordAuthentication") {
		t.Fatalf("running as root must not touch root/password login:\n%s", c)
	}
	f = sshFake()
	f.files["/home/habib/.ssh/authorized_keys"] = ""
	if ok, why := keyConfirmed(context.Background(), f, "habib", false); ok || !strings.Contains(why, "ssh-copy-id") {
		t.Fatal(ok, why)
	}
}

func TestSSHBadConfigIsRolledBack(t *testing.T) {
	f := sshFake()
	f.fail["sshd -t"] = true
	st := sshStep(context.Background(), f, Options{User: "habib"})
	_, err := Apply(context.Background(), f, []Step{st}, filepath.Join(t.TempDir(), "s.json"), func(string) {})
	if err == nil {
		t.Fatal("expected an error")
	}
	if _, ok := f.files[sshDropin]; ok {
		t.Fatal("rejected config must not stay on disk")
	}
	for _, r := range f.ran {
		if strings.HasPrefix(r, "systemctl reload") {
			t.Fatal("must not reload sshd with a rejected config")
		}
	}
}

func TestTunnelHasNoFail2ban(t *testing.T) {
	f := newFake()
	if stepByID(Plan(context.Background(), f, Options{Tunnel: true}), "fail2ban").ID != "" {
		t.Fatal("fail2ban is useless behind a tunnel")
	}
	if stepByID(Plan(context.Background(), f, Options{}), "fail2ban").ID == "" {
		t.Fatal("public servers get fail2ban")
	}
}

func TestUndoRestoresFilesAndRulesNewestFirst(t *testing.T) {
	f := sshFake()
	f.files[sshDropin] = "old\n"
	state := filepath.Join(t.TempDir(), "s.json")
	st := sshStep(context.Background(), f, Options{User: "habib"})
	if _, err := Apply(context.Background(), f, []Step{st}, state, func(string) {}); err != nil {
		t.Fatal(err)
	}
	f.ran = nil
	if err := Revert(context.Background(), f, state, true, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if f.files[sshDropin] != "old\n" {
		t.Fatalf("file not restored: %q", f.files[sshDropin])
	}
	if len(f.ran) != 1 || f.ran[0] != "systemctl reload ssh" {
		t.Fatalf("ran %v", f.ran)
	}
	if len(LoadState(state).Undos) != 0 {
		t.Fatal("state should be empty after undo")
	}
}

func TestParseUFWNamesTheAppProfile(t *testing.T) {
	got := parseUFW("To  Action  From\n--  ------  ----\n22/tcp (OpenSSH)           ALLOW IN    Anywhere\n22/tcp (OpenSSH (v6))      ALLOW IN    Anywhere (v6)\n")
	if len(got) != 1 || got[0].To != "OpenSSH" || portOf(got[0].To) != "22" {
		t.Fatalf("%+v", got)
	}
}

func TestTrustKeyNeedsOnlyAKeyNotALoggedLogin(t *testing.T) {
	f := sshFake() // has a key, no login in the log
	st := sshStep(context.Background(), f, Options{User: "habib", TrustKey: true})
	if _, err := Apply(context.Background(), f, []Step{st}, filepath.Join(t.TempDir(), "s.json"), func(string) {}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.files[sshDropin], "PasswordAuthentication no") {
		t.Fatalf("a trusted key should switch passwords off:\n%s", f.files[sshDropin])
	}
	f = sshFake()
	f.files["/home/habib/.ssh/authorized_keys"] = ""
	st = sshStep(context.Background(), f, Options{User: "habib", TrustKey: true})
	_, _ = Apply(context.Background(), f, []Step{st}, filepath.Join(t.TempDir(), "s.json"), func(string) {})
	if strings.Contains(f.files[sshDropin], "PasswordAuthentication") {
		t.Fatal("trusting a key that does not exist must never turn passwords off")
	}
	if HasAuthorizedKey(f, "../etc") || HasAuthorizedKey(f, "") {
		t.Fatal("user names are validated")
	}
}
