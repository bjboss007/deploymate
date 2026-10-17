// Package harden is `sudo deploymate harden`: opt-in host hardening (firewall, SSH policy, fail2ban,
// automatic updates). Plan() only reads; applying runs each Op and records how to reverse it, so
// `--undo` and the lock-out revert timer can put the old state back. See docs/specs/server-security.md.
package harden

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// System is everything harden touches, so tests can run it without a host.
type System interface {
	Run(ctx context.Context, argv ...string) (string, error)
	Read(path string) (string, bool)
	Write(path, content string) error
	Remove(path string) error
}

type Options struct {
	Tunnel  bool
	SSHFrom string // tunnel only: CIDR that may still reach port 22
	User    string // the human who ran sudo; empty or root when logged in as root
	// TrustKey: an authorized key is enough to turn passwords off, without a logged key login (unattended
	// installs have no login history; the provider put the key there).
	TrustKey bool
}

// Undo is one reversible step: restore a file (Prev nil = it did not exist) or run a command.
type Undo struct {
	File  string   `json:"file,omitempty"`
	Prev  *string  `json:"prev,omitempty"`
	Argv  []string `json:"argv,omitempty"`
	Risky bool     `json:"risky,omitempty"` // could lock someone out: the revert timer undoes these
}

type Op struct {
	Desc  string
	Risky bool
	Do    func(ctx context.Context, s System) ([]Undo, error)
}

type Step struct {
	ID, Title string
	Note      string // why there is nothing to do, or what was left alone
	Ops       []Op
}

func Plan(ctx context.Context, s System, o Options) []Step {
	steps := []Step{updatesStep(ctx, s), sshStep(ctx, s, o), firewallStep(ctx, s, o)}
	if !o.Tunnel { // behind a tunnel every connection looks local, so fail2ban cannot tell attackers apart
		steps = append(steps, fail2banStep(ctx, s))
	}
	return steps
}

// --- helpers ---

func cmd(desc string, risky bool, undo []string, argv ...string) Op {
	return Op{Desc: desc, Risky: risky, Do: func(ctx context.Context, s System) ([]Undo, error) {
		if out, err := s.Run(ctx, argv...); err != nil {
			return nil, fmt.Errorf("%s: %v: %s", strings.Join(argv, " "), err, strings.TrimSpace(out))
		}
		if undo == nil {
			return nil, nil
		}
		return []Undo{{Argv: undo, Risky: risky}}, nil
	}}
}

// writeFile skips (nil) when the file already has this content.
func writeFile(s System, path, content string, risky bool) *Op {
	prev, ok := s.Read(path)
	if ok && prev == content {
		return nil
	}
	return &Op{Desc: "write " + path, Risky: risky, Do: func(ctx context.Context, s System) ([]Undo, error) {
		var p *string
		if ok {
			p = &prev
		}
		if err := s.Write(path, content); err != nil {
			return nil, err
		}
		return []Undo{{File: path, Prev: p, Risky: risky}}, nil
	}}
}

func isActive(ctx context.Context, s System, unit string) bool {
	out, err := s.Run(ctx, "systemctl", "is-active", unit)
	return err == nil && strings.TrimSpace(out) == "active"
}

// --- automatic updates ---

const autoUpgrades = "APT::Periodic::Update-Package-Lists \"1\";\nAPT::Periodic::Unattended-Upgrade \"1\";\n"

