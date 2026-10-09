// Command deploymate is the DeployMate control plane: a single binary that
// runs the dashboard/API and orchestrates Docker on the host.
//
//	serve            run the server (default)
//	setup-admin      create the owner user interactively
//	seed-git-source  link an app to a git source (test-seeding; see seed.go)
//	mcp              MCP server over stdio for AI agents (see mcp.go)
//	seed-demo        fill an EMPTY data dir with a fictional fleet (website tour)
//	version          print the version
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/habibmuhammad/deploymate/internal/alerts"
	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/backup"
	"github.com/habibmuhammad/deploymate/internal/config"
	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/demo"
	"github.com/habibmuhammad/deploymate/internal/dns"
	"github.com/habibmuhammad/deploymate/internal/httpserver"
	"github.com/habibmuhammad/deploymate/internal/jobs"
	"github.com/habibmuhammad/deploymate/internal/monitor"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/services"
	"github.com/habibmuhammad/deploymate/internal/sse"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// version is stamped at release time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if err := run(); err != nil {
		slog.Error("deploymate", "err", err)
		os.Exit(1)
	}
}

func run() error {
	flag.Parse()
	switch flag.Arg(0) {
	case "setup-admin":
		return setupAdmin()
	case "seed-git-source":
		return seedGitSource()
	case "mcp":
		return runMCP()
	case "update":
		return updateCmd(flag.Args()[1:])
	case "seed-demo":
		return seedDemo()
	case "version":
		fmt.Println("deploymate", version)
		return nil
	default:
		return serve()
	}
}

func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()

	// The key encrypts secrets at rest; create it up front so it exists
	// before anything can write secrets.
	encKey, err := crypto.LoadOrCreateKey(cfg.KeyPath)
	if err != nil {
		return err
	}

	rt, err := runtime.NewDocker()
	if err != nil {
		return err
	}
	defer rt.Close()

	if cfg.SetupEmail != "" && cfg.SetupPassword != "" {
		if err := ensureOwner(st, cfg.SetupEmail, cfg.SetupPassword); err != nil {
			return err
		}
	}

	go pruneSessions(st)

	events := sse.NewBroker()
	prov := services.NewProvisioner(st, rt, encKey, httpserver.NetworkName)

	// Auto-DNS: new apps get their preview CNAME via the Cloudflare API
	// so the edge can issue a per-app cert; deleting an app removes it.
	// Best-effort; nil manager disables it (logs and config docs never
	// include the token).
	var dnsManager dns.Manager
	if cfg.CloudflareEnabled() {
		dnsManager = dns.NewCloudflare(cfg.CloudflareAPIToken, cfg.CloudflareZoneID, cfg.CloudflareTunnelID)
		slog.Info("auto-dns enabled", "zone", cfg.CloudflareZoneID, "tunnel", cfg.CloudflareTunnelID)
	}
	// Alert dispatcher: worker + monitor emit catalog events; targets
	// receive best-effort webhook deliveries.
	dispatcher := alerts.New(st, encKey)

	// Background loops (worker, monitor, backup scheduler) share one cancel.
	workerCtx, stopWorker := context.WithCancel(context.Background())
	defer stopWorker()

	// Database backups: destinations from env (DEPLOYMATE_BACKUP_DEST_*),
	// each service opting in per docs/specs/database-backups.md. With no
	// destinations configured nothing can enable, so no manager/scheduler is
	// built and the service pages show no backup panel at all.
	var backupMgr *backup.Manager
	if len(cfg.BackupDestinations) > 0 {
		backupDests, err := backup.NewDestinations(cfg.BackupDestinations)
		if err != nil {
			return err
		}
		backupMgr = backup.NewManager(st, rt, encKey, dispatcher, backupDests)
		sched := backup.NewScheduler(st, backupMgr)
		go sched.Run(workerCtx)
	}
	server := httpserver.New(st, rt, prov, events, encKey, cfg.LEMode, cfg.PreviewHost, cfg.DataDir, dnsManager, backupMgr)
	server.SetGitHubAPI(cfg.GitHubAPIURL)
	server.SetVersion(version)
	if os.Getenv("DEPLOYMATE_DEMO_HOST") == "1" {
		server.SetDemoHost(demo.Host{}) // the website's demo instance has no real machine to show
	}

	// The deployment worker: one in-process loop, builds serialized.
	worker := jobs.NewWorker(st, rt, prov, events, encKey, cfg.DataDir, httpserver.NetworkName, cfg.LEMode, cfg.RailpackPath, server.AppEnv, dispatcher)
	worker.SetGitHubAPI(cfg.GitHubAPIURL)
	go worker.Run(workerCtx)

	// Metrics, health, uptime, restart, and disk sampling.
	mon := monitor.New(st, rt, dispatcher)
	mon.SetHealer(server.HealApp)
	// DEPLOYMATE_DISABLE_MONITOR=1 keeps the monitor from probing and "healing"
	// containers: the seeded demo fleet has none, and must keep its seeded state.
	if os.Getenv("DEPLOYMATE_DISABLE_MONITOR") != "1" {
		go mon.Run(workerCtx)
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("deploymate listening", "addr", cfg.Addr, "data", cfg.DataDir)
	return srv.ListenAndServe()
}

// ensureOwner creates the first user when the database is empty.
func ensureOwner(st *store.Store, email, password string) error {
	n, err := st.CountUsers()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if _, err := st.CreateUser(store.User{Email: email, PasswordHash: hash, Role: "owner"}); err != nil {
		return err
	}
	slog.Info("created owner user", "email", email)
	return nil
}

func pruneSessions(st *store.Store) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for range t.C {
		if n, err := st.DeleteExpiredSessions(); err != nil {
			slog.Error("prune sessions", "err", err)
		} else if n > 0 {
			slog.Info("pruned expired sessions", "count", n)
		}
	}
}

// setupAdmin creates the owner user from env vars or an interactive prompt.
func setupAdmin() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()

	email := os.Getenv("DEPLOYMATE_SETUP_EMAIL")
	password := os.Getenv("DEPLOYMATE_SETUP_PASSWORD")
	if email == "" || password == "" {
		reader := bufio.NewReader(os.Stdin)
		if email == "" {
			fmt.Print("Email: ")
			email, _ = reader.ReadString('\n')
		}
		if password == "" {
			fmt.Print("Password: ")
			password, _ = reader.ReadString('\n')
		}
	}
	email = strings.TrimSpace(email)
	password = strings.TrimSpace(password)
	if email == "" || password == "" {
		return errors.New("email and password are required")
	}
	if err := ensureOwner(st, email, password); err != nil {
		return err
	}
	slog.Info("admin user ready — start deploymate with: deploymate serve")
	return nil
}
