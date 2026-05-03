// Package server: Prometheus managed-service driver (v0.10 metrics).
//
// Single-node Prometheus with auto-scrape configs for Nomad service
// discovery (every blob service registers `provider = "nomad"`), the
// Traefik admin endpoint, and blobd's own /metrics. TSDB lives on a
// Docker named volume blob-prometheus-<name>.
//
// Apps bind via `services: [<name>]`; we inject PROMETHEUS_URL into the
// consumer's environment.
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
	prometheusPortFloor = 13300
	prometheusPortCeil  = 13400
)

type prometheusMeta struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Port      int       `json:"port"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) prometheusMetaDir() string {
	return filepath.Join(s.cfg.StateDir, "prometheus")
}

func (s *Server) loadPrometheus(name string) (*prometheusMeta, error) {
	b, err := os.ReadFile(filepath.Join(s.prometheusMetaDir(), name+".json"))
	if err != nil {
		return nil, err
	}
	m := &prometheusMeta{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Server) savePrometheus(m *prometheusMeta) error {
	if err := os.MkdirAll(s.prometheusMetaDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.prometheusMetaDir(), m.Name+".json"), b, 0o600)
}

func (s *Server) allocatePrometheusPort() (int, error) {
	used := map[int]bool{}
	if entries, err := os.ReadDir(s.prometheusMetaDir()); err == nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			m, err := s.loadPrometheus(strings.TrimSuffix(e.Name(), ".json"))
			if err == nil {
				used[m.Port] = true
			}
		}
	}
	for p := prometheusPortFloor; p < prometheusPortCeil; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, errors.New("prometheus port pool exhausted")
}

// HTTP handlers

func (s *Server) handlePrometheus(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listPrometheus(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.CreatePrometheusRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.createPrometheus(r.Context(), &req)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handlePrometheusItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/prometheus/")
	parts := strings.SplitN(rest, "/", 2)
	name := parts[0]
	if !validName(name) {
		writeErr(w, 400, "invalid name")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == "GET":
		m, err := s.loadPrometheus(name)
		if err != nil {
			writeErr(w, 404, "no such prometheus")
			return
		}
		writeJSON(w, 200, s.prometheusPublic(r.Context(), m))
	case len(parts) == 1 && r.Method == "DELETE":
		if err := s.destroyPrometheus(r.Context(), name); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"name": name, "destroyed": true})
	default:
		writeErr(w, 404, "not found")
	}
}

func (s *Server) createPrometheus(ctx context.Context, req *api.CreatePrometheusRequest) (*api.Prometheus, error) {
	if !validName(req.Name) {
		return nil, errors.New("invalid name")
	}
	if _, err := s.loadPrometheus(req.Name); err == nil {
		return nil, fmt.Errorf("prometheus %q already exists", req.Name)
	}
	if req.Version == "" {
		req.Version = "v3.1.0"
	}
	if req.CPU <= 0 {
		req.CPU = 300
	}
	if req.Memory <= 0 {
		req.Memory = 512
	}
	port, err := s.allocatePrometheusPort()
	if err != nil {
		return nil, err
	}
	m := &prometheusMeta{Name: req.Name, Version: req.Version, Port: port, CreatedAt: time.Now()}
	if err := s.savePrometheus(m); err != nil {
		return nil, err
	}
	id := "prometheus-" + m.Name
	hcl := renderPrometheusJob(m, s.cfg.Datacenter, id, req.CPU, req.Memory, s.postgresHost())
	if err := os.MkdirAll(s.cfg.JobsDir, 0o755); err != nil {
		return nil, err
	}
	jobPath := filepath.Join(s.cfg.JobsDir, id+".nomad")
	if err := os.WriteFile(jobPath, []byte(hcl), 0o644); err != nil {
		return nil, err
	}
	if err := s.run(ctx, "nomad", "job", "run", jobPath); err != nil {
		_ = os.Remove(filepath.Join(s.prometheusMetaDir(), m.Name+".json"))
		return nil, fmt.Errorf("schedule prometheus: %w", err)
	}
	if err := s.waitJobRunning(ctx, id, 90*time.Second); err != nil {
		return nil, fmt.Errorf("prometheus %q did not become ready: %w", m.Name, err)
	}
	return s.prometheusPublic(ctx, m), nil
}

func (s *Server) destroyPrometheus(ctx context.Context, name string) error {
	m, err := s.loadPrometheus(name)
	if err != nil {
		return errors.New("no such prometheus")
	}
	id := "prometheus-" + m.Name
	if err := s.run(ctx, "nomad", "job", "stop", "-purge", id); err != nil {
		stdLog("prometheus destroy %s: nomad stop returned %v (continuing)", name, err)
	}
	_ = os.Remove(filepath.Join(s.cfg.JobsDir, id+".nomad"))
	_ = os.Remove(filepath.Join(s.prometheusMetaDir(), m.Name+".json"))
	return nil
}

func (s *Server) listPrometheus(ctx context.Context) (*api.ListPrometheusResponse, error) {
	out := &api.ListPrometheusResponse{}
	entries, err := os.ReadDir(s.prometheusMetaDir())
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
		m, err := s.loadPrometheus(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out.Prometheus = append(out.Prometheus, *s.prometheusPublic(ctx, m))
	}
	sort.Slice(out.Prometheus, func(i, j int) bool { return out.Prometheus[i].Name < out.Prometheus[j].Name })
	return out, nil
}

func (s *Server) prometheusPublic(ctx context.Context, m *prometheusMeta) *api.Prometheus {
	host := s.postgresHost()
	status := "unknown"
	if body, err := s.nomadGET(ctx, "/v1/job/prometheus-"+m.Name); err == nil {
		var j struct{ Status string }
		_ = json.Unmarshal(body, &j)
		status = j.Status
	}
	return &api.Prometheus{
		Name:      m.Name,
		Version:   m.Version,
		Host:      host,
		Port:      m.Port,
		JobID:     "prometheus-" + m.Name,
		URL:       fmt.Sprintf("http://%s:%d", host, m.Port),
		Status:    status,
		CreatedAt: m.CreatedAt,
	}
}

func (s *Server) lookupPrometheusForBinding(name string, env map[string]string, primary *bool) bool {
	m, err := s.loadPrometheus(name)
	if err != nil {
		return false
	}
	host := s.postgresHost()
	url := fmt.Sprintf("http://%s:%d", host, m.Port)
	if *primary {
		env["PROMETHEUS_URL"] = url
		*primary = false
	}
	prefix := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	env[prefix+"_URL"] = url
	return true
}