func updatesStep(ctx context.Context, s System) Step {
	st := Step{ID: "updates", Title: "Automatic security updates"}
	if _, err := s.Run(ctx, "dpkg", "-s", "unattended-upgrades"); err != nil {
		st.Ops = append(st.Ops, cmd("install unattended-upgrades", false, nil,
			"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "unattended-upgrades"))
	}
	if op := writeFile(s, "/etc/apt/apt.conf.d/20auto-upgrades", autoUpgrades, false); op != nil {
		st.Ops = append(st.Ops, *op)
	}
	if len(st.Ops) == 0 {
		st.Note = "already on"
	}
	return st
}

// --- SSH ---

var userRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

const sshDropin = "/etc/ssh/sshd_config.d/00-deploymate.conf"

func sshStep(ctx context.Context, s System, o Options) Step {
	st := Step{ID: "ssh", Title: "SSH policy"}
	main, _ := s.Read("/etc/ssh/sshd_config")
	if !strings.Contains(main, "sshd_config.d") {
		st.Note = "sshd_config does not include sshd_config.d here; edit /etc/ssh/sshd_config by hand (PermitRootLogin no, PasswordAuthentication no)"
		return st
	}
	lines := []string{"# Written by `deploymate harden`; remove with `sudo deploymate harden --undo`.", "MaxAuthTries 4"}
	var notes []string
	if o.User == "" || o.User == "root" || !userRe.MatchString(o.User) {
		notes = append(notes, "you are not logged in through sudo as a normal user, so root login and passwords are left alone")
	} else {
		lines = append(lines, "PermitRootLogin no")
		if ok, why := keyConfirmed(ctx, s, o.User, o.TrustKey); ok {
			lines = append(lines, "PasswordAuthentication no", "KbdInteractiveAuthentication no")
		} else {
			notes = append(notes, "password login left on: "+why)
		}
	}
	op := writeFile(s, sshDropin, strings.Join(lines, "\n")+"\n", true)
	if op == nil {
		st.Note = "already set"
		return st
	}
	unit := "sshd"
	if isActive(ctx, s, "ssh") {
		unit = "ssh"
	}
	write := *op
	st.Ops = []Op{{Desc: write.Desc + " and reload " + unit, Risky: true, Do: func(ctx context.Context, s System) ([]Undo, error) {
		undo, err := write.Do(ctx, s)
		if err != nil {
			return nil, err
		}
		// A broken config must never reach the running daemon: validate first, restore on failure.
		if out, err := s.Run(ctx, "sshd", "-t"); err != nil {
			rollback(ctx, s, undo)
			return nil, fmt.Errorf("sshd rejected the new settings (restored the old file): %s", strings.TrimSpace(out))
		}
		if out, err := s.Run(ctx, "systemctl", "reload", unit); err != nil {
			rollback(ctx, s, undo)
			return nil, fmt.Errorf("reload %s: %v: %s", unit, err, strings.TrimSpace(out))
		}
		return append(undo, Undo{Argv: []string{"systemctl", "reload", unit}, Risky: true}), nil
	}}}
	st.Note = strings.Join(notes, "; ")
	return st
}

// HasAuthorizedKey reports whether user has at least one line in their authorized_keys.
func HasAuthorizedKey(s System, user string) bool {
	if !userRe.MatchString(user) {
		return false
	}
	keys, _ := s.Read("/home/" + user + "/.ssh/authorized_keys")
	return strings.TrimSpace(keys) != ""
}

// keyConfirmed: the user has an authorized key AND sshd logged them in with a key before, so
// switching passwords off cannot strand them.
func keyConfirmed(ctx context.Context, s System, user string, trust bool) (bool, string) {
	if !HasAuthorizedKey(s, user) {
		return false, "no key in /home/" + user + "/.ssh/authorized_keys (run ssh-copy-id first)"
	}
	if trust {
		return true, ""
	}
	out, err := s.Run(ctx, "journalctl", "-u", "ssh", "-u", "sshd", "--no-pager", "-q", "--since", "-30d",
		"-g", "Accepted publickey for "+user+" ")
	if err != nil || strings.TrimSpace(out) == "" {
		return false, "no key login by " + user + " found in the SSH log; log in once with your key, then run this again"
	}
	return true, ""
}

func rollback(ctx context.Context, s System, undo []Undo) {
	for i := len(undo) - 1; i >= 0; i-- {
		runUndo(ctx, s, undo[i])
	}
}

func runUndo(ctx context.Context, s System, u Undo) error {
	switch {
	case u.File != "" && u.Prev == nil:
		return s.Remove(u.File)
	case u.File != "":
		return s.Write(u.File, *u.Prev)
	default:
		_, err := s.Run(ctx, u.Argv...)
		return err
	}
}

// --- firewall ---

type ufwRule struct{ To, Action, From string }

// parseUFW reads `ufw status` rule lines; v6 duplicates collapse because `ufw delete` removes both.
func parseUFW(status string) (rules []ufwRule) {
	seen := map[string]bool{}
	inRules := false
	for _, l := range strings.Split(status, "\n") {
		if strings.HasPrefix(l, "--") {
			inRules = true
			continue
		}
		if !inRules || strings.TrimSpace(l) == "" {
			continue
		}
		l = strings.ReplaceAll(l, " (v6)", "")
		f := regexp.MustCompile(`\s{2,}`).Split(strings.TrimSpace(l), -1)
		if len(f) < 2 {
			continue
		}
		fields := strings.Fields(f[1])
		if len(fields) == 0 {
			continue
		}
		to := f[0]
		if m := regexp.MustCompile(`^\S+ \((.+)\)$`).FindStringSubmatch(to); m != nil {
			to = m[1] // "22/tcp (OpenSSH)" is the OpenSSH profile: delete it by that name
		}
		r := ufwRule{To: to, Action: fields[0]}
		if len(f) > 2 {
			r.From = f[2]
		}
		if k := r.To + "|" + r.Action + "|" + r.From; !seen[k] {
			seen[k] = true
			rules = append(rules, r)
		}
	}
	return
}

func portOf(to string) string {
	if to == "OpenSSH" {
		return "22"
	}
	return strings.TrimSuffix(strings.SplitN(to, " ", 2)[0], "/tcp")
}

func firewallStep(ctx context.Context, s System, o Options) Step {
	st := Step{ID: "firewall", Title: "Firewall (ufw)"}
	status, err := s.Run(ctx, "ufw", "status", "verbose")
	if err != nil {
		st.Note = "ufw is missing or not usable (sudo apt install ufw), then run this again"
		return st
	}
	rules := parseUFW(status)
	active := !strings.Contains(status, "Status: inactive")
	add := func(desc string, ufwArgs ...string) {
		st.Ops = append(st.Ops, cmd(desc, true, append([]string{"ufw", "delete"}, ufwArgs...), append([]string{"ufw"}, ufwArgs...)...))
	}
	del := func(desc, action, to string) {
		st.Ops = append(st.Ops, cmd(desc, true, []string{"ufw", action, to}, "ufw", "delete", action, to))
	}
	anywhere := func(r ufwRule) bool { return strings.HasPrefix(r.From, "Anywhere") }
	find := func(port, action string) *ufwRule {
		for _, r := range rules {
			if portOf(r.To) == port && r.Action == action && anywhere(r) {
				return &r
			}
		}
		return nil
	}

	if o.Tunnel {
		for _, p := range []string{"80", "443"} {
			if r := find(p, "ALLOW"); r != nil {
				del("close port "+p+" (the tunnel connects out, it does not need it)", "allow", r.To)
			}
		}
		if o.SSHFrom == "" {
			st.Note = "port 22 left as it is; pass --ssh-from <your network, e.g. 192.168.1.0/24> to close it to everyone else"
			if !active && len(st.Ops) == 0 {
				st.Note = "ufw is off; pass --ssh-from <your network> to switch it on with SSH limited to that network"
			}
			return st
		}
		if _, _, err := net.ParseCIDR(o.SSHFrom); err != nil {
			st.Note = "--ssh-from " + o.SSHFrom + " is not a network such as 192.168.1.0/24"
			st.Ops = nil
			return st
		}
		st.Ops = append(st.Ops, cmd("allow SSH only from "+o.SSHFrom, true,
			[]string{"ufw", "delete", "allow", "from", o.SSHFrom, "to", "any", "port", "22", "proto", "tcp"},
			"ufw", "allow", "from", o.SSHFrom, "to", "any", "port", "22", "proto", "tcp"))
		for _, act := range []string{"ALLOW", "LIMIT"} {
			if r := find("22", act); r != nil {
				del("close port 22 to everyone else", strings.ToLower(act), r.To)
			}
		}
		if !active {
			st.Ops = append(st.Ops, cmd("switch ufw on", true, []string{"ufw", "disable"}, "ufw", "--force", "enable"))
		}
		return st
	}

	// Public server: 22 rate-limited, 80/443 open, everything else denied.
	if find("22", "LIMIT") == nil {
		add("limit new SSH connections per address (ufw limit 22/tcp)", "limit", "22/tcp")
		if r := find("22", "ALLOW"); r != nil {
			del("replace the open SSH rule with the limited one", "allow", r.To)
		}
	}
	for _, p := range []string{"80", "443"} {
		if find(p, "ALLOW") == nil {
			add("allow port "+p, "allow", p+"/tcp")
		}
	}
	if m := regexp.MustCompile(`Default: (\w+) \(incoming\)`).FindStringSubmatch(status); m == nil || m[1] != "deny" {
		prev := "allow"
		if m != nil {
			prev = m[1]
		}
		st.Ops = append(st.Ops, cmd("deny all other incoming connections", true,
			[]string{"ufw", "default", prev, "incoming"}, "ufw", "default", "deny", "incoming"))
	}
	if !active {
		st.Ops = append(st.Ops, cmd("switch ufw on", true, []string{"ufw", "disable"}, "ufw", "--force", "enable"))
	}
	if len(st.Ops) == 0 {
		st.Note = "already set"
	}
	return st
}

// --- fail2ban ---

const jail = "[sshd]\nenabled = true\nmaxretry = 5\nfindtime = 10m\nbantime = 1h\n"

func fail2banStep(ctx context.Context, s System) Step {
	st := Step{ID: "fail2ban", Title: "fail2ban for SSH"}
	if _, err := s.Run(ctx, "dpkg", "-s", "fail2ban"); err != nil {
		st.Ops = append(st.Ops, cmd("install fail2ban", false, nil,
			"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "fail2ban"))
	}
	if op := writeFile(s, "/etc/fail2ban/jail.d/deploymate.local", jail, false); op != nil {
		st.Ops = append(st.Ops, *op)
	}
	if len(st.Ops) > 0 || !isActive(ctx, s, "fail2ban") {
		st.Ops = append(st.Ops, cmd("start fail2ban", false, nil, "systemctl", "enable", "--now", "fail2ban"),
			cmd("reload fail2ban", false, nil, "systemctl", "restart", "fail2ban"))
	} else {
		st.Note = "already running"
	}
	return st
}

// --- applying, state, undo ---

type State struct {
	Undos []Undo `json:"undos"`
}

func LoadState(path string) State {
	var st State
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &st)
	}
	return st
}

func SaveState(path string, st State) error {
	raw, _ := json.MarshalIndent(st, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// Apply runs every Op in order, saving the undo record after each so a failure midway is still reversible.
func Apply(ctx context.Context, s System, steps []Step, statePath string, log func(string)) (risky bool, err error) {
	st := LoadState(statePath)
	for _, step := range steps {
		for _, op := range step.Ops {
			log("  " + op.Desc)
			undo, err := op.Do(ctx, s)
			st.Undos = append(st.Undos, undo...)
			if serr := SaveState(statePath, st); serr != nil {
				return risky, serr
			}
			if err != nil {
				return risky, err
			}
			risky = risky || op.Risky
		}
	}
	return risky, nil
}

// Revert reverses recorded changes newest first; onlyRisky leaves the harmless ones (the revert timer).
func Revert(ctx context.Context, s System, statePath string, onlyRisky bool, log func(string)) error {
	st := LoadState(statePath)
	var keep []Undo
	var failed []string
	for i := len(st.Undos) - 1; i >= 0; i-- {
		u := st.Undos[i]
		if onlyRisky && !u.Risky {
			keep = append([]Undo{u}, keep...)
			continue
		}
		what := u.File
		if what == "" {
			what = strings.Join(u.Argv, " ")
		}
		log("  undo " + what)
		if err := runUndo(ctx, s, u); err != nil {
			failed = append(failed, what+": "+err.Error())
			keep = append([]Undo{u}, keep...)
		}
	}
	if err := SaveState(statePath, State{Undos: keep}); err != nil {
		return err
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not undo: %s", strings.Join(failed, "; "))
	}
	return nil
}

// OS is the real System.
type OS struct{}

func (OS) Run(ctx context.Context, argv ...string) (string, error) {
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	return string(out), err
}
func (OS) Read(path string) (string, bool) {
	b, err := os.ReadFile(path)
	return string(b), err == nil
}
func (OS) Write(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
func (OS) Remove(path string) error { return os.Remove(path) }
