// Package server: Loki managed-service driver (v0.8 observability).
//
// Mirrors the Valkey/Postgres pattern: each instance is a Nomad service job
// running grafana/loki:<ver> in single-binary mode, listening on a static host
// port (13100+) with a Docker named volume for /loki (chunks + index). No auth
// — bind to the private host network. Promtail (next cycle) and Grafana
// (cycle after) discover Loki via lookupLokiForBinding which injects LOKI_URL
// and LOKI_PUSH_URL into apps that bind via `services: [<name>]`.
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
	lokiPortFloor = 13100
	lokiPortCeil  = 13200
)

type lokiMeta struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Port      int       `json:"port"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) lokiMetaDir() string {
	return filepath.Join(s.cfg.StateDir, "loki")
}

func (s *Server) loadLoki(name string) (*lokiMeta, error) {
	b, err := os.ReadFile(filepath.Join(s.lokiMetaDir(), name+".json"))
	if err != nil {
		return nil, err
	}
	m := &lokiMeta{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Server) saveLoki(m *lokiMeta) error {
	if err := os.MkdirAll(s.lokiMetaDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.lokiMetaDir(), m.Name+".json"), b, 0o600)
}

func (s *Server) allocateLokiPort() (int, error) {
	used := map[int]bool{}
	if entries, err := os.ReadDir(s.lokiMetaDir()); err == nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			m, err := s.loadLoki(strings.TrimSuffix(e.Name(), ".json"))
			if err == nil {
				used[m.Port] = true
			}
		}
	}
	for p := lokiPortFloor; p < lokiPortCeil; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, errors.New("loki port pool exhausted")
}

// --- HTTP handlers -----------------------------------------------------------

func (s *Server) handleLoki(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listLoki(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.CreateLokiRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.createLoki(r.Context(), &req)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleLokiItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/loki/")
	parts := strings.SplitN(rest, "/", 2)
	name := parts[0]
	if !validName(name) {
		writeErr(w, 400, "invalid name")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == "GET":
		m, err := s.loadLoki(name)
		if err != nil {
			writeErr(w, 404, "no such loki")
			return
		}
		writeJSON(w, 200, s.lokiPublic(r.Context(), m))
	case len(parts) == 1 && r.Method == "DELETE":
		if err := s.destroyLoki(r.Context(), name); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"name": name, "destroyed": true})
	default:
		writeErr(w, 404, "not found")
	}
}

// --- create / destroy / list --------------------------------------------------

func (s *Server) createLoki(ctx context.Context, req *api.CreateLokiRequest) (*api.Loki, error) {
	if !validName(req.Name) {
		return nil, errors.New("invalid name")
	}
	if _, err := s.loadLoki(req.Name); err == nil {
		return nil, fmt.Errorf("loki %q already exists", req.Name)
	}
	if req.Version == "" {
		req.Version = "3.6"
	}
	if req.CPU <= 0 {
		req.CPU = 500
	}
	if req.Memory <= 0 {
		req.Memory = 512
	}
	port, err := s.allocateLokiPort()
	if err != nil {
		return nil, err
	}
	m := &lokiMeta{
		Name:      req.Name,
		Version:   req.Version,
		Port:      port,
		CreatedAt: time.Now(),
	}
	if err := s.saveLoki(m); err != nil {
		return nil, err
	}
	id := "loki-" + m.Name
	hcl := renderLokiJob(m, s.cfg.Datacenter, id, req.CPU, req.Memory)
	if err := os.MkdirAll(s.cfg.JobsDir, 0o755); err != nil {
		return nil, err
	}
	jobPath := filepath.Join(s.cfg.JobsDir, id+".nomad")
	if err := os.WriteFile(jobPath, []byte(hcl), 0o644); err != nil {
		return nil, err
	}
	if err := s.run(ctx, "nomad", "job", "run", jobPath); err != nil {
		_ = os.Remove(filepath.Join(s.lokiMetaDir(), m.Name+".json"))
		return nil, fmt.Errorf("schedule loki: %w", err)
	}
	if err := s.waitJobRunning(ctx, id, 90*time.Second); err != nil {
		return nil, fmt.Errorf("loki %q did not become ready: %w", m.Name, err)
	}
	return s.lokiPublic(ctx, m), nil
}

func (s *Server) destroyLoki(ctx context.Context, name string) error {
	m, err := s.loadLoki(name)
	if err != nil {
		return errors.New("no such loki")
	}
	id := "loki-" + m.Name
	if err := s.run(ctx, "nomad", "job", "stop", "-purge", id); err != nil {
		stdLog("loki destroy %s: nomad stop returned %v (continuing)", name, err)
	}
	_ = os.Remove(filepath.Join(s.cfg.JobsDir, id+".nomad"))
	_ = os.Remove(filepath.Join(s.lokiMetaDir(), m.Name+".json"))
	// Docker volume preserved (matches postgres/valkey semantics).
	return nil
}

func (s *Server) listLoki(ctx context.Context) (*api.ListLokiResponse, error) {
	out := &api.ListLokiResponse{}
	entries, err := os.ReadDir(s.lokiMetaDir())
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
		m, err := s.loadLoki(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out.Loki = append(out.Loki, *s.lokiPublic(ctx, m))
	}
	sort.Slice(out.Loki, func(i, j int) bool { return out.Loki[i].Name < out.Loki[j].Name })
	return out, nil
}

func (s *Server) lokiPublic(ctx context.Context, m *lokiMeta) *api.Loki {
	host := s.postgresHost()
	status := "unknown"
	if body, err := s.nomadGET(ctx, "/v1/job/loki-"+m.Name); err == nil {
		var j struct{ Status string }
		_ = json.Unmarshal(body, &j)
		status = j.Status
	}
	return &api.Loki{
		Name:      m.Name,
		Version:   m.Version,
		Host:      host,
		Port:      m.Port,
		JobID:     "loki-" + m.Name,
		URL:       fmt.Sprintf("http://%s:%d", host, m.Port),
		Status:    status,
		CreatedAt: m.CreatedAt,
	}
}

// lookupLokiForBinding resolves a Loki instance for a `services:` binding and
// injects LOKI_URL / LOKI_PUSH_URL into the workload's env. Promtail and
// Grafana use these to discover the data plane.
func (s *Server) lookupLokiForBinding(name string, env map[string]string, primary *bool) bool {
	m, err := s.loadLoki(name)
	if err != nil {
		return false
	}
	host := s.postgresHost()
	base := fmt.Sprintf("http://%s:%d", host, m.Port)
	push := base + "/loki/api/v1/push"
	if *primary {
		env["LOKI_URL"] = base
		env["LOKI_PUSH_URL"] = push
		*primary = false
	}
	prefix := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	env[prefix+"_URL"] = base
	env[prefix+"_PUSH_URL"] = push
	return true
}
