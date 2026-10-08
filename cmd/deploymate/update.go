package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/habibmuhammad/deploymate/internal/updater"
)

const (
	installedBinary = "/usr/local/bin/deploymate"
	installedUnit   = "/etc/systemd/system/deploymate.service"
)

// unitEnv reads `Environment=KEY=value` from the installed systemd unit, so an
// update uses the data dir and address this server was actually set up with.
func unitEnv(key string) string {
	raw, err := os.ReadFile(installedUnit)
	if err != nil {
		return ""
	}
	prefix := "Environment=" + key + "="
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return strings.TrimPrefix(strings.TrimSpace(line), prefix)
		}
	}
	return ""
}

// updateCmd: `sudo deploymate update [--check] [--version vX.Y.Z] [--from archive.tar.gz]`.
func updateCmd(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	check := fs.Bool("check", false, "only report whether a newer release exists")
	ver := fs.String("version", "latest", "a tag such as v0.1.2 (also how you go back)")
	from := fs.String("from", "", "install this local release archive instead of downloading (checksum is printed, not verified)")
	force := fs.Bool("force", false, "reinstall even when already on this version")
	repo := fs.String("repo", envOr("DEPLOYMATE_REPO", "bjboss007/deploymate"), "owner/name on GitHub")
	_ = fs.Parse(args)
	ctx := context.Background()

	if *check {
		tag, err := updater.LatestTag(ctx, *repo)
		if err != nil {
			return err
		}
		if tag == version {
			fmt.Printf("up to date (%s)\n", version)
		} else {
			fmt.Printf("running %s, latest is %s — run: sudo deploymate update\n", version, tag)
		}
		return nil
	}

	if runtime.GOOS != "linux" {
		return fmt.Errorf("update manages the systemd service, so it only runs on the Linux server")
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("run as root: sudo deploymate update")
	}

	target := *ver
	if *from == "" && target == "latest" {
		tag, err := updater.LatestTag(ctx, *repo)
		if err != nil {
			return err
		}
		target = tag
		if tag == version && !*force {
			fmt.Printf("already on the latest release (%s). Use --force to reinstall it.\n", version)
			return nil
		}
	}
	if *from == "" && target == version && !*force {
		fmt.Printf("already on %s. Use --force to reinstall it.\n", version)
		return nil
	}

	tmp, err := os.MkdirTemp("", "dm-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	archive := *from
	if archive == "" {
		base := updater.ResolveBase(*repo, target, os.Getenv("DEPLOYMATE_BASE_URL"))
		name := updater.ArchiveName()
		archive = filepath.Join(tmp, name)
		fmt.Printf("==> downloading %s (%s)\n", name, target)
		if err := updater.Fetch(ctx, base+"/"+name, archive); err != nil {
			return err
		}
		sums := filepath.Join(tmp, "checksums.txt")
		if err := updater.Fetch(ctx, base+"/checksums.txt", sums); err != nil {
			return err
		}
		fmt.Println("==> verifying checksum")
		if err := updater.VerifyChecksum(archive, sums); err != nil {
			return fmt.Errorf("refusing to install: %w", err)
		}
	} else {
		sum, err := updater.SHA256File(archive)
		if err != nil {
			return err
		}
		fmt.Printf("==> installing from %s\n    sha256 %s\n", archive, sum)
	}

	pkg := filepath.Join(tmp, "pkg")
	if err := os.Mkdir(pkg, 0o700); err != nil {
		return err
	}
	if err := updater.Extract(archive, pkg, map[string]string{
		"deploymate":                "deploymate",
		"deploy/deploymate.service": "deploymate.service",
	}); err != nil {
		return err
	}

	dataDir := firstNonEmpty(os.Getenv("DEPLOYMATE_DATA_DIR"), unitEnv("DEPLOYMATE_DATA_DIR"), "/var/lib/deploymate")
	addr := firstNonEmpty(unitEnv("DEPLOYMATE_ADDR"), "127.0.0.1:8080")
	in := &updater.Installer{
		Binary: installedBinary, UnitPath: installedUnit, DataDir: dataDir, Out: os.Stdout,
		Systemctl: func(ctx context.Context, a ...string) error {
			out, err := exec.CommandContext(ctx, "systemctl", a...).CombinedOutput()
			if err != nil {
				return fmt.Errorf("systemctl %s: %v: %s", strings.Join(a, " "), err, strings.TrimSpace(string(out)))
			}
			return nil
		},
		Healthy: func(ctx context.Context) error {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
			resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
			if err != nil {
				return err
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("healthz answered %s", resp.Status)
			}
			return nil
		},
		SmokeTest: func(ctx context.Context, bin string) (string, error) {
			out, err := exec.CommandContext(ctx, bin, "version").CombinedOutput()
			return strings.TrimSpace(string(out)), err
		},
	}
	if err := in.Apply(ctx, pkg); err != nil {
		return fmt.Errorf("update failed, previous version restored: %w", err)
	}
	fmt.Println("Done. Traefik and your apps were not touched. The previous binary is kept at " + installedBinary + ".prev.")
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
