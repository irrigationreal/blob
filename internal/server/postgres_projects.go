// Per-project users on managed Postgres.
//
// A "project" is an isolated (role, database, password, statement_timeout)
// tuple living on a shared Postgres instance. Apps bind via
// `services: [<instance>.<project>]` and get a DATABASE_URL scoped to that
// role + database. Two unrelated blob.yaml files can share one Postgres
// instance with no cross-tenant visibility.
//
// SQL we run as the instance superuser (the `blob` role) on create:
//   CREATE ROLE <p> LOGIN PASSWORD '<random>';
//   CREATE DATABASE <p> OWNER <p>;
//   REVOKE ALL ON DATABASE <p> FROM PUBLIC;
//   ALTER ROLE <p> SET statement_timeout = '<ms>';
//
// On destroy:
//   REASSIGN OWNED BY <p> TO blob;
//   DROP OWNED BY <p>;
//   DROP DATABASE <p>;
//   DROP ROLE <p>;
//
// On timeout change:
//   ALTER ROLE <p> SET statement_timeout = '<ms>';
//
// Every SQL action is run inside the running pg-<instance> container via
// `nomad alloc exec ... psql -U blob -d postgres`.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/irrigationreal/blob/internal/api"
)

// projectRE matches valid project identifiers. Stricter than blob name rules
// because Postgres reserves a few characters; we want anything that's safe to
// drop directly into CREATE ROLE / CREATE DATABASE without quoting.
var projectRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}[a-z0-9]$`)

func validProject(s string) bool { return projectRE.MatchString(s) }

const defaultStatementTimeoutMS = 30_000

type postgresProjectMeta struct {
	Instance           string    `json:"instance"`
	Project            string    `json:"project"`
	Role               string    `json:"role"`
	Database           string    `json:"database"`
	Password           string    `json:"password"`
	StatementTimeoutMS int       `json:"statement_timeout_ms"`
	CreatedAt          time.Time `json:"created_at"`
}

func (s *Server) projectsDir(instance string) string {
	return filepath.Join(s.cfg.StateDir, "postgres", instance, "projects")
}

func (s *Server) projectMetaPath(instance, project string) string {
	return filepath.Join(s.projectsDir(instance), project+".json")
}

func (s *Server) loadProject(instance, project string) (*postgresProjectMeta, error) {
	b, err := os.ReadFile(s.projectMetaPath(instance, project))
	if err != nil {
		return nil, err
	}
	m := &postgresProjectMeta{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Server) saveProject(m *postgresProjectMeta) error {
	dir := s.projectsDir(m.Instance)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.projectMetaPath(m.Instance, m.Project), b, 0o600)
}

func (s *Server) deleteProjectMeta(instance, project string) error {
	err := os.Remove(s.projectMetaPath(instance, project))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// runPsqlAsSuperuser executes a single SQL statement inside the running
// pg-<instance> container as the instance's `blob` superuser, connected to
// the maintenance "postgres" database. We use ON_ERROR_STOP=1 so any error
// surfaces as a non-zero exit code.
func (s *Server) runPsqlAsSuperuser(ctx context.Context, instance, sql string) error {
	allocID, err := s.runningPostgresAlloc(ctx, instance)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx,
		"nomad", "alloc", "exec",
		"-i=true", "-t=false",
		"-task", "pg",
		allocID,
		"psql", "-U", "blob", "-d", "postgres", "-v", "ON_ERROR_STOP=1",
	)
	cmd.Stdin = strings.NewReader(sql + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("psql: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *Server) projectURL(m *postgresProjectMeta, mask bool) string {
	pw := m.Password
	if mask {
		pw = "***"
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		m.Role, pw, s.postgresHost(), s.projectInstancePort(m.Instance), m.Database)
}

func (s *Server) projectInstancePort(instance string) int {
	if im, err := s.loadPostgres(instance); err == nil {
		return im.Port
	}
	return 0
}

func (s *Server) projectPublic(m *postgresProjectMeta) *api.PostgresProject {
	return &api.PostgresProject{
		Instance:           m.Instance,
		Project:            m.Project,
		Role:               m.Role,
		Database:           m.Database,
		URLMasked:          s.projectURL(m, true),
		StatementTimeoutMS: m.StatementTimeoutMS,
		CreatedAt:          m.CreatedAt,
	}
}

// --- create / destroy / list / timeout ---------------------------------------

func (s *Server) createPostgresProject(ctx context.Context, instance string, req *api.CreatePostgresProjectRequest) (*api.PostgresProject, error) {
	if _, err := s.loadPostgres(instance); err != nil {
		return nil, fmt.Errorf("instance %q not found", instance)
	}
	if !validProject(req.Project) {
		return nil, errors.New("invalid project name (must match [a-z][a-z0-9_]{0,30}[a-z0-9])")
	}
	if _, err := s.loadProject(instance, req.Project); err == nil {
		return nil, fmt.Errorf("project %q already exists on instance %q", req.Project, instance)
	}
	timeout := req.StatementTimeoutMS
	if timeout <= 0 {
		timeout = defaultStatementTimeoutMS
	}

	m := &postgresProjectMeta{
		Instance:           instance,
		Project:            req.Project,
		Role:               req.Project,
		Database:           req.Project,
		Password:           randomPassword(),
		StatementTimeoutMS: timeout,
		CreatedAt:          time.Now(),
	}

	// Note: we deliberately don't quote the role/database identifiers because
	// projectRE has already restricted them to lowercase + digits + underscores.
	// The password is single-quoted with E'...' escaping to be safe against
	// any hex chars (random_password is hex but this future-proofs).
	stmt := fmt.Sprintf(`
