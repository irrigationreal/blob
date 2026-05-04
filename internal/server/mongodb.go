// Package server: MongoDB managed-service driver (v0.19).
//
// Mirrors the MySQL/Postgres/Valkey pattern: each instance is a Nomad
// service job running mongo:<ver> on a static host port (15700+) with
// a Docker named volume for /data/db. Root credentials and an app-level
// (user, database) tuple are generated on create and stored in
// /srv/blob/mongodb/<name>.json mode 0600. Apps bind via `services:`
// and the resolver injects MONGODB_URL plus standard mongo env.
//
// Single-node only. No replica sets, no sharding — that's a v0.20+
// conversation.
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

	"github.com/darvell/blob/internal/api"
)

const (
	mongoPortFloor = 15700
	mongoPortCeil  = 15800
)

type mongoMeta struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	RootUser  string    `json:"root_user"`
	RootPass  string    `json:"root_password"`
	User      string    `json:"user"`
	Password  string    `json:"password"`
	Database  string    `json:"database"`
	Port      int       `json:"port"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) mongoMetaDir() string {
	return filepath.Join(s.cfg.StateDir, "mongodb")
}

func (s *Server) loadMongo(name string) (*mongoMeta, error) {
	b, err := os.ReadFile(filepath.Join(s.mongoMetaDir(), name+".json"))
	if err != nil {
		return nil, err
	}
	m := &mongoMeta{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Server) saveMongo(m *mongoMeta) error {
	if err := os.MkdirAll(s.mongoMetaDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.mongoMetaDir(), m.Name+".json"), b, 0o600)
}

func (s *Server) allocateMongoPort() (int, error) {
	used := map[int]bool{}
	if entries, err := os.ReadDir(s.mongoMetaDir()); err == nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			m, err := s.loadMongo(strings.TrimSuffix(e.Name(), ".json"))
			if err == nil {
				used[m.Port] = true
			}
		}
	}
	for p := mongoPortFloor; p < mongoPortCeil; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, errors.New("mongodb port pool exhausted")
}

// --- HTTP handlers -----------------------------------------------------------

func (s *Server) handleMongo(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listMongo(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.CreateMongoRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.createMongo(r.Context(), &req)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleMongoItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/mongodb/")
	parts := strings.SplitN(rest, "/", 2)
	name := parts[0]
	if !validName(name) {
		writeErr(w, 400, "invalid name")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == "GET":
		m, err := s.loadMongo(name)
		if err != nil {
			writeErr(w, 404, "no such mongodb")
			return
		}
		writeJSON(w, 200, s.mongoPublic(r.Context(), m))
	case len(parts) == 1 && r.Method == "DELETE":
		if err := s.destroyMongo(r.Context(), name); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"name": name, "destroyed": true})
	case len(parts) == 2 && parts[1] == "url" && r.Method == "GET":
		m, err := s.loadMongo(name)
		if err != nil {
			writeErr(w, 404, "no such mongodb")
			return
		}
		writeJSON(w, 200, api.MongoURL{URL: s.mongoURL(m, false)})
	default:
		writeErr(w, 404, "not found")
	}
}

// --- create / destroy / list --------------------------------------------------

func (s *Server) createMongo(ctx context.Context, req *api.CreateMongoRequest) (*api.Mongo, error) {
	if !validName(req.Name) {
		return nil, errors.New("invalid name")
	}
	if _, err := s.loadMongo(req.Name); err == nil {
		return nil, fmt.Errorf("mongodb %q already exists", req.Name)
	}
	if req.Version == "" {
		req.Version = "7"
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
	port, err := s.allocateMongoPort()
	if err != nil {
		return nil, err
	}
	m := &mongoMeta{
		Name:      req.Name,
		Version:   req.Version,
		RootUser:  "root",
		RootPass:  randomPassword(),
		User:      "blob",
		Password:  randomPassword(),
		Database:  req.Database,
		Port:      port,
		CreatedAt: time.Now(),
	}
	if err := s.saveMongo(m); err != nil {
		return nil, err
	}
	id := "mongodb-" + m.Name
	hcl := renderMongoJob(m, s.cfg.Datacenter, id, req.CPU, req.Memory)
	if err := os.MkdirAll(s.cfg.JobsDir, 0o755); err != nil {
		return nil, err
	}
	jobPath := filepath.Join(s.cfg.JobsDir, id+".nomad")
	if err := os.WriteFile(jobPath, []byte(hcl), 0o644); err != nil {
		return nil, err
	}
	if err := s.run(ctx, "nomad", "job", "run", jobPath); err != nil {
		_ = os.Remove(filepath.Join(s.mongoMetaDir(), m.Name+".json"))
		return nil, fmt.Errorf("schedule mongodb: %w", err)
	}
	if err := s.waitJobRunning(ctx, id, 120*time.Second); err != nil {
		return nil, fmt.Errorf("mongodb %q did not become ready: %w", m.Name, err)
	}
	// App user is provisioned by the init.js bind-mounted into
	// /docker-entrypoint-initdb.d/ — the mongo entrypoint runs it
	// before flipping the readiness gate, so by the time we get here
	// it already exists. On data-dir reuse (re-create with a different
	// password, etc.) initdb is skipped; in that case ensureMongoUser
	// patches the live instance.
	if isFreshDataDir, _ := s.mongoIsFreshVolume(m); !isFreshDataDir {
		if err := s.ensureMongoUser(ctx, m); err != nil {
			stdLog("mongodb %s: ensure app user %q in db %q returned %v (instance is up; create manually with mongosh if your client errors with auth)", m.Name, m.User, m.Database, err)
		}
	}
	return s.mongoPublic(ctx, m), nil
}

// mongoIsFreshVolume returns true when the named docker volume has no
// pre-existing data — used to decide whether to skip the post-start
// user-provisioning hop on the happy path. Best-effort; on errors we
// fall through and run the hop (the cost is one image pull on a stale
// instance, which is rare).
func (s *Server) mongoIsFreshVolume(m *mongoMeta) (bool, error) {
	cmd := exec.Command("docker", "volume", "inspect", "--format", "{{.CreatedAt}}",
		"blob-mongodb-"+m.Name)
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	created, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(out)))
	if err != nil {
		// Older docker emits a different format; fall back to "fresh".
		return true, nil
	}
	// Created within the last 5 minutes → treat as fresh.
	return time.Since(created) < 5*time.Minute, nil
}

// ensureMongoUser runs `mongosh` in a one-off container and creates the
// app-level user with readWrite on the target database. Idempotent —
// catches the "User already exists" error and returns nil.
func (s *Server) ensureMongoUser(ctx context.Context, m *mongoMeta) error {
	host := s.postgresHost()
	rootURI := fmt.Sprintf("mongodb://%s:%s@%s:%d/admin?authSource=admin",
		m.RootUser, m.RootPass, host, m.Port)
	script := fmt.Sprintf(`
