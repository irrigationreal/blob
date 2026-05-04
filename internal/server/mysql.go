// Package server: MySQL managed-service driver (v0.17).
//
// Mirrors the Valkey/Postgres pattern: each instance is a Nomad service
// job running mysql:<ver> on a static host port (15300+) with a Docker
// named volume for /var/lib/mysql. Root password and an app-level
// (user, database) tuple are generated on create and stored in
// /srv/blob/mysql/<name>.json mode 0600. Apps bind via `services:`
// and the resolver injects MYSQL_URL plus the standard mysql client env.
package server

import (
	"context"
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
	mysqlPortFloor = 15300
	mysqlPortCeil  = 15400
)

type mysqlMeta struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	RootPass  string    `json:"root_password"`
	User      string    `json:"user"`
	Password  string    `json:"password"`
	Database  string    `json:"database"`
	Port      int       `json:"port"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) mysqlMetaDir() string {
	return filepath.Join(s.cfg.StateDir, "mysql")
}

func (s *Server) loadMySQL(name string) (*mysqlMeta, error) {
	b, err := os.ReadFile(filepath.Join(s.mysqlMetaDir(), name+".json"))
	if err != nil {
		return nil, err
	}
	m := &mysqlMeta{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Server) saveMySQL(m *mysqlMeta) error {
	if err := os.MkdirAll(s.mysqlMetaDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.mysqlMetaDir(), m.Name+".json"), b, 0o600)
}

func (s *Server) allocateMySQLPort() (int, error) {
	used := map[int]bool{}
	if entries, err := os.ReadDir(s.mysqlMetaDir()); err == nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			m, err := s.loadMySQL(strings.TrimSuffix(e.Name(), ".json"))
			if err == nil {
				used[m.Port] = true
			}
		}
	}
	for p := mysqlPortFloor; p < mysqlPortCeil; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, errors.New("mysql port pool exhausted")
}

// --- HTTP handlers -----------------------------------------------------------

func (s *Server) handleMySQL(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listMySQL(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.CreateMySQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.createMySQL(r.Context(), &req)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleMySQLItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/mysql/")
	parts := strings.SplitN(rest, "/", 2)
	name := parts[0]
	if !validName(name) {
		writeErr(w, 400, "invalid name")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == "GET":
		m, err := s.loadMySQL(name)
		if err != nil {
			writeErr(w, 404, "no such mysql")
			return
		}
		writeJSON(w, 200, s.mysqlPublic(r.Context(), m))
	case len(parts) == 1 && r.Method == "DELETE":
		if err := s.destroyMySQL(r.Context(), name); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"name": name, "destroyed": true})
	case len(parts) == 2 && parts[1] == "url" && r.Method == "GET":
		m, err := s.loadMySQL(name)
		if err != nil {
			writeErr(w, 404, "no such mysql")
			return
		}
		writeJSON(w, 200, api.MySQLURL{URL: s.mysqlURL(m, false)})
	default:
		writeErr(w, 404, "not found")
	}
}

// --- create / destroy / list --------------------------------------------------

func (s *Server) createMySQL(ctx context.Context, req *api.CreateMySQLRequest) (*api.MySQL, error) {
	if !validName(req.Name) {
		return nil, errors.New("invalid name")
	}
	if _, err := s.loadMySQL(req.Name); err == nil {
		return nil, fmt.Errorf("mysql %q already exists", req.Name)
	}
	if req.Version == "" {
		req.Version = "8.4"
	}
	if req.CPU <= 0 {
		req.CPU = 500
	}
	if req.Memory <= 0 {
		req.Memory = 512
	}
	if req.Database == "" {
		req.Database = req.Name
	}
	port, err := s.allocateMySQLPort()
	if err != nil {
		return nil, err
	}
	m := &mysqlMeta{
		Name:      req.Name,
		Version:   req.Version,
		RootPass:  randomPassword(),
		User:      "blob",
		Password:  randomPassword(),
		Database:  req.Database,
		Port:      port,
		CreatedAt: time.Now(),
	}
	if err := s.saveMySQL(m); err != nil {
		return nil, err
	}
	id := "mysql-" + m.Name
	hcl := renderMySQLJob(m, s.cfg.Datacenter, id, req.CPU, req.Memory)
	if err := os.MkdirAll(s.cfg.JobsDir, 0o755); err != nil {
		return nil, err
	}
	jobPath := filepath.Join(s.cfg.JobsDir, id+".nomad")
	if err := os.WriteFile(jobPath, []byte(hcl), 0o644); err != nil {
		return nil, err
	}
	if err := s.run(ctx, "nomad", "job", "run", jobPath); err != nil {
		_ = os.Remove(filepath.Join(s.mysqlMetaDir(), m.Name+".json"))
		return nil, fmt.Errorf("schedule mysql: %w", err)
	}
	if err := s.waitJobRunning(ctx, id, 120*time.Second); err != nil {
		return nil, fmt.Errorf("mysql %q did not become ready: %w", m.Name, err)
	}
	return s.mysqlPublic(ctx, m), nil
}

func (s *Server) destroyMySQL(ctx context.Context, name string) error {
	m, err := s.loadMySQL(name)
	if err != nil {
		return errors.New("no such mysql")
	}
	id := "mysql-" + m.Name
	if err := s.run(ctx, "nomad", "job", "stop", "-purge", id); err != nil {
		stdLog("mysql destroy %s: nomad stop returned %v (continuing)", name, err)
	}
	_ = os.Remove(filepath.Join(s.cfg.JobsDir, id+".nomad"))
	_ = os.Remove(filepath.Join(s.mysqlMetaDir(), m.Name+".json"))
	// Docker volume preserved (matches postgres/valkey semantics).
	return nil
}

func (s *Server) listMySQL(ctx context.Context) (*api.ListMySQLResponse, error) {
	out := &api.ListMySQLResponse{}
	entries, err := os.ReadDir(s.mysqlMetaDir())
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
		m, err := s.loadMySQL(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out.MySQL = append(out.MySQL, *s.mysqlPublic(ctx, m))
	}
	sort.Slice(out.MySQL, func(i, j int) bool { return out.MySQL[i].Name < out.MySQL[j].Name })
	return out, nil
}

func (s *Server) mysqlPublic(ctx context.Context, m *mysqlMeta) *api.MySQL {
	host := s.postgresHost()
	status := "unknown"
	if body, err := s.nomadGET(ctx, "/v1/job/mysql-"+m.Name); err == nil {
		var j struct{ Status string }
		_ = json.Unmarshal(body, &j)
		status = j.Status
	}
	return &api.MySQL{
		Name:      m.Name,
		Version:   m.Version,
		Host:      host,
		Port:      m.Port,
		Database:  m.Database,
		User:      m.User,
		JobID:     "mysql-" + m.Name,
		URLMasked: s.mysqlURL(m, true),
		Status:    status,
		CreatedAt: m.CreatedAt,
	}
}

func (s *Server) mysqlURL(m *mysqlMeta, mask bool) string {
	pw := m.Password
	if mask {
		pw = "***"
	}
	return fmt.Sprintf("mysql://%s:%s@%s:%d/%s", m.User, pw, s.postgresHost(), m.Port, m.Database)
}

func (s *Server) lookupMySQLForBinding(name string, env map[string]string, primary *bool) bool {
	m, err := s.loadMySQL(name)
	if err != nil {
		return false
	}
	host := s.postgresHost()
	url := fmt.Sprintf("mysql://%s:%s@%s:%d/%s", m.User, m.Password, host, m.Port, m.Database)
	if *primary {
		env["MYSQL_URL"] = url
		env["MYSQL_HOST"] = host
		env["MYSQL_PORT"] = fmt.Sprintf("%d", m.Port)
		env["MYSQL_USER"] = m.User
		env["MYSQL_PASSWORD"] = m.Password
		env["MYSQL_DATABASE"] = m.Database
		// DATABASE_URL is conventionally claimed by postgres; we only
		// take the slot here when no postgres binding has claimed it.
		// (resolveServices keeps a separate primary flag per family.)
		*primary = false
	}
	prefix := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	env[prefix+"_URL"] = url
	env[prefix+"_HOST"] = host
	env[prefix+"_PORT"] = fmt.Sprintf("%d", m.Port)
	env[prefix+"_USER"] = m.User
	env[prefix+"_PASSWORD"] = m.Password
	env[prefix+"_DATABASE"] = m.Database
	return true
}
