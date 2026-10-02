package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// Provisioner gives services their runtime resources: credentials, image,
// container on the shared network, and a readiness wait. It is shared by
// the dashboard handlers and the deployment worker so provisioning is one
// code path whether a human clicks Start or a deploymate.yml declares
// postgres.
type Provisioner struct {
	st      *store.Store
	rt      runtime.Runtime
	encKey  [32]byte
	network string
}

// NewProvisioner builds a Provisioner.
func NewProvisioner(st *store.Store, rt runtime.Runtime, encKey [32]byte, network string) *Provisioner {
	return &Provisioner{st: st, rt: rt, encKey: encKey, network: network}
}

// ContainerName is the docker container name for a service slug.
func ContainerName(slug string) string { return "dm-svc-" + slug }

// ContainerLabels marks service containers as DeployMate-owned.
func ContainerLabels(slug, svcType string) map[string]string {
	return map[string]string{
		"deploymate.managed": "true",
		"deploymate.service": slug,
		"deploymate.type":    svcType,
	}
}

// Resolution reports what Ensure did about one declared service type.
type Resolution struct {
	Type    string
	Service store.Service
	Action  string // ActionReused | ActionStarted | ActionProvisioned
}

// Ensure actions.
const (
	ActionReused      = "reused"
	ActionStarted     = "started"
	ActionProvisioned = "provisioned"
	// ActionOrphaned: a manifest-created service in the app's environment
	// is no longer declared. It is flagged and surfaced — never deleted.
	ActionOrphaned = "orphaned"
)

// Ensure resolves declared service declarations against the project's
// existing services in the given environment: a running service of the
// type in that environment is reused, a stopped one is started, and a
// missing one is created and provisioned from scratch. Services in other
// environments are never touched or deleted — teardown is a human
// decision.
func (p *Provisioner) Ensure(ctx context.Context, projectID, env string, decls []ServiceDecl) ([]Resolution, error) {
	res := make([]Resolution, 0, len(decls))
	declared := make(map[string]bool, len(decls))
	for _, decl := range decls {
		declared[decl.Type] = true
		svc, err := p.findByType(projectID, env, decl.Type)
		if errors.Is(err, store.ErrNotFound) {
			created, err := p.create(ctx, projectID, env, decl)
			if err != nil {
				return res, err
			}
			res = append(res, Resolution{Type: decl.Type, Service: created, Action: ActionProvisioned})
			continue
		}
		if err != nil {
			return res, err
		}
		if svc.Status == "running" {
			res = append(res, Resolution{Type: decl.Type, Service: svc, Action: ActionReused})
			continue
		}
		if err := p.Provision(ctx, svc); err != nil {
			return res, fmt.Errorf("starting %s service: %w", decl.Type, err)
		}
		res = append(res, Resolution{Type: decl.Type, Service: svc, Action: ActionStarted})
	}

	orphans, err := p.reconcileOrphans(projectID, env, declared)
	if err != nil {
		return res, err
	}
	res = append(res, orphans...)
	return res, nil
}

// reconcileOrphans walks the project's manifest-created services in the given
// environment: a declared type has its orphaned flag cleared (it was
// re-declared), an undeclared one is flagged and reported as an orphan
// candidate. Manual services are never touched — teardown is a human
// decision, so this only surfaces, never deletes.
func (p *Provisioner) reconcileOrphans(projectID, env string, declared map[string]bool) ([]Resolution, error) {
	svcs, err := p.st.ListServices(projectID)
	if err != nil {
		return nil, err
	}
	var out []Resolution
	for _, svc := range svcs {
		if svc.Environment != env || svc.Origin != store.OriginManifest {
			continue
		}
		switch {
		case declared[svc.Type]:
			if svc.Orphaned {
				if err := p.st.SetServiceOrphaned(svc.ID, false); err != nil {
					return out, err
				}
			}
		case !svc.Orphaned:
			if err := p.st.SetServiceOrphaned(svc.ID, true); err != nil {
				return out, err
			}
			svc.Orphaned = true
			out = append(out, Resolution{Type: svc.Type, Service: svc, Action: ActionOrphaned})
		}
	}
	return out, nil
}

