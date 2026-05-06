// Package server: ScyllaDB managed-service driver (v0.20).
//
// Single-node scylladb/scylla container with a Docker named volume on
// /var/lib/scylla. Mandatory --developer-mode 1 + --memory 1G + --smp 1
// because Scylla's defaults grab every CPU and most of the box's RAM.
//
// On create, a keyspace and an app-level user with read/modify
// permissions are provisioned post-start by a one-shot cqlsh container.
// Apps bind via `services:` and the resolver injects
// SCYLLA_URL/HOSTS/PORT/USER/PASSWORD/KEYSPACE.
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
	"sort"
	"strings"
	"time"

	"github.com/irrigationreal/blob/internal/api"
)

const (
	scyllaPortFloor = 15800
	scyllaPortCeil  = 15900
)

type scyllaMeta struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	User      string    `json:"user"`
	Password  string    `json:"password"`
	Keyspace  string    `json:"keyspace"`
	Port      int       `json:"port"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) scyllaMetaDir() string {
	return filepath.Join(s.cfg.StateDir, "scylladb")
}

func (s *Server) loadScylla(name string) (*scyllaMeta, error) {
	b, err := os.ReadFile(filepath.Join(s.scyllaMetaDir(), name+".json"))
	if err != nil {
		return nil, err
	}
	m := &scyllaMeta{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Server) saveScylla(m *scyllaMeta) error {
	if err := os.MkdirAll(s.scyllaMetaDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.scyllaMetaDir(), m.Name+".json"), b, 0o600)
}

func (s *Server) allocateScyllaPort() (int, error) {
	used := map[int]bool{}
	if entries, err := os.ReadDir(s.scyllaMetaDir()); err == nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			m, err := s.loadScylla(strings.TrimSuffix(e.Name(), ".json"))
			if err == nil {
				used[m.Port] = true
			}
		}
	}
	for p := scyllaPortFloor; p < scyllaPortCeil; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, errors.New("scylladb port pool exhausted")
}

// --- HTTP handlers -----------------------------------------------------------

func (s *Server) handleScylla(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listScylla(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.CreateScyllaRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.createScylla(r.Context(), &req)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleScyllaItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/scylladb/")
	parts := strings.SplitN(rest, "/", 2)
	name := parts[0]
	if !validName(name) {
		writeErr(w, 400, "invalid name")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == "GET":
		m, err := s.loadScylla(name)
		if err != nil {
			writeErr(w, 404, "no such scylladb")
			return
		}
		writeJSON(w, 200, s.scyllaPublic(r.Context(), m))
	case len(parts) == 1 && r.Method == "DELETE":
		if err := s.destroyScylla(r.Context(), name); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"name": name, "destroyed": true})
	case len(parts) == 2 && parts[1] == "url" && r.Method == "GET":
		m, err := s.loadScylla(name)
		if err != nil {
			writeErr(w, 404, "no such scylladb")
			return
		}
		writeJSON(w, 200, api.ScyllaURL{URL: s.scyllaURL(m, false)})
	default:
		writeErr(w, 404, "not found")
	}
}

// --- create / destroy / list --------------------------------------------------

func (s *Server) createScylla(ctx context.Context, req *api.CreateScyllaRequest) (*api.Scylla, error) {
	if !validName(req.Name) {
		return nil, errors.New("invalid name")
	}
	if _, err := s.loadScylla(req.Name); err == nil {
		return nil, fmt.Errorf("scylladb %q already exists", req.Name)
	}
	if req.Version == "" {
		req.Version = "5.4"
	}
	if req.CPU <= 0 {
		req.CPU = 500
	}
	if req.Memory <= 0 {
		req.Memory = 2048
	}
	ks := req.Keyspace
	if ks == "" {
		ks = strings.ReplaceAll(req.Name, "-", "_")
	}
	port, err := s.allocateScyllaPort()
	if err != nil {
		return nil, err
	}
	m := &scyllaMeta{
		Name:      req.Name,
		Version:   req.Version,
		User:      "blob",
		Password:  randomPassword(),
		Keyspace:  ks,
		Port:      port,
		CreatedAt: time.Now(),
	}
	if err := s.saveScylla(m); err != nil {
		return nil, err
	}
	id := "scylladb-" + m.Name
	hcl := renderScyllaJob(m, s.cfg.Datacenter, id, req.CPU, req.Memory)
	if err := os.MkdirAll(s.cfg.JobsDir, 0o755); err != nil {
		return nil, err
	}
	jobPath := filepath.Join(s.cfg.JobsDir, id+".nomad")
	if err := os.WriteFile(jobPath, []byte(hcl), 0o644); err != nil {
		return nil, err
	}
	if err := s.run(ctx, "nomad", "job", "run", jobPath); err != nil {
		_ = os.Remove(filepath.Join(s.scyllaMetaDir(), m.Name+".json"))
		return nil, fmt.Errorf("schedule scylladb: %w", err)
	}
	if err := s.waitJobRunning(ctx, id, 240*time.Second); err != nil {
		return nil, fmt.Errorf("scylladb %q did not become ready: %w", m.Name, err)
	}
	if err := s.ensureScyllaUser(ctx, m); err != nil {
		stdLog("scylladb %s: ensure keyspace/user returned %v (instance is up; create them manually with cqlsh if your client errors)", m.Name, err)
	}
	return s.scyllaPublic(ctx, m), nil
}

// ensureScyllaUser runs cqlsh in a one-off container to create the
// keyspace and app-level superuser. Idempotent — uses IF NOT EXISTS.
//
// Scylla's CQL auth requires a short post-start grace period after the
// CQL port opens (system_auth keyspace bootstrap). Retry for up to 90s
// before giving up.
func (s *Server) ensureScyllaUser(ctx context.Context, m *scyllaMeta) error {
	host := s.postgresHost()
	cql := fmt.Sprintf(`
