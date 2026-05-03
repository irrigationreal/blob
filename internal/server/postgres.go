// Package server: Postgres managed-service driver.
//
// Each Postgres instance is a small Nomad service job running
// postgres:<ver>-alpine bound to a static host port (15432+) and a Docker
// named volume (blob-pg-<name>) for /var/lib/postgresql/data. Credentials
// are generated on create and stored encrypted in /srv/blob/postgres/<name>.json.
//
// Workloads bind by adding a `services: [<pg-name>]` field to blob.yaml.
// At deploy time the resolver injects DATABASE_URL plus the standard libpq
// env vars (PGHOST/PGPORT/PGUSER/PGPASSWORD/PGDATABASE).
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/darvell/blob/internal/api"
)

const (
	pgPortFloor = 15432
	pgPortCeil  = 15532 // 100 instances per platform host. Bump if needed.
)

type postgresMeta struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Database  string    `json:"database"`
	User      string    `json:"user"`
	Password  string    `json:"password"`
	Port      int       `json:"port"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) postgresMetaDir() string {
	return filepath.Join(s.cfg.StateDir, "postgres")
}

func (s *Server) loadPostgres(name string) (*postgresMeta, error) {
	b, err := os.ReadFile(filepath.Join(s.postgresMetaDir(), name+".json"))
	if err != nil {
		return nil, err
	}
	m := &postgresMeta{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Server) savePostgres(m *postgresMeta) error {
	if err := os.MkdirAll(s.postgresMetaDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.postgresMetaDir(), m.Name+".json"), b, 0o600)
}

func (s *Server) allocatePostgresPort() (int, error) {
	used := map[int]bool{}
	if entries, err := os.ReadDir(s.postgresMetaDir()); err == nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			m, err := s.loadPostgres(strings.TrimSuffix(e.Name(), ".json"))
			if err == nil {
				used[m.Port] = true
			}
		}
	}
	for p := pgPortFloor; p < pgPortCeil; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, errors.New("postgres port pool exhausted")
}

func randomPassword() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b) // 36 chars, hex-safe in DSNs
}

// --- HTTP handlers -----------------------------------------------------------

func (s *Server) handlePostgres(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listPostgres(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.CreatePostgresRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.createPostgres(r.Context(), &req)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handlePostgresItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/postgres/")
	parts := strings.SplitN(rest, "/", 2)
	name := parts[0]
	if !validName(name) {
		writeErr(w, 400, "invalid name")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == "GET":
		m, err := s.loadPostgres(name)
		if err != nil {
			writeErr(w, 404, "no such postgres")
			return
		}
		writeJSON(w, 200, s.postgresPublic(r.Context(), m))
	case len(parts) == 1 && r.Method == "DELETE":
		if err := s.destroyPostgres(r.Context(), name); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"name": name, "destroyed": true})
	case len(parts) == 2 && parts[1] == "url" && r.Method == "GET":
		m, err := s.loadPostgres(name)
		if err != nil {
			writeErr(w, 404, "no such postgres")
			return
		}
		writeJSON(w, 200, api.PostgresURL{URL: s.postgresURL(m, false)})
	case len(parts) == 2 && parts[1] == "backup" && r.Method == "POST":
		s.handlePostgresBackup(w, r, name)
	case len(parts) == 2 && parts[1] == "backups" && r.Method == "GET":
		s.handlePostgresBackupsList(w, r, name)
	case len(parts) == 2 && parts[1] == "restore" && r.Method == "POST":
		s.handlePostgresRestore(w, r, name)
	case len(parts) == 2 && parts[1] == "backup-config":
		s.handlePostgresBackupConfig(w, r, name)
	case len(parts) == 2 && parts[1] == "backup-config/test":
		s.handlePostgresBackupConfigTest(w, r, name)
	case len(parts) == 2 && parts[1] == "projects":
		// /v1/postgres/<instance>/projects (GET list, POST create)
		s.handlePostgresProjects(w, r, name)
	case len(parts) == 2 && strings.HasPrefix(parts[1], "projects/"):
		// /v1/postgres/<instance>/projects/<project>[/<sub>]
		rest2 := strings.TrimPrefix(parts[1], "projects/")
		ps := strings.SplitN(rest2, "/", 2)
		project := ps[0]
		sub := ""
		if len(ps) == 2 {
			sub = ps[1]
		}
		s.handlePostgresProjectItem(w, r, name, project, sub)
	default:
		writeErr(w, 404, "not found")
	}
}

// --- create / destroy / list --------------------------------------------------

func (s *Server) createPostgres(ctx context.Context, req *api.CreatePostgresRequest) (*api.Postgres, error) {
	if !validName(req.Name) {
		return nil, errors.New("invalid name")
	}
	if _, err := s.loadPostgres(req.Name); err == nil {
		return nil, fmt.Errorf("postgres %q already exists", req.Name)
	}
	if req.Version == "" {
		req.Version = "16"
	}
	if req.Database == "" {
		req.Database = strings.ReplaceAll(req.Name, "-", "_")
	}
	if req.CPU <= 0 {
		req.CPU = 500
	}
	if req.Memory <= 0 {
		req.Memory = 512
	}
	port, err := s.allocatePostgresPort()
	if err != nil {
		return nil, err
	}
	m := &postgresMeta{
		Name:      req.Name,
		Version:   req.Version,
		Database:  req.Database,
		User:      "blob",
		Password:  randomPassword(),
		Port:      port,
		CreatedAt: time.Now(),
	}
	if err := s.savePostgres(m); err != nil {
		return nil, err
	}
	id := "pg-" + m.Name
	hcl := renderPostgresJob(m, s.cfg.Datacenter, id)
	if err := os.MkdirAll(s.cfg.JobsDir, 0o755); err != nil {
		return nil, err
	}
	jobPath := filepath.Join(s.cfg.JobsDir, id+".nomad")
	if err := os.WriteFile(jobPath, []byte(hcl), 0o644); err != nil {
		return nil, err
	}
	if err := s.run(ctx, "nomad", "job", "run", jobPath); err != nil {
		_ = os.Remove(filepath.Join(s.postgresMetaDir(), m.Name+".json"))
		return nil, fmt.Errorf("schedule postgres: %w", err)
	}
	if err := s.waitJobRunning(ctx, id, 90*time.Second); err != nil {
		return nil, fmt.Errorf("postgres %q did not become ready: %w", m.Name, err)
	}
	// Best-effort sanity: pg_isready
	_ = s.waitPostgresReady(ctx, m, 30*time.Second)
	return s.postgresPublic(ctx, m), nil
}

func (s *Server) destroyPostgres(ctx context.Context, name string) error {
	m, err := s.loadPostgres(name)
	if err != nil {
		return errors.New("no such postgres")
	}
	id := "pg-" + m.Name
	if err := s.run(ctx, "nomad", "job", "stop", "-purge", id); err != nil {
		// Continue cleanup even if the job is already gone.
		stdLog("postgres destroy %s: nomad stop returned %v (continuing)", name, err)
	}
	_ = os.Remove(filepath.Join(s.cfg.JobsDir, id+".nomad"))
	_ = os.Remove(filepath.Join(s.postgresMetaDir(), m.Name+".json"))
	// We deliberately do NOT delete the Docker volume by default — destroy is
	// reversible until volume cleanup is explicitly requested. blob volumes
	// list will show the orphan; operator can `docker volume rm` it.
	return nil
}

func (s *Server) listPostgres(ctx context.Context) (*api.ListPostgresResponse, error) {
	out := &api.ListPostgresResponse{}
	entries, err := os.ReadDir(s.postgresMetaDir())
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		m, err := s.loadPostgres(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out.Postgres = append(out.Postgres, *s.postgresPublic(ctx, m))
	}
	sort.Slice(out.Postgres, func(i, j int) bool { return out.Postgres[i].Name < out.Postgres[j].Name })
	return out, nil
}

func (s *Server) postgresPublic(ctx context.Context, m *postgresMeta) *api.Postgres {
	host := s.postgresHost()
	status := "unknown"
	if body, err := s.nomadGET(ctx, "/v1/job/pg-"+m.Name); err == nil {
		var j struct{ Status string }
		_ = json.Unmarshal(body, &j)
		status = j.Status
	}
	return &api.Postgres{
		Name:      m.Name,
		Version:   m.Version,
		Database:  m.Database,
		User:      m.User,
		Host:      host,
		Port:      m.Port,
		JobID:     "pg-" + m.Name,
		URLMasked: s.postgresURL(m, true),
		Status:    status,
		CreatedAt: m.CreatedAt,
	}
}

// postgresHost returns the address apps should use to reach Postgres. We use
// the platform's public IP if configured (works across nodes), otherwise the
// configured BaseDomain (assumes wildcard DNS), otherwise loopback.
func (s *Server) postgresHost() string {
	if s.cfg.PlatformPublicIP != "" {
		return s.cfg.PlatformPublicIP
	}
	if s.cfg.BaseDomain != "" {
		return s.cfg.BaseDomain
	}
	return "127.0.0.1"
}

func (s *Server) postgresURL(m *postgresMeta, mask bool) string {
	pw := m.Password
	if mask {
		pw = "***"
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", m.User, pw, s.postgresHost(), m.Port, m.Database)
}

// waitPostgresReady runs pg_isready inside the postgres allocation until it
// returns 0 or the deadline is hit. Best-effort.
func (s *Server) waitPostgresReady(ctx context.Context, m *postgresMeta, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		body, err := s.nomadGET(ctx, "/v1/job/pg-"+m.Name+"/allocations")
		if err == nil {
			var allocs []struct{ ID, ClientStatus string }
			_ = json.Unmarshal(body, &allocs)
			for _, a := range allocs {
				if a.ClientStatus != "running" {
					continue
				}
				out := s.output(ctx, "nomad", "alloc", "exec", "-i=false", "-t=false", "-task", "pg", a.ID, "pg_isready", "-U", m.User, "-d", m.Database)
				if strings.Contains(out, "accepting connections") {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return errors.New("pg_isready timeout")
}

// --- service binding (used by deploy path) -----------------------------------

// resolveServices looks up each managed-service binding and merges its env
// vars into req.Env. Tries Postgres first, then Valkey. The first binding
// wins the canonical "DATABASE_URL"/"REDIS_URL" slot per family.
//
// Binding syntax:
//   - "<instance>"             — legacy: superuser role, instance-named database
//   - "<instance>.<project>"   — per-project role, per-project database (v0.6+)
func (s *Server) resolveServices(req *api.DeployRequest) error {
	if len(req.Services) == 0 {
		return nil
	}
	if req.Env == nil {
		req.Env = map[string]string{}
	}
	pgPrimary := true
	redisPrimary := true
	lokiPrimary := true
	grafanaPrimary := true
	for _, svc := range req.Services {
		// Try project binding first ("instance.project").
		if instance, project := parseProjectBinding(svc); project != "" {
			pm, err := s.loadProject(instance, project)
			if err != nil {
				return fmt.Errorf("project binding %q not found", svc)
			}
			im, err := s.loadPostgres(instance)
			if err != nil {
				return fmt.Errorf("instance %q not found", instance)
			}
			host := s.postgresHost()
			dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
				pm.Role, pm.Password, host, im.Port, pm.Database)
			if pgPrimary {
				req.Env["DATABASE_URL"] = dsn
				req.Env["PGHOST"] = host
				req.Env["PGPORT"] = fmt.Sprintf("%d", im.Port)
				req.Env["PGUSER"] = pm.Role
				req.Env["PGPASSWORD"] = pm.Password
				req.Env["PGDATABASE"] = pm.Database
				pgPrimary = false
			}
			// Per-binding prefixed envs use the project name (the instance is
			// just the underlying pool — apps don't care which one).
			envPrefix := strings.ToUpper(strings.ReplaceAll(project, "-", "_"))
			req.Env[envPrefix+"_URL"] = dsn
			req.Env[envPrefix+"_HOST"] = host
			req.Env[envPrefix+"_PORT"] = fmt.Sprintf("%d", im.Port)
			req.Env[envPrefix+"_USER"] = pm.Role
			req.Env[envPrefix+"_PASSWORD"] = pm.Password
			req.Env[envPrefix+"_DATABASE"] = pm.Database
			continue
		}
		// Try Postgres instance (legacy bare-name binding)
		if m, err := s.loadPostgres(svc); err == nil {
			host := s.postgresHost()
			dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
				m.User, m.Password, host, m.Port, m.Database)
			if pgPrimary {
				req.Env["DATABASE_URL"] = dsn
				req.Env["PGHOST"] = host
				req.Env["PGPORT"] = fmt.Sprintf("%d", m.Port)
				req.Env["PGUSER"] = m.User
				req.Env["PGPASSWORD"] = m.Password
				req.Env["PGDATABASE"] = m.Database
				pgPrimary = false
			}
			envPrefix := strings.ToUpper(strings.ReplaceAll(svc, "-", "_"))
			req.Env[envPrefix+"_URL"] = dsn
			req.Env[envPrefix+"_HOST"] = host
			req.Env[envPrefix+"_PORT"] = fmt.Sprintf("%d", m.Port)
			req.Env[envPrefix+"_USER"] = m.User
			req.Env[envPrefix+"_PASSWORD"] = m.Password
			req.Env[envPrefix+"_DATABASE"] = m.Database
			continue
		}
		// Try Valkey
		if s.lookupValkeyForBinding(svc, req.Env, &redisPrimary) {
			continue
		}
		// Try Loki / Grafana (v0.8 observability). Each has its own primary
		// slot (LOKI_URL / GRAFANA_URL) so multiple bindings still produce
		// the canonical env on the first hit.
		if s.lookupLokiForBinding(svc, req.Env, &lokiPrimary) {
			continue
		}
		if s.lookupGrafanaForBinding(svc, req.Env, &grafanaPrimary) {
			continue
		}
		return fmt.Errorf("service %q not found", svc)
	}
	return nil
}
