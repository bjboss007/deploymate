package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/habibmuhammad/deploymate/internal/appspec"
	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/security"
	"github.com/habibmuhammad/deploymate/internal/services"
	"github.com/habibmuhammad/deploymate/web/templates"
)

// SetSecurityContext tells the audit how this server is set up: the dashboard's
// listen address and whether it runs behind a tunnel (DEPLOYMATE_LE_MODE=off).
func (s *Server) SetSecurityContext(listenAddr string, tunnel bool) {
	s.listenAddr, s.tunnelMode = listenAddr, tunnel
}

// SetNotifier lets the dashboard raise alerts (repeated failed sign-ins).
func (s *Server) SetNotifier(fn func(event, subject, details string)) { s.notify = fn }

// securityReport gathers what only DeployMate knows (containers, services) and runs
// the read-only audit.
func (s *Server) securityReport(ctx context.Context) security.Report {
	env := security.Env{DashboardAddr: s.listenAddr, DataDir: s.dataDir, TunnelMode: s.tunnelMode}
	if s.host != nil && !s.demoHost {
		env.RebootNeeded = s.host.Snapshot(ctx).RebootRequired
	}

	svcs, _ := s.store.ListAllServices()
	var names []string
	for _, sv := range svcs {
		info := security.ServiceInfo{Slug: sv.Slug, Type: sv.Type}
		if sv.Type == "redis" {
			if enc, err := s.store.GetServiceCredentials(sv.ID); err == nil && enc["password"] != "" {
				if pw, err := crypto.Decrypt(s.encKey, enc["password"]); err == nil && pw != "" {
					info.HasPassword = true
				}
			}
		}
		env.Services = append(env.Services, info)
		names = append(names, services.ContainerName(sv.Slug))
	}
	if apps, err := s.store.ListAllApps(); err == nil {
		for _, a := range apps {
			rows, _ := s.store.ListAppReplicas(a.ID)
			for _, sl := range appspec.Slots(a, rows) {
				names = append(names, sl.Name)
			}
		}
	}

	if s.rt == nil || s.demoHost {
		env.ContainersErr = "no container engine is connected"
	} else {
		ictx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		for _, n := range names {
			info, err := s.rt.Inspect(ictx, n)
			if err != nil {
				continue // not created yet, or removed: nothing to audit
			}
			env.Containers = append(env.Containers, security.ContainerInfo{
				Name: n, PublicBindings: info.PublicBindings, Privileged: info.Privileged,
				NoNewPrivileges: info.NoNewPrivileges, DockerSocket: info.DockerSocket,
			})
		}
	}
	return security.Audit(ctx, env)
}

func levelLabel(l security.Level) string {
	return [...]string{"OK", "Note", "Fix soon", "Fix now"}[l]
}

func secClass(l security.Level) string {
	return [...]string{"ok", "info", "warn", "crit"}[l]
}

func (s *Server) handleSecurityPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	rep := s.securityReport(ctx)
	v := templates.SecurityView{Headline: rep.Headline}
	switch {
	case rep.Level >= security.Crit:
		v.Level = "crit"
	case rep.Level >= security.Warn:
		v.Level = "warn"
	default:
		v.Level = "ok"
	}
	for _, c := range rep.Checks {
		v.Checks = append(v.Checks, templates.SecurityCheck{Title: c.Title, Level: secClass(c.Level), Label: levelLabel(c.Level), Detail: c.Detail, Fix: c.Fix})
	}
	v.SignIns = s.signInView()
	render(w, r, http.StatusOK, templates.SecurityPage(s.viewCtx(r), v))
}

type apiSecurityJSON struct {
	Status   string             `json:"status"` // ok | warning | critical
	Headline string             `json:"headline"`
	Checks   []apiSecurityCheck `json:"checks"`
}

type apiSecurityCheck struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Level  string `json:"level"` // ok | info | warning | critical
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// handleAPISecurity: GET /api/v1/security — read scope, the same audit as the page.
func (s *Server) handleAPISecurity(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	rep := s.securityReport(ctx)
	out := apiSecurityJSON{Status: map[bool]string{true: "ok", false: rep.Level.String()}[rep.Level < security.Warn], Headline: rep.Headline, Checks: []apiSecurityCheck{}}
	for _, c := range rep.Checks {
		out.Checks = append(out.Checks, apiSecurityCheck{ID: c.ID, Title: c.Title, Level: c.Level.String(), Detail: c.Detail, Fix: c.Fix})
	}
	slog.Debug("api: security", "status", out.Status)
	apiJSON(w, http.StatusOK, out)
}

// signInView gathers the sign-in activity block; a failed lookup just leaves a part empty.
func (s *Server) signInView() templates.SignInView {
	var v templates.SignInView
	now := time.Now().UTC()
	if c, err := s.store.LoginCounts(now.Add(-24 * time.Hour).Format(time.RFC3339Nano)); err == nil {
		v.OK, v.Refused, v.Blocked = c["ok"], c["refused"], c["blocked"]
	}
	if c, err := s.store.LoginCounts(now.AddDate(0, 0, -7).Format(time.RFC3339Nano)); err == nil {
		v.OK7, v.Refused7 = c["ok"], c["refused"]
	}
	v.ActiveSessions, _ = s.store.CountActiveSessions()
	if rows, err := s.store.ListLogins(20); err == nil {
		for _, r := range rows {
			v.Recent = append(v.Recent, templates.SignInRow{TS: r.TS, Email: r.Email, Address: r.Address, Result: r.Result, NewAddress: r.NewAddress})
		}
	}
	return v
}
