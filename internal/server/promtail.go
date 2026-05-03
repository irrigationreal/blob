// Package server: Promtail managed-service driver (v0.8 observability).
//
// Differs from loki/grafana/valkey in one important way: the Nomad job is
// `type = "system"` so Nomad places exactly one alloc on every client node
// (current and future). Each alloc bind-mounts the host's
// /var/lib/nomad/alloc and /var/log so it can tail every workload's stdout
// and the host journal.
//
// One promtail instance per cluster is the expected shape. The operator
// names it (e.g. `blob promtail create platform --loki main`) and the Loki
// push URL is resolved at create time from the named Loki instance — same
// pattern as grafana → loki so the data plane is self-discovering.
//
// TODO(v0.8): wire HTTP routes (handleLoki/Grafana/Promtail + their item
// variants) and extendResolveServices() registrations for all three drivers.
// Held until the end-to-end wire-up cycle so loki + grafana + promtail go
// live together.
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

type promtailMeta struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	LokiURL   string    `json:"loki_url"`      // base http://host:port (push path appended at job render time)
	LokiName  string    `json:"loki_instance"` // captured for re-render after loki recreation
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) promtailMetaDir() string {
	return filepath.Join(s.cfg.StateDir, "promtail")
}

func (s *Server) loadPromtail(name string) (*promtailMeta, error) {
	b, err := os.ReadFile(filepath.Join(s.promtailMetaDir(), name+".json"))
	if err != nil {
		return nil, err
	}
	m := &promtailMeta{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Server) savePromtail(m *promtailMeta) error {
	if err := os.MkdirAll(s.promtailMetaDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.promtailMetaDir(), m.Name+".json"), b, 0o600)
}

// --- HTTP handlers -----------------------------------------------------------

func (s *Server) handlePromtail(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listPromtail(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.CreatePromtailRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.createPromtail(r.Context(), &req)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handlePromtailItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/promtail/")
	parts := strings.SplitN(rest, "/", 2)
	name := parts[0]
	if !validName(name) {
		writeErr(w, 400, "invalid name")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == "GET":
		m, err := s.loadPromtail(name)
		if err != nil {
			writeErr(w, 404, "no such promtail")
			return
		}
		writeJSON(w, 200, s.promtailPublic(r.Context(), m))
	case len(parts) == 1 && r.Method == "DELETE":
		if err := s.destroyPromtail(r.Context(), name); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"name": name, "destroyed": true})
	default:
		writeErr(w, 404, "not found")
	}
}

// --- create / destroy / list --------------------------------------------------

func (s *Server) createPromtail(ctx context.Context, req *api.CreatePromtailRequest) (*api.Promtail, error) {
	if !validName(req.Name) {
		return nil, errors.New("invalid name")
	}
	if _, err := s.loadPromtail(req.Name); err == nil {
		return nil, fmt.Errorf("promtail %q already exists", req.Name)
	}
	if req.LokiInstance == "" {
		return nil, errors.New("loki_instance required (which managed Loki to ship to)")
	}
	lm, err := s.loadLoki(req.LokiInstance)
	if err != nil {
		return nil, fmt.Errorf("loki %q not found", req.LokiInstance)
	}
	if req.Version == "" {
		req.Version = "3.6"
	}
	if req.CPU <= 0 {
		req.CPU = 100
	}
	if req.Memory <= 0 {
		req.Memory = 128
	}
	m := &promtailMeta{
		Name:      req.Name,
		Version:   req.Version,
		LokiURL:   fmt.Sprintf("http://%s:%d", s.postgresHost(), lm.Port),
		LokiName:  req.LokiInstance,
		CreatedAt: time.Now(),
	}
	if err := s.savePromtail(m); err != nil {
		return nil, err
	}
	id := "promtail-" + m.Name
	hcl := renderPromtailJob(m, s.cfg.Datacenter, id, req.CPU, req.Memory)
	if err := os.MkdirAll(s.cfg.JobsDir, 0o755); err != nil {
		return nil, err
	}
	jobPath := filepath.Join(s.cfg.JobsDir, id+".nomad")
	if err := os.WriteFile(jobPath, []byte(hcl), 0o644); err != nil {
		return nil, err
	}
	if err := s.run(ctx, "nomad", "job", "run", jobPath); err != nil {
		_ = os.Remove(filepath.Join(s.promtailMetaDir(), m.Name+".json"))
		return nil, fmt.Errorf("schedule promtail: %w", err)
	}
	// system jobs: don't wait for "running" status the same way — Nomad
	// reports running once at least one alloc is healthy, which is fine.
	if err := s.waitJobRunning(ctx, id, 90*time.Second); err != nil {
		return nil, fmt.Errorf("promtail %q did not become ready: %w", m.Name, err)
	}
	return s.promtailPublic(ctx, m), nil
}

func (s *Server) destroyPromtail(ctx context.Context, name string) error {
	m, err := s.loadPromtail(name)
	if err != nil {
		return errors.New("no such promtail")
	}
	id := "promtail-" + m.Name
	if err := s.run(ctx, "nomad", "job", "stop", "-purge", id); err != nil {
		stdLog("promtail destroy %s: nomad stop returned %v (continuing)", name, err)
	}
	_ = os.Remove(filepath.Join(s.cfg.JobsDir, id+".nomad"))
	_ = os.Remove(filepath.Join(s.promtailMetaDir(), m.Name+".json"))
	return nil
}

func (s *Server) listPromtail(ctx context.Context) (*api.ListPromtailResponse, error) {
	out := &api.ListPromtailResponse{}
	entries, err := os.ReadDir(s.promtailMetaDir())
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
		m, err := s.loadPromtail(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out.Promtail = append(out.Promtail, *s.promtailPublic(ctx, m))
	}
	sort.Slice(out.Promtail, func(i, j int) bool { return out.Promtail[i].Name < out.Promtail[j].Name })
	return out, nil
}

func (s *Server) promtailPublic(ctx context.Context, m *promtailMeta) *api.Promtail {
	status := "unknown"
	if body, err := s.nomadGET(ctx, "/v1/job/promtail-"+m.Name); err == nil {
		var j struct{ Status string }
		_ = json.Unmarshal(body, &j)
		status = j.Status
	}
	return &api.Promtail{
		Name:         m.Name,
		Version:      m.Version,
		JobID:        "promtail-" + m.Name,
		LokiInstance: m.LokiName,
		LokiURL:      m.LokiURL,
		Status:       status,
		CreatedAt:    m.CreatedAt,
	}
}
