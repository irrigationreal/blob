// Package server: Grafana managed-service driver (v0.8 observability).
//
// Mirrors the Loki/Valkey/Postgres pattern: each instance is a Nomad service
// job running grafana/grafana:<ver> on a static host port (13000+) with a
// Docker named volume for /var/lib/grafana (sqlite + plugins). An admin
// password is generated on create and stored in /srv/blob/grafana/<name>.json
// (mode 0600).
//
// Datasource and dashboard provisioning files are written via Nomad
// `template` stanzas in renderGrafanaJob — no on-host config files. The
// Loki datasource URL is read from LOKI_URL, which is injected into the
// Grafana workload's env when the operator binds via `services: [<loki>]`.
//
// TODO(v0.8): wire HTTP routes (handleGrafana / handleGrafanaItem) and
// extendResolveServices() so apps can `services: [<grafana>]` (same TODO
// applies to loki.go — both pending the end-to-end cycle).
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
	grafanaPortFloor = 13000
	grafanaPortCeil  = 13100
)

type grafanaMeta struct {
	Name          string    `json:"name"`
	Version       string    `json:"version"`
	Port          int       `json:"port"`
	AdminPassword string    `json:"admin_password"`
	LokiURL       string    `json:"loki_url,omitempty"` // captured at create time from services: binding
	CreatedAt     time.Time `json:"created_at"`
}

func (s *Server) grafanaMetaDir() string {
	return filepath.Join(s.cfg.StateDir, "grafana")
}

func (s *Server) loadGrafana(name string) (*grafanaMeta, error) {
	b, err := os.ReadFile(filepath.Join(s.grafanaMetaDir(), name+".json"))
	if err != nil {
		return nil, err
	}
	m := &grafanaMeta{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Server) saveGrafana(m *grafanaMeta) error {
	if err := os.MkdirAll(s.grafanaMetaDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.grafanaMetaDir(), m.Name+".json"), b, 0o600)
}

func (s *Server) allocateGrafanaPort() (int, error) {
	used := map[int]bool{}
	if entries, err := os.ReadDir(s.grafanaMetaDir()); err == nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			m, err := s.loadGrafana(strings.TrimSuffix(e.Name(), ".json"))
			if err == nil {
				used[m.Port] = true
			}
		}
	}
	for p := grafanaPortFloor; p < grafanaPortCeil; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, errors.New("grafana port pool exhausted")
}

// --- HTTP handlers -----------------------------------------------------------

func (s *Server) handleGrafana(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listGrafana(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.CreateGrafanaRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.createGrafana(r.Context(), &req)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleGrafanaItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/grafana/")
	parts := strings.SplitN(rest, "/", 2)
	name := parts[0]
	if !validName(name) {
		writeErr(w, 400, "invalid name")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == "GET":
		m, err := s.loadGrafana(name)
		if err != nil {
			writeErr(w, 404, "no such grafana")
			return
		}
		writeJSON(w, 200, s.grafanaPublic(r.Context(), m))
	case len(parts) == 1 && r.Method == "DELETE":
		if err := s.destroyGrafana(r.Context(), name); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"name": name, "destroyed": true})
	case len(parts) == 2 && parts[1] == "url" && r.Method == "GET":
		m, err := s.loadGrafana(name)
		if err != nil {
			writeErr(w, 404, "no such grafana")
			return
		}
		writeJSON(w, 200, api.GrafanaURL{URL: s.grafanaURL(m, false), AdminPassword: m.AdminPassword})
	default:
		writeErr(w, 404, "not found")
	}
}

// --- create / destroy / list --------------------------------------------------