// findByType returns a running service of the given type in the given
// environment, else any service of the type in that environment, else
// ErrNotFound. When an environment has several services of one type,
// declared types converge on the running one.
func (p *Provisioner) findByType(projectID, env, typ string) (store.Service, error) {
	svcs, err := p.st.ListServices(projectID)
	if err != nil {
		return store.Service{}, err
	}
	var fallback store.Service
	found := false
	for _, svc := range svcs {
		if svc.Type != typ || svc.Environment != env {
			continue
		}
		if svc.Status == "running" {
			return svc, nil
		}
		if !found {
			fallback, found = svc, true
		}
	}
	if found {
		return fallback, nil
	}
	return store.Service{}, store.ErrNotFound
}

// create provisions a brand-new service of the given declaration:
// metadata row, then the usual provision path. Production services keep
// the plain type name; every other environment gets an environment-
// prefixed name and slug ("Dev PostgreSQL"/"dev-postgres", "Staging
// PostgreSQL"/"staging-postgres") so they never collide across
// environments (slugs are globally unique).
func (p *Provisioner) create(ctx context.Context, projectID, env string, decl ServiceDecl) (store.Service, error) {
	tpl, ok := ForType(decl.Type)
	if !ok {
		return store.Service{}, fmt.Errorf("unknown service type %q", decl.Type)
	}
	name, slug := decl.Type, decl.Type
	if env != store.EnvProduction {
		name = strings.ToUpper(env[:1]) + env[1:] + " " + tpl.Label
		slug = env + "-" + decl.Type
	}
	mk := func(name, slug string) (store.Service, error) {
		return p.st.CreateService(store.Service{
			ProjectID: projectID, Type: decl.Type, Name: name, Slug: slug,
			Image: decl.Image(), Status: "stopped", VolumeName: VolumeName(slug), Port: tpl.Port,
			Environment: env, Origin: store.OriginManifest,
		})
	}
	svc, err := mk(name, slug)
	if errors.Is(err, store.ErrSlugTaken) {
		// Another project already owns "dev-postgres": container and volume
		// names are global, so this project's service must not share them
		// (that would stop and replace the other project's container).
		// Prefix the project slug instead.
		if proj, perr := p.st.GetProjectByID(projectID); perr == nil && proj.Slug != "" {
			svc, err = mk(name+" ("+proj.Name+")", proj.Slug+"-"+slug)
		}
	}
	if errors.Is(err, store.ErrSlugTaken) {
		return store.Service{}, fmt.Errorf(
			"creating %s service: the name %q is already taken by another service — rename or delete it first", decl.Type, slug)
	}
	if err != nil {
		return store.Service{}, err
	}
	if err := p.Provision(ctx, svc); err != nil {
		return store.Service{}, err
	}
	return svc, nil
}

