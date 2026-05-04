package server

import (
	"crypto/rand"
	"encoding/hex"
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

const defaultIncidentImpact = "minor"

func (s *Server) statusIncidentsDir() string {
	return filepath.Join(s.statusPagesDir(), "incidents")
}

func (s *Server) statusIncidentPath(id string) string {
	return filepath.Join(s.statusIncidentsDir(), id+".json")
}

func newStatusIncidentID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("inc-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("inc-%d-%s", time.Now().UnixNano(), hex.EncodeToString(b[:]))
}

func handleStatusIncidentImpact(impact string) (string, error) {
	impact = strings.ToLower(strings.TrimSpace(impact))
	if impact == "" {
		return defaultIncidentImpact, nil
	}
	switch impact {
	case "minor", "major", "critical", "maintenance":
		return impact, nil
	default:
		return "", errors.New("impact must be minor, major, critical, or maintenance")
	}
}

func (s *Server) handleStatusPageIncidents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listStatusIncidents(r.URL.Query().Get("app"), false)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.OpenStatusPageIncidentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.openStatusIncident(&req)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleStatusPageIncidentItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/status-pages/incidents/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || !validName(parts[0]) {
		writeErr(w, 400, "invalid incident id")
		return
	}
	id := parts[0]
	switch {
	case len(parts) == 1 && r.Method == "GET":
		incident, err := s.loadStatusIncident(id)
		if err != nil {
			writeErr(w, 404, "incident not found")
			return
		}
		writeJSON(w, 200, &api.StatusPageIncidentResponse{Incident: *incident})
	case len(parts) == 1 && (r.Method == "PATCH" || r.Method == "POST"):
		var req api.UpdateStatusPageIncidentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.updateStatusIncident(id, &req)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case len(parts) == 2 && parts[1] == "resolve" && r.Method == "POST":
		var req api.ResolveStatusPageIncidentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.resolveStatusIncident(id, &req)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 404, "not found")
	}
}

func (s *Server) openStatusIncident(req *api.OpenStatusPageIncidentRequest) (*api.StatusPageIncidentResponse, error) {
	app := strings.TrimSpace(req.App)
	if !validName(app) {
		return nil, errors.New("invalid app name")
	}
	if _, err := s.loadStatusPage(app); err != nil {
		return nil, errors.New("status page not enabled")
	}
	title := strings.TrimSpace(req.Title)
	if title == "" || len(title) > 160 {
		return nil, errors.New("title is required and must be <= 160 chars")
	}
	impact, err := handleStatusIncidentImpact(req.Impact)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	incident := &api.StatusPageIncident{
		ID:        newStatusIncidentID(),
		App:       app,
		Title:     title,
		Status:    "open",
		Impact:    impact,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if msg := strings.TrimSpace(req.Message); msg != "" {
		incident.Updates = append(incident.Updates, api.StatusPageIncidentUpdate{Message: msg, Status: incident.Status, Impact: incident.Impact, CreatedAt: now})
	}
	if err := s.saveStatusIncident(incident); err != nil {
		return nil, err
	}
	return &api.StatusPageIncidentResponse{Incident: *incident}, nil
}

func (s *Server) updateStatusIncident(id string, req *api.UpdateStatusPageIncidentRequest) (*api.StatusPageIncidentResponse, error) {
	incident, err := s.loadStatusIncident(id)
	if err != nil {
		return nil, errors.New("incident not found")
	}
	if !incident.Open() {
		return nil, errors.New("incident is already resolved")
	}
	title := strings.TrimSpace(req.Title)
	message := strings.TrimSpace(req.Message)
	impact := strings.TrimSpace(req.Impact)
	if title == "" && message == "" && impact == "" {
		return nil, errors.New("title, message, or impact is required")
	}
	changed := false
	if title != "" {
		if len(title) > 160 {
			return nil, errors.New("title must be <= 160 chars")
		}
		if title != incident.Title {
			incident.Title = title
			changed = true
		}
	}
	if impact != "" {
		parsed, err := handleStatusIncidentImpact(impact)
		if err != nil {
			return nil, err
		}
		if parsed != incident.Impact {
			incident.Impact = parsed
			changed = true
		}
	}
	if !changed && message == "" {
		return &api.StatusPageIncidentResponse{Incident: *incident}, nil
	}
	now := time.Now().UTC()
	if message != "" {
		incident.Updates = append(incident.Updates, api.StatusPageIncidentUpdate{Message: message, Status: incident.Status, Impact: incident.Impact, CreatedAt: now})
	}
	incident.UpdatedAt = now
	if err := s.saveStatusIncident(incident); err != nil {
		return nil, err
	}
	return &api.StatusPageIncidentResponse{Incident: *incident}, nil
}

func (s *Server) resolveStatusIncident(id string, req *api.ResolveStatusPageIncidentRequest) (*api.StatusPageIncidentResponse, error) {
	incident, err := s.loadStatusIncident(id)
	if err != nil {
		return nil, errors.New("incident not found")
	}
	if incident.Status == "resolved" {
		return &api.StatusPageIncidentResponse{Incident: *incident}, nil
	}
	now := time.Now().UTC()
	incident.Status = "resolved"
	incident.UpdatedAt = now
	incident.ResolvedAt = now
	message := strings.TrimSpace(req.Message)
	if message == "" {
		message = "Resolved"
	}
	incident.Updates = append(incident.Updates, api.StatusPageIncidentUpdate{Message: message, Status: incident.Status, Impact: incident.Impact, CreatedAt: now})
	if err := s.saveStatusIncident(incident); err != nil {
		return nil, err
	}
	return &api.StatusPageIncidentResponse{Incident: *incident}, nil
}

func (s *Server) listStatusIncidents(app string, openOnly bool) (*api.ListStatusPageIncidentsResponse, error) {
	if app != "" && !validName(app) {
		return nil, errors.New("invalid app name")
	}
	all, err := s.loadAllStatusIncidents()
	if err != nil {
		return nil, err
	}
	out := &api.ListStatusPageIncidentsResponse{}
	for _, incident := range all {
		if app != "" && incident.App != app {
			continue
		}
		if openOnly && !incident.Open() {
			continue
		}
		out.Incidents = append(out.Incidents, incident)
	}
	sortStatusIncidents(out.Incidents)
	return out, nil
}

func (s *Server) loadAllStatusIncidents() ([]api.StatusPageIncident, error) {
	entries, err := os.ReadDir(s.statusIncidentsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]api.StatusPageIncident, 0, len(entries))
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		incident, err := s.loadStatusIncident(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out = append(out, *incident)
	}
	return out, nil
}

func (s *Server) loadStatusIncident(id string) (*api.StatusPageIncident, error) {
	b, err := os.ReadFile(s.statusIncidentPath(id))
	if err != nil {
		return nil, err
	}
	incident := &api.StatusPageIncident{}
	if err := json.Unmarshal(b, incident); err != nil {
		return nil, err
	}
	return incident, nil
}

func (s *Server) saveStatusIncident(incident *api.StatusPageIncident) error {
	if err := os.MkdirAll(s.statusIncidentsDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(incident, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.statusIncidentPath(incident.ID), b, 0o600)
}

func sortStatusIncidents(incidents []api.StatusPageIncident) {
	sort.Slice(incidents, func(i, j int) bool {
		if incidents[i].Status != incidents[j].Status {
			return incidents[i].Open()
		}
		return incidents[i].CreatedAt.After(incidents[j].CreatedAt)
	})
}
