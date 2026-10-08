package updater

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Installer carries everything an update touches, so tests can point it at a
// temp directory and a fake service manager.
type Installer struct {
	Binary   string // /usr/local/bin/deploymate
	UnitPath string // /etc/systemd/system/deploymate.service
	DataDir  string // /var/lib/deploymate
	// Systemctl runs `systemctl <args>`.
	Systemctl func(ctx context.Context, args ...string) error
	// Healthy reports whether the freshly started service answers.
	Healthy func(ctx context.Context) error
	// SmokeTest runs the new binary's `version` to prove it executes on this host.
	SmokeTest func(ctx context.Context, bin string) (string, error)
	Out       io.Writer
	// HealthTimeout bounds how long the new version gets to come up.
	HealthTimeout time.Duration
}

func (in *Installer) say(format string, a ...any) { fmt.Fprintf(in.Out, "==> "+format+"\n", a...) }

// Apply installs the binary (and unit, when it changed) from an extracted
// package directory. On any failure after the service was stopped it restores
// the previous binary and database and starts the old version again.
func (in *Installer) Apply(ctx context.Context, pkgDir string) (err error) {
	newBin := filepath.Join(pkgDir, "deploymate")
	ver, err := in.SmokeTest(ctx, newBin)
	if err != nil {
		return fmt.Errorf("the new binary does not run on this machine: %w", err)
	}
	in.say("new version: %s", ver)

	// Unit file: carry over the data dir this install already uses.
	unitNew := ""
	if raw, e := os.ReadFile(filepath.Join(pkgDir, "deploymate.service")); e == nil {
		unitNew = strings.ReplaceAll(string(raw), "/var/lib/deploymate", in.DataDir)
	}
	unitOld, _ := os.ReadFile(in.UnitPath)
	unitChanged := unitNew != "" && unitNew != string(unitOld)

	in.say("stopping the service (your apps keep running)")
	if err := in.Systemctl(ctx, "stop", "deploymate"); err != nil {
		return fmt.Errorf("stop: %w", err)
	}

	prev := in.Binary + ".prev"
	dbBackup := filepath.Join(in.DataDir, "data.db.pre-update")
	restore := func() {
		in.say("restoring the previous version")
		_ = os.Rename(prev, in.Binary)
		if _, e := os.Stat(dbBackup); e == nil {
			for _, ext := range []string{"", "-wal", "-shm"} {
				_ = os.Remove(filepath.Join(in.DataDir, "data.db"+ext))
			}
			_ = copyFile(dbBackup, filepath.Join(in.DataDir, "data.db"), 0o600)
		}
		if unitChanged {
			_ = os.WriteFile(in.UnitPath, unitOld, 0o644)
			_ = in.Systemctl(ctx, "daemon-reload")
		}
		_ = in.Systemctl(ctx, "start", "deploymate")
	}
	defer func() {
		if err != nil {
			restore()
		}
	}()

	if _, e := os.Stat(filepath.Join(in.DataDir, "data.db")); e == nil {
		in.say("backing up the database to %s", dbBackup)
		if err := copyFile(filepath.Join(in.DataDir, "data.db"), dbBackup, 0o600); err != nil {
			return fmt.Errorf("database backup: %w", err)
		}
		_ = chownLike(dbBackup, filepath.Join(in.DataDir, "data.db"))
	}

	in.say("installing the binary (old one kept as %s)", prev)
	if err := os.Remove(prev); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(in.Binary, prev); err != nil && !os.IsNotExist(err) {
		return err
	}
	staged := in.Binary + ".new"
	if err := copyFile(newBin, staged, 0o755); err != nil {
		return err
	}
	if err := os.Rename(staged, in.Binary); err != nil {
		return err
	}
	if unitChanged {
		in.say("updating the systemd unit")
		if err := os.WriteFile(in.UnitPath, []byte(unitNew), 0o644); err != nil {
			return err
		}
		if err := in.Systemctl(ctx, "daemon-reload"); err != nil {
			return err
		}
	}

	in.say("starting the new version")
	if err := in.Systemctl(ctx, "start", "deploymate"); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	timeout := in.HealthTimeout
	if timeout == 0 {
		timeout = 45 * time.Second
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last error
	for {
		if last = in.Healthy(hctx); last == nil {
			break
		}
		select {
		case <-hctx.Done():
			return fmt.Errorf("the new version did not become healthy: %w", last)
		case <-time.After(time.Second):
		}
	}
	in.say("healthy. Updated to %s", ver)
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
