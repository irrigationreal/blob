// Package server: Tempo managed-service driver (v0.10 distributed tracing).
//
// Single-tenant Tempo, OTLP gRPC ingest at :4317 (proxied through host port),
// HTTP API at :3200 for /api/search and Grafana datasource. Local-block
// storage on a Docker named volume blob-tempo-<name>.
//
// Apps bind via `services: [<name>]`; we inject TEMPO_URL (HTTP) and
// TEMPO_OTLP_GRPC (otlp endpoint) into the consumer's environment.
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
	tempoPortFloor = 13200
	tempoPortCeil  = 13300
)

type tempoMeta struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	HTTPPort  int       `json:"http_port"`
	OTLPPort  int       `json:"otlp_port"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) tempoMetaDir() string { return filepath.Join(s.cfg.StateDir, "tempo") }

func (s *Server) loadTempo(name string) (*tempoMeta, error) {
	b, err := os.ReadFile(filepath.Join(s.tempoMetaDir(), name+".json"))
	if err != nil {
		return nil, err
	}
	m := &tempoMeta{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Server) saveTempo(m *tempoMeta) error {
	if err := os.MkdirAll(s.tempoMetaDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.tempoMetaDir(), m.Name+".json"), b, 0o600)
}

func (s *Server) allocateTempoPort() (httpPort, otlpPort int, err error) {
	usedHTTP := map[int]bool{}
	usedOTLP := map[int]bool{}
	if entries, e := os.ReadDir(s.tempoMetaDir()); e == nil {
		for _, ent := range entries {
			if !strings.HasSuffix(ent.Name(), ".json") {
				continue
			}
			m, e := s.loadTempo(strings.TrimSuffix(ent.Name(), ".json"))
			if e == nil {
				usedHTTP[m.HTTPPort] = true
				usedOTLP[m.OTLPPort] = true
			}
		}
	}
	for p := tempoPortFloor; p < tempoPortCeil; p += 2 {
		if !usedHTTP[p] && !usedOTLP[p+1] {
			return p, p + 1, nil
		}
	}
	return 0, 0, errors.New("tempo port pool exhausted")
}

// HTTP handlers

func (s *Server) handleTempo(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listTempo(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.CreateTempoRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.createTempo(r.Context(), &req)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleTempoItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/tempo/")
	parts := strings.SplitN(rest, "/", 2)
	name := parts[0]
	if !validName(name) {
		writeErr(w, 400, "invalid name")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == "GET":
		m, err := s.loadTempo(name)
		if err != nil {
			writeErr(w, 404, "no such tempo")
			return
		}
		writeJSON(w, 200, s.tempoPublic(r.Context(), m))
	case len(parts) == 1 && r.Method == "DELETE":
		if err := s.destroyTempo(r.Context(), name); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"name": name, "destroyed": true})
	default:
		writeErr(w, 404, "not found")
	}
}

func (s *Server) createTempo(ctx context.Context, req *api.CreateTempoRequest) (*api.Tempo, error) {
	if !validName(req.Name) {
		return nil, errors.New("invalid name")
	}
	if _, err := s.loadTempo(req.Name); err == nil {
		return nil, fmt.Errorf("tempo %q already exists", req.Name)
	}
	if req.Version == "" {
		req.Version = "2.6.1"
	}
	if req.CPU <= 0 {
		req.CPU = 300
	}
	if req.Memory <= 0 {
		req.Memory = 512
	}
	httpPort, otlpPort, err := s.allocateTempoPort()
	if err != nil {
		return nil, err
	}
	m := &tempoMeta{
		Name:      req.Name,
		Version:   req.Version,
		HTTPPort:  httpPort,
		OTLPPort:  otlpPort,
		CreatedAt: time.Now(),
	}
	if err := s.saveTempo(m); err != nil {
		return nil, err
	}
	id := "tempo-" + m.Name
	hcl := renderTempoJob(m, s.cfg.Datacenter, id, req.CPU, req.Memory)
	if err := os.MkdirAll(s.cfg.JobsDir, 0o755); err != nil {
		return nil, err
	}
	jobPath := filepath.Join(s.cfg.JobsDir, id+".nomad")
	if err := os.WriteFile(jobPath, []byte(hcl), 0o644); err != nil {
		return nil, err
	}
	if err := s.run(ctx, "nomad", "job", "run", jobPath); err != nil {
		_ = os.Remove(filepath.Join(s.tempoMetaDir(), m.Name+".json"))
		return nil, fmt.Errorf("schedule tempo: %w", err)
	}
	if err := s.waitJobRunning(ctx, id, 90*time.Second); err != nil {
		return nil, fmt.Errorf("tempo %q did not become ready: %w", m.Name, err)
	}
	return s.tempoPublic(ctx, m), nil
}

func (s *Server) destroyTempo(ctx context.Context, name string) error {
	m, err := s.loadTempo(name)
	if err != nil {
		return errors.New("no such tempo")
	}
	id := "tempo-" + m.Name
	if err := s.run(ctx, "nomad", "job", "stop", "-purge", id); err != nil {
		stdLog("tempo destroy %s: nomad stop returned %v (continuing)", name, err)
	}
	_ = os.Remove(filepath.Join(s.cfg.JobsDir, id+".nomad"))
	_ = os.Remove(filepath.Join(s.tempoMetaDir(), m.Name+".json"))
	return nil
}

func (s *Server) listTempo(ctx context.Context) (*api.ListTempoResponse, error) {
	out := &api.ListTempoResponse{}
	entries, err := os.ReadDir(s.tempoMetaDir())
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
		m, err := s.loadTempo(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out.Tempo = append(out.Tempo, *s.tempoPublic(ctx, m))
	}
	sort.Slice(out.Tempo, func(i, j int) bool { return out.Tempo[i].Name < out.Tempo[j].Name })
	return out, nil
}

func (s *Server) tempoPublic(ctx context.Context, m *tempoMeta) *api.Tempo {
	host := s.postgresHost()
	status := "unknown"
	if body, err := s.nomadGET(ctx, "/v1/job/tempo-"+m.Name); err == nil {
		var j struct{ Status string }
		_ = json.Unmarshal(body, &j)
		status = j.Status
	}
	return &api.Tempo{
		Name:      m.Name,
		Version:   m.Version,
		Host:      host,
		HTTPPort:  m.HTTPPort,
		OTLPPort:  m.OTLPPort,
		JobID:     "tempo-" + m.Name,
		URL:       fmt.Sprintf("http://%s:%d", host, m.HTTPPort),
		OTLPGRPC:  fmt.Sprintf("%s:%d", host, m.OTLPPort),
		Status:    status,
		CreatedAt: m.CreatedAt,
	}
}

func (s *Server) lookupTempoForBinding(name string, env map[string]string, primary *bool) bool {
	m, err := s.loadTempo(name)
	if err != nil {
		return false
	}
	host := s.postgresHost()
	httpURL := fmt.Sprintf("http://%s:%d", host, m.HTTPPort)
	otlp := fmt.Sprintf("%s:%d", host, m.OTLPPort)
	if *primary {
		env["TEMPO_URL"] = httpURL
		env["TEMPO_OTLP_GRPC"] = otlp
		env["OTEL_EXPORTER_OTLP_ENDPOINT"] = "http://" + otlp
		*primary = false
	}
	prefix := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	env[prefix+"_URL"] = httpURL
	env[prefix+"_OTLP_GRPC"] = otlp
	return true
}

// firstTempoOTLP returns "host:port" of the first registered Tempo, or "".
// Used by blobd to pick its own OTel exporter target at startup.
func (s *Server) firstTempoOTLP() string {
	entries, err := os.ReadDir(s.tempoMetaDir())
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		m, err := s.loadTempo(strings.TrimSuffix(e.Name(), ".json"))
		if err == nil {
			return fmt.Sprintf("%s:%d", s.postgresHost(), m.OTLPPort)
		}
	}
	return ""
}