DO $$BEGIN
  CREATE ROLE %s LOGIN PASSWORD %s;
EXCEPTION WHEN duplicate_object THEN
  ALTER ROLE %s WITH LOGIN PASSWORD %s;
END$$;
`, m.Role, sqlLiteral(m.Password), m.Role, sqlLiteral(m.Password))
	if err := s.runPsqlAsSuperuser(ctx, instance, stmt); err != nil {
		return nil, fmt.Errorf("create role: %w", err)
	}
	// CREATE DATABASE cannot run inside a DO block. Issue it separately.
	createDB := fmt.Sprintf("CREATE DATABASE %s OWNER %s;", m.Database, m.Role)
	if err := s.runPsqlAsSuperuser(ctx, instance, createDB); err != nil {
		// Cleanup partial state.
		_ = s.runPsqlAsSuperuser(ctx, instance, fmt.Sprintf("DROP ROLE IF EXISTS %s;", m.Role))
		return nil, fmt.Errorf("create database: %w", err)
	}
	revoke := fmt.Sprintf("REVOKE ALL ON DATABASE %s FROM PUBLIC;", m.Database)
	if err := s.runPsqlAsSuperuser(ctx, instance, revoke); err != nil {
		return nil, fmt.Errorf("revoke public: %w", err)
	}
	if err := s.applyStatementTimeout(ctx, m); err != nil {
		return nil, fmt.Errorf("set statement_timeout: %w", err)
	}
	if err := s.saveProject(m); err != nil {
		return nil, err
	}
	return s.projectPublic(m), nil
}

func (s *Server) destroyPostgresProject(ctx context.Context, instance, project string) error {
	m, err := s.loadProject(instance, project)
	if err != nil {
		return errors.New("no such project")
	}
	// Reassign + drop owned must run AS the role being dropped's owner; do it
	// in stages. REASSIGN OWNED is safe to run by the superuser.
	stmt := fmt.Sprintf(`
