package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/habibmuhammad/deploymate/internal/harden"
)

const revertUnit = "deploymate-harden-revert"

// hardenCmd: `sudo deploymate harden [--apply] [--ssh-from CIDR] [--profile auto|public|tunnel]`,
// then `--confirm` once you can still log in, or `--undo` to put everything back.
func hardenCmd(args []string) error {
	fs := flag.NewFlagSet("harden", flag.ExitOnError)
	apply := fs.Bool("apply", false, "make the changes (default: only show the plan)")
	sshFrom := fs.String("ssh-from", "", "tunnel servers: the network that may still reach SSH, e.g. 192.168.1.0/24")
	profile := fs.String("profile", "auto", "auto, public or tunnel")
	minutes := fs.Int("timeout", 5, "minutes to confirm before firewall/SSH changes are reverted")
	confirm := fs.Bool("confirm", false, "keep the changes: cancel the pending revert")
	revert := fs.Bool("revert", false, "undo only the firewall/SSH changes (what the timer runs)")
	undo := fs.Bool("undo", false, "undo everything harden changed")
	trustKey := fs.Bool("trust-ssh-key", false, "unattended installs: an authorized key is enough to turn password login off")
	noTimer := fs.Bool("no-timer", false, "unattended installs: no revert timer (nobody is there to confirm)")
	forceRange := fs.Bool("force-range", false, "accept an --ssh-from range this machine is not on")
	user := fs.String("user", os.Getenv("SUDO_USER"), "the login user whose authorized_keys count (default: who ran sudo)")
	_ = fs.Parse(args)
	if os.Geteuid() != 0 {
		return fmt.Errorf("run with sudo: it changes the firewall and SSH settings")
	}
	ctx := context.Background()
	s := harden.OS{}
	dataDir := envOr("DEPLOYMATE_DATA_DIR", unitEnv("DEPLOYMATE_DATA_DIR"))
	if dataDir == "" {
		dataDir = "/var/lib/deploymate"
	}
	state := filepath.Join(dataDir, "harden-state.json")
	say := func(m string) { fmt.Println(m) }

	switch {
	case *confirm:
		_ = exec.Command("systemctl", "stop", revertUnit+".timer").Run()
		fmt.Println("Confirmed. The changes stay. `sudo deploymate harden --undo` reverses them later.")
		return nil
	case *revert, *undo:
		_ = exec.Command("systemctl", "stop", revertUnit+".timer").Run()
		return harden.Revert(ctx, s, state, *revert, say)
	}

	tunnel := *profile == "tunnel" || (*profile == "auto" && unitEnv("DEPLOYMATE_LE_MODE") == "off")
	if (*trustKey || *noTimer) && !harden.HasAuthorizedKey(s, *user) {
		return fmt.Errorf("refusing --trust-ssh-key/--no-timer: %q has no authorized key, so password login could be the only way in (pass --user, and install a key first)", *user)
	}
	steps := harden.Plan(ctx, s, harden.Options{Tunnel: tunnel, SSHFrom: *sshFrom, User: *user, TrustKey: *trustKey, LocalNets: harden.LocalNetworks(), ForceRange: *forceRange})
	kind := "public server"
	if tunnel {
		kind = "tunnel server"
	}
	fmt.Printf("Hardening plan for a %s:\n", kind)
	risky, changes := false, 0
	for _, st := range steps {
		fmt.Printf("\n%s\n", st.Title)
		for _, op := range st.Ops {
			fmt.Println("  - " + op.Desc)
			risky, changes = risky || op.Risky, changes+1
		}
		if st.Note != "" {
			fmt.Println("  note: " + st.Note)
		}
		if len(st.Ops) == 0 && st.Note == "" {
			fmt.Println("  nothing to do")
		}
	}
	if changes == 0 {
		fmt.Println("\nNothing to change.")
		return nil
	}
	if !*apply {
		fmt.Println("\nThis was a dry run. Add --apply to make these changes.")
		return nil
	}
	if risky && !*noTimer {
		// The timer lives in systemd, not in this terminal, so it still fires if the connection drops.
		exe, _ := os.Executable()
		_ = exec.Command("systemctl", "stop", revertUnit+".timer").Run()
		out, err := exec.Command("systemd-run", "--unit="+revertUnit, fmt.Sprintf("--on-active=%dm", *minutes), exe, "harden", "--revert").CombinedOutput()
		if err != nil {
			return fmt.Errorf("could not start the revert timer, so nothing was changed: %v: %s", err, out)
		}
	}
	fmt.Println("\nApplying:")
	if _, err := harden.Apply(ctx, s, steps, state, say); err != nil {
		return fmt.Errorf("%w (changes so far are recorded: `sudo deploymate harden --undo` reverses them)", err)
	}
	if risky && !*noTimer {
		fmt.Printf("\nDone. Firewall/SSH settings change back in %d minutes unless you confirm.\nOpen a NEW terminal, log in again, and if it works run:\n\n  sudo deploymate harden --confirm\n", *minutes)
	} else {
		fmt.Println("\nDone.")
	}
	return nil
}