try {
  db.getSiblingDB(%q).createUser({ user: %q, pwd: %q, roles: [ { role: "readWrite", db: %q } ] });
  print("created");
} catch (e) {
  if (String(e).indexOf("already exists") >= 0 || (e.codeName && e.codeName === "DuplicateKey")) {
    print("exists");
  } else {
    throw e;
  }
}
`, m.Database, m.User, m.Password, m.Database)
	tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(tctx, "docker", "run", "--rm", "--network", "host",
		"mongo:"+m.Version, "mongosh", rootURI, "--quiet", "--eval", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mongosh: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *Server) destroyMongo(ctx context.Context, name string) error {
	m, err := s.loadMongo(name)
	if err != nil {
		return errors.New("no such mongodb")
	}
	id := "mongodb-" + m.Name
	if err := s.run(ctx, "nomad", "job", "stop", "-purge", id); err != nil {
		stdLog("mongodb destroy %s: nomad stop returned %v (continuing)", name, err)
	}
	_ = os.Remove(filepath.Join(s.cfg.JobsDir, id+".nomad"))
	_ = os.Remove(filepath.Join(s.mongoMetaDir(), m.Name+".json"))
	// Docker volume preserved (matches postgres/mysql semantics).
	return nil
}

func (s *Server) listMongo(ctx context.Context) (*api.ListMongoResponse, error) {
	out := &api.ListMongoResponse{}
	entries, err := os.ReadDir(s.mongoMetaDir())
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
		m, err := s.loadMongo(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out.Mongo = append(out.Mongo, *s.mongoPublic(ctx, m))
	}
	sort.Slice(out.Mongo, func(i, j int) bool { return out.Mongo[i].Name < out.Mongo[j].Name })
	return out, nil
}

func (s *Server) mongoPublic(ctx context.Context, m *mongoMeta) *api.Mongo {
	host := s.postgresHost()
	status := "unknown"
	if body, err := s.nomadGET(ctx, "/v1/job/mongodb-"+m.Name); err == nil {
		var j struct{ Status string }
		_ = json.Unmarshal(body, &j)
		status = j.Status
	}
	return &api.Mongo{
		Name:      m.Name,
		Version:   m.Version,
		Host:      host,
		Port:      m.Port,
		Database:  m.Database,
		User:      m.User,
		JobID:     "mongodb-" + m.Name,
		URLMasked: s.mongoURL(m, true),
		Status:    status,
		CreatedAt: m.CreatedAt,
	}
}

func (s *Server) mongoURL(m *mongoMeta, mask bool) string {
	pw := m.Password
	if mask {
		pw = "***"
	}
	return fmt.Sprintf("mongodb://%s:%s@%s:%d/%s?authSource=%s",
		m.User, pw, s.postgresHost(), m.Port, m.Database, m.Database)
}

func (s *Server) lookupMongoForBinding(name string, env map[string]string, primary *bool) bool {
	m, err := s.loadMongo(name)
	if err != nil {
		return false
	}
	host := s.postgresHost()
	url := fmt.Sprintf("mongodb://%s:%s@%s:%d/%s?authSource=%s",
		m.User, m.Password, host, m.Port, m.Database, m.Database)
	if *primary {
		env["MONGODB_URL"] = url
		env["MONGODB_HOST"] = host
		env["MONGODB_PORT"] = fmt.Sprintf("%d", m.Port)
		env["MONGODB_USER"] = m.User
		env["MONGODB_PASSWORD"] = m.Password
		env["MONGODB_DATABASE"] = m.Database
		// MONGO_URL is the convention used by some older drivers
		// (mongoose, meteor); ship both names so apps don't need to remap.
		env["MONGO_URL"] = url
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
