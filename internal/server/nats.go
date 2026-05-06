// Package server: NATS managed-service driver (v0.10 messaging).
//
// Single-node NATS with JetStream enabled. One instance per cluster is
// the expected shape. Apps bind via `services: [<name>]`; we inject
// NATS_URL into the consumer's environment.
//
// JetStream data lives on a Docker named volume blob-nats-<name> mounted
// at /data inside the container. The server listens on the static host
// port lookupNATSForBinding announces.
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
	natsPortFloor = 14222
	natsPortCeil  = 14322
)

type natsMeta struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Port      int       `json:"port"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) natsMetaDir() string { return filepath.Join(s.cfg.StateDir, "nats") }

func (s *Server) loadNATS(name string) (*natsMeta, error) {
	b, err := os.ReadFile(filepath.Join(s.natsMetaDir(), name+".json"))
	if err != nil {
		return nil, err
	}
	m := &natsMeta{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Server) saveNATS(m *natsMeta) error {
	if err := os.MkdirAll(s.natsMetaDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.natsMetaDir(), m.Name+".json"), b, 0o600)
}

func (s *Server) allocateNATSPort() (int, error) {
	used := map[int]bool{}
	if entries, err := os.ReadDir(s.natsMetaDir()); err == nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			m, err := s.loadNATS(strings.TrimSuffix(e.Name(), ".json"))
			if err == nil {
				used[m.Port] = true
			}
		}
	}
	for p := natsPortFloor; p < natsPortCeil; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, errors.New("nats port pool exhausted")
}

// HTTP handlers

func (s *Server) handleNATS(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listNATS(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.CreateNATSRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.createNATS(r.Context(), &req)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleNATSItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/nats/")
	parts := strings.SplitN(rest, "/", 2)
	name := parts[0]
	if !validName(name) {
		writeErr(w, 400, "invalid name")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == "GET":
		m, err := s.loadNATS(name)
		if err != nil {
			writeErr(w, 404, "no such nats")
			return
		}
		writeJSON(w, 200, s.natsPublic(r.Context(), m))
	case len(parts) == 1 && r.Method == "DELETE":
		if err := s.destroyNATS(r.Context(), name); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"name": name, "destroyed": true})
	default:
		writeErr(w, 404, "not found")
	}
}

func (s *Server) createNATS(ctx context.Context, req *api.CreateNATSRequest) (*api.NATS, error) {
	if !validName(req.Name) {
		return nil, errors.New("invalid name")
	}
	if _, err := s.loadNATS(req.Name); err == nil {
		return nil, fmt.Errorf("nats %q already exists", req.Name)
	}
	if req.Version == "" {
		req.Version = "2.10-alpine"
	}
	if req.CPU <= 0 {
		req.CPU = 200
	}
	if req.Memory <= 0 {
		req.Memory = 256
	}
	port, err := s.allocateNATSPort()
	if err != nil {
		return nil, err
	}
	m := &natsMeta{Name: req.Name, Version: req.Version, Port: port, CreatedAt: time.Now()}
	if err := s.saveNATS(m); err != nil {
		return nil, err
	}
	id := "nats-" + m.Name
	hcl := renderNATSJob(m, s.cfg.Datacenter, id, req.CPU, req.Memory)
	if err := os.MkdirAll(s.cfg.JobsDir, 0o755); err != nil {
		return nil, err
	}
	jobPath := filepath.Join(s.cfg.JobsDir, id+".nomad")
	if err := os.WriteFile(jobPath, []byte(hcl), 0o644); err != nil {
		return nil, err
	}
	if err := s.run(ctx, "nomad", "job", "run", jobPath); err != nil {
		_ = os.Remove(filepath.Join(s.natsMetaDir(), m.Name+".json"))
		return nil, fmt.Errorf("schedule nats: %w", err)
	}
	if err := s.waitJobRunning(ctx, id, 60*time.Second); err != nil {
		return nil, fmt.Errorf("nats %q did not become ready: %w", m.Name, err)
	}
	return s.natsPublic(ctx, m), nil
}

func (s *Server) destroyNATS(ctx context.Context, name string) error {
	m, err := s.loadNATS(name)
	if err != nil {
		return errors.New("no such nats")
	}
	id := "nats-" + m.Name
	if err := s.run(ctx, "nomad", "job", "stop", "-purge", id); err != nil {
		stdLog("nats destroy %s: nomad stop returned %v (continuing)", name, err)
	}
	_ = os.Remove(filepath.Join(s.cfg.JobsDir, id+".nomad"))
	_ = os.Remove(filepath.Join(s.natsMetaDir(), m.Name+".json"))
	return nil
}

func (s *Server) listNATS(ctx context.Context) (*api.ListNATSResponse, error) {
	out := &api.ListNATSResponse{}
	entries, err := os.ReadDir(s.natsMetaDir())
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
		m, err := s.loadNATS(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out.NATS = append(out.NATS, *s.natsPublic(ctx, m))
	}
	sort.Slice(out.NATS, func(i, j int) bool { return out.NATS[i].Name < out.NATS[j].Name })
	return out, nil
}

func (s *Server) natsPublic(ctx context.Context, m *natsMeta) *api.NATS {
	host := s.postgresHost()
	status := "unknown"
	if body, err := s.nomadGET(ctx, "/v1/job/nats-"+m.Name); err == nil {
		var j struct{ Status string }
		_ = json.Unmarshal(body, &j)
		status = j.Status
	}
	return &api.NATS{
		Name:      m.Name,
		Version:   m.Version,
		Host:      host,
		Port:      m.Port,
		JobID:     "nats-" + m.Name,
		URL:       fmt.Sprintf("nats://%s:%d", host, m.Port),
		Status:    status,
		CreatedAt: m.CreatedAt,
	}
}

func (s *Server) lookupNATSForBinding(name string, env map[string]string, primary *bool) bool {
	m, err := s.loadNATS(name)
	if err != nil {
		return false
	}
	host := s.postgresHost()
	url := fmt.Sprintf("nats://%s:%d", host, m.Port)
	if *primary {
		env["NATS_URL"] = url
		*primary = false
	}
	prefix := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	env[prefix+"_URL"] = url
	return true
}