CREATE KEYSPACE IF NOT EXISTS %s WITH replication = {'class':'SimpleStrategy','replication_factor':1};
CREATE ROLE IF NOT EXISTS %s WITH PASSWORD = '%s' AND LOGIN = true AND SUPERUSER = false;
GRANT ALL ON KEYSPACE %s TO %s;
`, m.Keyspace, m.User, m.Password, m.Keyspace, m.User)
	deadline := time.Now().Add(90 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		tctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		cmd := exec.CommandContext(tctx, "docker", "run", "--rm", "--network", "host",
			"--entrypoint", "cqlsh",
			"scylladb/scylla:"+m.Version,
			host, fmt.Sprintf("%d", m.Port),
			"-u", "cassandra", "-p", "cassandra",
			"-e", cql)
		out, err := cmd.CombinedOutput()
		cancel()
		if err == nil {
			return nil
		}
		lastErr = fmt.Errorf("cqlsh: %v: %s", err, strings.TrimSpace(string(out)))
		time.Sleep(5 * time.Second)
	}
	return lastErr
}

func (s *Server) destroyScylla(ctx context.Context, name string) error {
	m, err := s.loadScylla(name)
	if err != nil {
		return errors.New("no such scylladb")
	}
	id := "scylladb-" + m.Name
	if err := s.run(ctx, "nomad", "job", "stop", "-purge", id); err != nil {
		stdLog("scylladb destroy %s: nomad stop returned %v (continuing)", name, err)
	}
	_ = os.Remove(filepath.Join(s.cfg.JobsDir, id+".nomad"))
	_ = os.Remove(filepath.Join(s.scyllaMetaDir(), m.Name+".json"))
	return nil
}

func (s *Server) listScylla(ctx context.Context) (*api.ListScyllaResponse, error) {
	out := &api.ListScyllaResponse{}
	entries, err := os.ReadDir(s.scyllaMetaDir())
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
		m, err := s.loadScylla(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out.Scylla = append(out.Scylla, *s.scyllaPublic(ctx, m))
	}
	sort.Slice(out.Scylla, func(i, j int) bool { return out.Scylla[i].Name < out.Scylla[j].Name })
	return out, nil
}

func (s *Server) scyllaPublic(ctx context.Context, m *scyllaMeta) *api.Scylla {
	host := s.postgresHost()
	status := "unknown"
	if body, err := s.nomadGET(ctx, "/v1/job/scylladb-"+m.Name); err == nil {
		var j struct{ Status string }
		_ = json.Unmarshal(body, &j)
		status = j.Status
	}
	return &api.Scylla{
		Name:      m.Name,
		Version:   m.Version,
		Host:      host,
		Port:      m.Port,
		Keyspace:  m.Keyspace,
		User:      m.User,
		JobID:     "scylladb-" + m.Name,
		URLMasked: s.scyllaURL(m, true),
		Status:    status,
		CreatedAt: m.CreatedAt,
	}
}

// scyllaURL returns a cassandra:// pseudo-URL. The cassandra ecosystem
// has no canonical URL scheme, but most app drivers accept hosts/port +
// keyspace + creds separately. We emit a URL form so apps can parse one
// env var if they prefer.
func (s *Server) scyllaURL(m *scyllaMeta, mask bool) string {
	pw := m.Password
	if mask {
		pw = "***"
	}
	return fmt.Sprintf("cassandra://%s:%s@%s:%d/%s",
		m.User, pw, s.postgresHost(), m.Port, m.Keyspace)
}

func (s *Server) lookupScyllaForBinding(name string, env map[string]string, primary *bool) bool {
	m, err := s.loadScylla(name)
	if err != nil {
		return false
	}
	host := s.postgresHost()
	url := fmt.Sprintf("cassandra://%s:%s@%s:%d/%s",
		m.User, m.Password, host, m.Port, m.Keyspace)
	if *primary {
		env["SCYLLA_URL"] = url
		env["SCYLLA_HOSTS"] = host
		env["SCYLLA_PORT"] = fmt.Sprintf("%d", m.Port)
		env["SCYLLA_USER"] = m.User
		env["SCYLLA_PASSWORD"] = m.Password
		env["SCYLLA_KEYSPACE"] = m.Keyspace
		// Cassandra-conventional aliases — most cassandra-driver libs
		// look up CASSANDRA_* env first.
		env["CASSANDRA_HOSTS"] = host
		env["CASSANDRA_PORT"] = fmt.Sprintf("%d", m.Port)
		env["CASSANDRA_USER"] = m.User
		env["CASSANDRA_PASSWORD"] = m.Password
		env["CASSANDRA_KEYSPACE"] = m.Keyspace
		*primary = false
	}
	prefix := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	env[prefix+"_URL"] = url
	env[prefix+"_HOSTS"] = host
	env[prefix+"_PORT"] = fmt.Sprintf("%d", m.Port)
	env[prefix+"_USER"] = m.User
	env[prefix+"_PASSWORD"] = m.Password
	env[prefix+"_KEYSPACE"] = m.Keyspace
	return true
}