// Provision gives a service its runtime resources: credentials on first
// run, its image, a container on the shared network, and a readiness wait
// so "running" means "accepting connections". Idempotent: provisioning an
// already-provisioned service re-creates its container and reuses its
// credentials. On error the service is marked failed.
func (p *Provisioner) Provision(ctx context.Context, svc store.Service) error {
	tpl, ok := ForType(svc.Type)
	if !ok {
		return fmt.Errorf("unknown service type %q", svc.Type)
	}
	name := ContainerName(svc.Slug)
	fail := func(err error) error {
		slog.Error("services: provision", "service", svc.Slug, "err", err)
		_ = p.st.UpdateServiceStatus(svc.ID, "failed")
		return err
	}

	// Credentials: keep existing, generate on first run.
	credsEnc, err := p.st.GetServiceCredentials(svc.ID)
	if err != nil {
		return fail(err)
	}
	if len(credsEnc) == 0 {
		plain := make(map[string]string, len(tpl.CredsGen))
		enc := make(map[string]string, len(tpl.CredsGen))
		for k, gen := range tpl.CredsGen {
			plain[k] = gen()
			enc[k], err = crypto.Encrypt(p.encKey, plain[k])
			if err != nil {
				return fail(err)
			}
		}
		if err := p.st.SetServiceCredentials(svc.ID, enc); err != nil {
			return fail(err)
		}
		credsEnc = enc
	}
	creds := decryptCreds(p.encKey, credsEnc)

	// The stored image wins over the template default: manifest pins
	// ("postgres:17") and the latest rule ("postgres" → postgres:latest)
	// are recorded on the service row at creation.
	image := svc.Image
	if image == "" {
		image = tpl.Image
	}
	has, err := p.rt.HasImage(ctx, image)
	if err != nil {
		return fail(err)
	}
	if !has {
		if err := p.rt.PullImage(ctx, image); err != nil {
			return fail(err)
		}
	}
	if err := p.rt.EnsureNetwork(ctx, p.network); err != nil {
		return fail(err)
	}
	_ = p.rt.Stop(ctx, name, 5)
	if err := p.rt.Remove(ctx, name); err != nil && !errors.Is(err, runtime.ErrContainerNotFound) {
		return fail(err)
	}
	spec := runtime.Spec{
		Name:    name,
		Image:   image,
		Env:     tpl.Env(creds),
		Labels:  ContainerLabels(svc.Slug, svc.Type),
		Network: p.network,
		Binds:   []string{svc.VolumeName + ":" + tpl.Mount},
	}
	if _, err := p.rt.Create(ctx, spec); err != nil {
		return fail(err)
	}
	if err := p.rt.Start(ctx, name); err != nil {
		return fail(err)
	}
	if err := p.waitReady(ctx, name, tpl, creds); err != nil {
		return fail(err)
	}
	return p.st.UpdateServiceStatus(svc.ID, "running")
}

// Restart stops and re-provisions a service in one click: stop, start,
// and the full readiness wait. Credentials are never regenerated.
func (p *Provisioner) Restart(ctx context.Context, svc store.Service) error {
	tpl, ok := ForType(svc.Type)
	if !ok {
		return fmt.Errorf("unknown service type %q", svc.Type)
	}
	name := ContainerName(svc.Slug)
	fail := func(err error) error {
		slog.Error("services: restart", "service", svc.Slug, "err", err)
		_ = p.st.UpdateServiceStatus(svc.ID, "failed")
		return err
	}

	credsEnc, err := p.st.GetServiceCredentials(svc.ID)
	if err != nil {
		return fail(err)
	}
	creds := decryptCreds(p.encKey, credsEnc)

	if err := p.rt.Stop(ctx, name, 10); err != nil && !errors.Is(err, runtime.ErrContainerNotFound) {
		return fail(err)
	}
	if err := p.rt.Start(ctx, name); err != nil {
		return fail(err)
	}
	if err := p.waitReady(ctx, name, tpl, creds); err != nil {
		return fail(err)
	}
	return p.st.UpdateServiceStatus(svc.ID, "running")
}

// Readiness poll knobs — vars so tests can shrink the 60s window.
var (
	readinessAttempts = 30
	readinessInterval = 2 * time.Second
)

// waitReady polls the service's readiness probe up to 60s.
func (p *Provisioner) waitReady(ctx context.Context, name string, tpl Template, creds map[string]string) error {
	for i := 0; i < readinessAttempts; i++ {
		if _, err := p.rt.Exec(ctx, name, tpl.ReadyCmd(creds)); err == nil {
			return nil
		}
		time.Sleep(readinessInterval)
	}
	return fmt.Errorf("service did not become ready within 60s — check its logs")
}

// decryptCreds decrypts an encrypted credential map, dropping entries that
// fail to decrypt.
func decryptCreds(encKey [32]byte, enc map[string]string) map[string]string {
	out := make(map[string]string, len(enc))
	for k, v := range enc {
		plain, err := crypto.Decrypt(encKey, v)
		if err != nil {
			slog.Error("services: decrypt cred", "key", k, "err", err)
			continue
		}
		out[k] = plain
	}
	return out
}