func (s *Server) createGrafana(ctx context.Context, req *api.CreateGrafanaRequest) (*api.Grafana, error) {
	if !validName(req.Name) {
		return nil, errors.New("invalid name")
	}
	if _, err := s.loadGrafana(req.Name); err == nil {
		return nil, fmt.Errorf("grafana %q already exists", req.Name)
	}
	if req.Version == "" {
		req.Version = "11.4.0"
	}
	if req.CPU <= 0 {
		req.CPU = 300
	}
	if req.Memory <= 0 {
		req.Memory = 384
	}
	port, err := s.allocateGrafanaPort()
	if err != nil {
		return nil, err
	}
	// Resolve Loki URL from the requested binding, if any. Stored in meta so
	// later restarts of the grafana job re-render the same datasource.
	lokiURL := ""
	if req.LokiInstance != "" {
		lm, err := s.loadLoki(req.LokiInstance)
		if err != nil {
			return nil, fmt.Errorf("loki %q not found", req.LokiInstance)
		}
		lokiURL = fmt.Sprintf("http://%s:%d", s.postgresHost(), lm.Port)
	}
	m := &grafanaMeta{
		Name:          req.Name,
		Version:       req.Version,
		Port:          port,
		AdminPassword: randomPassword(),
		LokiURL:       lokiURL,
		CreatedAt:     time.Now(),
	}
	if err := s.saveGrafana(m); err != nil {
		return nil, err
	}
	id := "grafana-" + m.Name
	hcl := renderGrafanaJob(m, s.cfg.Datacenter, id, req.CPU, req.Memory)
	if err := os.MkdirAll(s.cfg.JobsDir, 0o755); err != nil {
		return nil, err
	}
	jobPath := filepath.Join(s.cfg.JobsDir, id+".nomad")
	if err := os.WriteFile(jobPath, []byte(hcl), 0o644); err != nil {
		return nil, err
	}
	if err := s.run(ctx, "nomad", "job", "run", jobPath); err != nil {
		_ = os.Remove(filepath.Join(s.grafanaMetaDir(), m.Name+".json"))
		return nil, fmt.Errorf("schedule grafana: %w", err)
	}
	if err := s.waitJobRunning(ctx, id, 90*time.Second); err != nil {
		return nil, fmt.Errorf("grafana %q did not become ready: %w", m.Name, err)
	}
	return s.grafanaPublic(ctx, m), nil
}

func (s *Server) destroyGrafana(ctx context.Context, name string) error {
	m, err := s.loadGrafana(name)
	if err != nil {
		return errors.New("no such grafana")
	}
	id := "grafana-" + m.Name
	if err := s.run(ctx, "nomad", "job", "stop", "-purge", id); err != nil {
		stdLog("grafana destroy %s: nomad stop returned %v (continuing)", name, err)
	}
	_ = os.Remove(filepath.Join(s.cfg.JobsDir, id+".nomad"))
	_ = os.Remove(filepath.Join(s.grafanaMetaDir(), m.Name+".json"))
	// Docker volume preserved (matches postgres/valkey/loki semantics).
	return nil
}

func (s *Server) listGrafana(ctx context.Context) (*api.ListGrafanaResponse, error) {
	out := &api.ListGrafanaResponse{}
	entries, err := os.ReadDir(s.grafanaMetaDir())
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
		m, err := s.loadGrafana(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out.Grafana = append(out.Grafana, *s.grafanaPublic(ctx, m))
	}
	sort.Slice(out.Grafana, func(i, j int) bool { return out.Grafana[i].Name < out.Grafana[j].Name })
	return out, nil
}

func (s *Server) grafanaPublic(ctx context.Context, m *grafanaMeta) *api.Grafana {
	host := s.postgresHost()
	status := "unknown"
	if body, err := s.nomadGET(ctx, "/v1/job/grafana-"+m.Name); err == nil {
		var j struct{ Status string }
		_ = json.Unmarshal(body, &j)
		status = j.Status
	}
	return &api.Grafana{
		Name:      m.Name,
		Version:   m.Version,
		Host:      host,
		Port:      m.Port,
		JobID:     "grafana-" + m.Name,
		URL:       s.grafanaURL(m, true),
		LokiURL:   m.LokiURL,
		Status:    status,
		CreatedAt: m.CreatedAt,
	}
}

func (s *Server) grafanaURL(m *grafanaMeta, mask bool) string {
	pw := m.AdminPassword
	if mask {
		pw = "***"
	}
	_ = pw // password is not in the URL; admin login is form-based. Kept for symmetry with valkeyURL/postgresURL.
	return fmt.Sprintf("http://%s:%d", s.postgresHost(), m.Port)
}

// lookupGrafanaForBinding resolves a Grafana instance for a `services:`
// binding and injects GRAFANA_URL into the consumer's env.
func (s *Server) lookupGrafanaForBinding(name string, env map[string]string, primary *bool) bool {
	m, err := s.loadGrafana(name)
	if err != nil {
		return false
	}
	host := s.postgresHost()
	base := fmt.Sprintf("http://%s:%d", host, m.Port)
	if *primary {
		env["GRAFANA_URL"] = base
		*primary = false
	}
	prefix := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	env[prefix+"_URL"] = base
	return true
}
