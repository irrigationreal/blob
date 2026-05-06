// Package server: Valkey managed-service driver.
//
// Mirrors the Postgres pattern: each instance is a Nomad service job running
// valkey/valkey:<ver>-alpine on a static host port (16379+) with a Docker
// named volume for /data. AUTH password is generated on create and stored in
// /srv/blob/valkey/<name>.json (mode 0600). Apps bind via `services:` and the
// resolver injects REDIS_URL plus the standard ioredis/node-redis env vars.
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

	"github.com/irrigationreal/blob/internal/api"
)

const (
	valkeyPortFloor = 16379
	valkeyPortCeil  = 16479
)

type valkeyMeta struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Password  string    `json:"password"`
	Port      int       `json:"port"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) valkeyMetaDir() string {
	return filepath.Join(s.cfg.StateDir, "valkey")
}

func (s *Server) loadValkey(name string) (*valkeyMeta, error) {
	b, err := os.ReadFile(filepath.Join(s.valkeyMetaDir(), name+".json"))
	if err != nil {
		return nil, err
	}
	m := &valkeyMeta{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Server) saveValkey(m *valkeyMeta) error {
	if err := os.MkdirAll(s.valkeyMetaDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.valkeyMetaDir(), m.Name+".json"), b, 0o600)
}

func (s *Server) allocateValkeyPort() (int, error) {
	used := map[int]bool{}
	if entries, err := os.ReadDir(s.valkeyMetaDir()); err == nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			m, err := s.loadValkey(strings.TrimSuffix(e.Name(), ".json"))
			if err == nil {
				used[m.Port] = true
			}
		}
	}
	for p := valkeyPortFloor; p < valkeyPortCeil; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, errors.New("valkey port pool exhausted")
}

// --- HTTP handlers -----------------------------------------------------------

func (s *Server) handleValkey(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listValkey(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.CreateValkeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.createValkey(r.Context(), &req)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleValkeyItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/valkey/")
	parts := strings.SplitN(rest, "/", 2)
	name := parts[0]
	if !validName(name) {
		writeErr(w, 400, "invalid name")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == "GET":
		m, err := s.loadValkey(name)
		if err != nil {
			writeErr(w, 404, "no such valkey")
			return
		}
		writeJSON(w, 200, s.valkeyPublic(r.Context(), m))
	case len(parts) == 1 && r.Method == "DELETE":
		if err := s.destroyValkey(r.Context(), name); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"name": name, "destroyed": true})
	case len(parts) == 2 && parts[1] == "url" && r.Method == "GET":
		m, err := s.loadValkey(name)
		if err != nil {
			writeErr(w, 404, "no such valkey")
			return
		}
		writeJSON(w, 200, api.ValkeyURL{URL: s.valkeyURL(m, false)})
	default:
		writeErr(w, 404, "not found")
	}
}

// --- create / destroy / list --------------------------------------------------

func (s *Server) createValkey(ctx context.Context, req *api.CreateValkeyRequest) (*api.Valkey, error) {
	if !validName(req.Name) {
		return nil, errors.New("invalid name")
	}
	if _, err := s.loadValkey(req.Name); err == nil {
		return nil, fmt.Errorf("valkey %q already exists", req.Name)
	}
	if req.Version == "" {
		req.Version = "8"
	}
	if req.CPU <= 0 {
		req.CPU = 200
	}
	if req.Memory <= 0 {
		req.Memory = 256
	}
	port, err := s.allocateValkeyPort()
	if err != nil {
		return nil, err
	}
	m := &valkeyMeta{
		Name:      req.Name,
		Version:   req.Version,
		Password:  randomPassword(),
		Port:      port,
		CreatedAt: time.Now(),
	}
	if err := s.saveValkey(m); err != nil {
		return nil, err
	}
	id := "valkey-" + m.Name
	hcl := renderValkeyJob(m, s.cfg.Datacenter, id, req.CPU, req.Memory)
	if err := os.MkdirAll(s.cfg.JobsDir, 0o755); err != nil {
		return nil, err
	}
	jobPath := filepath.Join(s.cfg.JobsDir, id+".nomad")
	if err := os.WriteFile(jobPath, []byte(hcl), 0o644); err != nil {
		return nil, err
	}
	if err := s.run(ctx, "nomad", "job", "run", jobPath); err != nil {
		_ = os.Remove(filepath.Join(s.valkeyMetaDir(), m.Name+".json"))
		return nil, fmt.Errorf("schedule valkey: %w", err)
	}
	if err := s.waitJobRunning(ctx, id, 60*time.Second); err != nil {
		return nil, fmt.Errorf("valkey %q did not become ready: %w", m.Name, err)
	}
	return s.valkeyPublic(ctx, m), nil
}

func (s *Server) destroyValkey(ctx context.Context, name string) error {
	m, err := s.loadValkey(name)
	if err != nil {
		return errors.New("no such valkey")
	}
	id := "valkey-" + m.Name
	if err := s.run(ctx, "nomad", "job", "stop", "-purge", id); err != nil {
		stdLog("valkey destroy %s: nomad stop returned %v (continuing)", name, err)
	}
	_ = os.Remove(filepath.Join(s.cfg.JobsDir, id+".nomad"))
	_ = os.Remove(filepath.Join(s.valkeyMetaDir(), m.Name+".json"))
	// Docker volume preserved (matches postgres semantics: destroy is reversible).
	return nil
}

func (s *Server) listValkey(ctx context.Context) (*api.ListValkeyResponse, error) {
	out := &api.ListValkeyResponse{}
	entries, err := os.ReadDir(s.valkeyMetaDir())
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
		m, err := s.loadValkey(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out.Valkey = append(out.Valkey, *s.valkeyPublic(ctx, m))
	}
	sort.Slice(out.Valkey, func(i, j int) bool { return out.Valkey[i].Name < out.Valkey[j].Name })
	return out, nil
}

func (s *Server) valkeyPublic(ctx context.Context, m *valkeyMeta) *api.Valkey {
	host := s.postgresHost() // same host-resolution policy as Postgres
	status := "unknown"
	if body, err := s.nomadGET(ctx, "/v1/job/valkey-"+m.Name); err == nil {
		var j struct{ Status string }
		_ = json.Unmarshal(body, &j)
		status = j.Status
	}
	return &api.Valkey{
		Name:      m.Name,
		Version:   m.Version,
		Host:      host,
		Port:      m.Port,
		JobID:     "valkey-" + m.Name,
		URLMasked: s.valkeyURL(m, true),
		Status:    status,
		CreatedAt: m.CreatedAt,
	}
}

func (s *Server) valkeyURL(m *valkeyMeta, mask bool) string {
	pw := m.Password
	if mask {
		pw = "***"
	}
	return fmt.Sprintf("redis://:%s@%s:%d/0", pw, s.postgresHost(), m.Port)
}

// resolveValkeyServices is folded into resolveServices in postgres.go via
// extendResolveServices() — both are tried for each entry in req.Services.
func (s *Server) lookupValkeyForBinding(name string, env map[string]string, primary *bool) bool {
	m, err := s.loadValkey(name)
	if err != nil {
		return false
	}
	host := s.postgresHost()
	url := fmt.Sprintf("redis://:%s@%s:%d/0", m.Password, host, m.Port)
	if *primary {
		env["REDIS_URL"] = url
		env["REDIS_HOST"] = host
		env["REDIS_PORT"] = fmt.Sprintf("%d", m.Port)
		env["REDIS_PASSWORD"] = m.Password
		*primary = false
	}
	prefix := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	env[prefix+"_URL"] = url
	env[prefix+"_HOST"] = host
	env[prefix+"_PORT"] = fmt.Sprintf("%d", m.Port)
	env[prefix+"_PASSWORD"] = m.Password
	return true
}