REASSIGN OWNED BY %s TO blob;
DROP OWNED BY %s;
DROP DATABASE IF EXISTS %s;
DROP ROLE IF EXISTS %s;
`, m.Role, m.Role, m.Database, m.Role)
	if err := s.runPsqlAsSuperuser(ctx, instance, stmt); err != nil {
		return fmt.Errorf("drop project: %w", err)
	}
	return s.deleteProjectMeta(instance, project)
}

func (s *Server) listPostgresProjects(instance string) (*api.ListPostgresProjectsResponse, error) {
	if _, err := s.loadPostgres(instance); err != nil {
		return nil, fmt.Errorf("instance %q not found", instance)
	}
	out := &api.ListPostgresProjectsResponse{}
	entries, err := os.ReadDir(s.projectsDir(instance))
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
		m, err := s.loadProject(instance, strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out.Projects = append(out.Projects, *s.projectPublic(m))
	}
	sort.Slice(out.Projects, func(i, j int) bool { return out.Projects[i].Project < out.Projects[j].Project })
	return out, nil
}

func (s *Server) setPostgresProjectTimeout(ctx context.Context, instance, project string, ms int) (*api.PostgresProject, error) {
	m, err := s.loadProject(instance, project)
	if err != nil {
		return nil, errors.New("no such project")
	}
	if ms <= 0 {
		return nil, errors.New("statement_timeout_ms must be > 0 (use a large value to effectively disable)")
	}
	prev := m.StatementTimeoutMS
	m.StatementTimeoutMS = ms
	if err := s.applyStatementTimeout(ctx, m); err != nil {
		m.StatementTimeoutMS = prev
		return nil, err
	}
	if err := s.saveProject(m); err != nil {
		return nil, err
	}
	return s.projectPublic(m), nil
}

func (s *Server) applyStatementTimeout(ctx context.Context, m *postgresProjectMeta) error {
	// ALTER ROLE only affects new sessions. Apps using pg.Pool (or any pooler)
	// will hold open connections with the previous timeout. Terminate the
	// role's existing backends so they reconnect and pick up the new value.
	stmt := fmt.Sprintf(`
ALTER ROLE %s SET statement_timeout = '%dms';
SELECT pg_terminate_backend(pid)
  FROM pg_stat_activity
  WHERE usename = '%s' AND pid <> pg_backend_pid();
`, m.Role, m.StatementTimeoutMS, m.Role)
	return s.runPsqlAsSuperuser(ctx, m.Instance, stmt)
}

// --- HTTP handlers -----------------------------------------------------------

func (s *Server) handlePostgresProjects(w http.ResponseWriter, r *http.Request, instance string) {
	switch r.Method {
	case "GET":
		out, err := s.listPostgresProjects(instance)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.CreatePostgresProjectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.createPostgresProject(r.Context(), instance, &req)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handlePostgresProjectItem(w http.ResponseWriter, r *http.Request, instance, project, sub string) {
	if !validProject(project) {
		writeErr(w, 400, "invalid project name")
		return
	}
	switch {
	case sub == "" && r.Method == "GET":
		m, err := s.loadProject(instance, project)
		if err != nil {
			writeErr(w, 404, "no such project")
			return
		}
		writeJSON(w, 200, s.projectPublic(m))
	case sub == "" && r.Method == "DELETE":
		if err := s.destroyPostgresProject(r.Context(), instance, project); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"instance": instance, "project": project, "destroyed": true})
	case sub == "url" && r.Method == "GET":
		m, err := s.loadProject(instance, project)
		if err != nil {
			writeErr(w, 404, "no such project")
			return
		}
		writeJSON(w, 200, api.PostgresProjectURL{URL: s.projectURL(m, false)})
	case sub == "timeout" && r.Method == "POST":
		var req api.SetPostgresProjectTimeoutRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.setPostgresProjectTimeout(r.Context(), instance, project, req.StatementTimeoutMS)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 404, "not found")
	}
}

// sqlLiteral renders a Postgres single-quoted string literal, doubling embedded
// single quotes. Random hex passwords don't contain quotes but this stays
// correct if the password generator changes.
func sqlLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// parseProjectBinding extracts the (instance, project) tuple from a service
// binding. Returns ("", "") if the binding is a bare instance name (legacy).
func parseProjectBinding(svc string) (instance, project string) {
	if i := strings.IndexByte(svc, '.'); i > 0 {
		return svc[:i], svc[i+1:]
	}
	return "", ""
}
