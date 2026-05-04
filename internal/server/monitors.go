package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	urlpkg "net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/darvell/blob/internal/api"
)

const (
	defaultMonitorIntervalSeconds = 60
	defaultMonitorTimeoutSeconds  = 5
	defaultMonitorExpectedStatus  = 200
	monitorLoopInterval           = 15 * time.Second
)

type monitorRunner struct {
	srv  *Server
	stop chan struct{}
}

func newMonitorRunner(s *Server) *monitorRunner {
	return &monitorRunner{srv: s, stop: make(chan struct{})}
}

func (m *monitorRunner) Start() {
	go m.loop()
}

func (m *monitorRunner) Stop() {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
}

func (m *monitorRunner) loop() {
	t := time.NewTicker(monitorLoopInterval)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.tick()
		}
	}
}

func (m *monitorRunner) tick() {
	monitors, err := m.srv.listMonitors()
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, mon := range monitors.Monitors {
		if !mon.Enabled || !monitorDue(&mon, now) {
			continue
		}
		if _, err := m.srv.checkMonitor(context.Background(), mon.Name); err != nil {
			stdLog("monitor[%s]: %v", mon.Name, err)
		}
	}
}

func monitorDue(mon *api.Monitor, now time.Time) bool {
	if mon.LastCheck.CheckedAt.IsZero() {
		return true
	}
	interval := time.Duration(mon.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = defaultMonitorIntervalSeconds * time.Second
	}
	return now.Sub(mon.LastCheck.CheckedAt) >= interval
}

func (s *Server) monitorsDir() string {
	return filepath.Join(s.cfg.StateDir, "monitors")
}

func (s *Server) monitorPath(name string) string {
	return filepath.Join(s.monitorsDir(), name+".json")
}

func (s *Server) handleMonitors(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listMonitors()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.AddMonitorRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.addMonitor(r.Context(), &req)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleMonitorItem(w http.ResponseWriter, r *http.Request) {
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/monitors/"), "/")
	if !validName(name) {
		writeErr(w, 400, "invalid monitor name")
		return
	}
	switch r.Method {
	case "GET":
		mon, err := s.loadMonitor(name)
		if err != nil {
			writeErr(w, 404, "monitor not found")
			return
		}
		writeJSON(w, 200, &api.MonitorResponse{Monitor: *mon})
	case "DELETE":
		if err := s.removeMonitor(name); err != nil {
			writeErr(w, 404, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"name": name, "removed": true})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) addMonitor(ctx context.Context, req *api.AddMonitorRequest) (*api.MonitorResponse, error) {
	name := sanitizeMonitorName(firstNonEmpty(req.Name, req.App))
	if !validName(name) {
		return nil, errors.New("invalid monitor name")
	}
	app := strings.TrimSpace(req.App)
	if app == "" {
		app = name
	}
	if app != "" && !validName(app) {
		return nil, errors.New("invalid app name")
	}
	url := strings.TrimSpace(req.URL)
	if url == "" {
		st, err := s.appStatus(ctx, app)
		if err != nil {
			return nil, err
		}
		url = monitorURLWithPath(st.URL, req.Path)
	}
	if err := validateMonitorURL(url); err != nil {
		return nil, err
	}
	interval := req.IntervalSeconds
	if interval <= 0 {
		interval = defaultMonitorIntervalSeconds
	}
	if interval < 15 {
		return nil, errors.New("interval_seconds must be >= 15")
	}
	timeout := req.TimeoutSeconds
	if timeout <= 0 {
		timeout = defaultMonitorTimeoutSeconds
	}
	if timeout < 1 || timeout > 60 {
		return nil, errors.New("timeout_seconds must be between 1 and 60")
	}
	expected := req.ExpectedStatus
	if expected <= 0 {
		expected = defaultMonitorExpectedStatus
	}
	if expected < 100 || expected > 599 {
		return nil, errors.New("expected_status must be between 100 and 599")
	}
	webhook := strings.TrimSpace(req.AlertWebhook)
	if webhook != "" {
		if err := validateMonitorURL(webhook); err != nil {
			return nil, fmt.Errorf("alert webhook: %w", err)
		}
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	now := time.Now().UTC()
	mon := &api.Monitor{
		Name:            name,
		App:             app,
		URL:             url,
		IntervalSeconds: interval,
		TimeoutSeconds:  timeout,
		ExpectedStatus:  expected,
		AlertWebhook:    webhook,
		Enabled:         enabled,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if existing, err := s.loadMonitor(name); err == nil {
		mon.CreatedAt = existing.CreatedAt
		mon.LastCheck = existing.LastCheck
		mon.ConsecutiveFailures = existing.ConsecutiveFailures
		mon.LastAlertStatus = existing.LastAlertStatus
		mon.LastAlertAt = existing.LastAlertAt
	}
	if err := s.saveMonitor(mon); err != nil {
		return nil, err
	}
	checked, err := s.checkMonitor(ctx, name)
	if err != nil {
		return nil, err
	}
	return &api.MonitorResponse{Monitor: *checked}, nil
}

func (s *Server) listMonitors() (*api.ListMonitorsResponse, error) {
	out := &api.ListMonitorsResponse{}
	entries, err := os.ReadDir(s.monitorsDir())
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
		mon, err := s.loadMonitor(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out.Monitors = append(out.Monitors, *mon)
	}
	sort.Slice(out.Monitors, func(i, j int) bool { return out.Monitors[i].Name < out.Monitors[j].Name })
	return out, nil
}

func (s *Server) loadMonitor(name string) (*api.Monitor, error) {
	b, err := os.ReadFile(s.monitorPath(name))
	if err != nil {
		return nil, err
	}
	mon := &api.Monitor{}
	if err := json.Unmarshal(b, mon); err != nil {
		return nil, err
	}
	return mon, nil
}

func (s *Server) saveMonitor(mon *api.Monitor) error {
	if err := os.MkdirAll(s.monitorsDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(mon, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.monitorPath(mon.Name), b, 0o600)
}

func (s *Server) removeMonitor(name string) error {
	if _, err := s.loadMonitor(name); err != nil {
		return errors.New("monitor not found")
	}
	return removeIgnoringMissing(s.monitorPath(name))
}

func (s *Server) checkMonitor(ctx context.Context, name string) (*api.Monitor, error) {
	mon, err := s.loadMonitor(name)
	if err != nil {
		return nil, err
	}
	previous := monitorHealthState(mon.LastCheck)
	health := probeMonitor(ctx, mon)
	mon.LastCheck = health
	mon.UpdatedAt = time.Now().UTC()
	if health.OK {
		mon.ConsecutiveFailures = 0
	} else {
		mon.ConsecutiveFailures++
	}
	current := monitorHealthState(health)
	if err := s.saveMonitor(mon); err != nil {
		return nil, err
	}
	if mon.AlertWebhook != "" && previous != "" && previous != current {
		if err := sendMonitorAlert(ctx, mon, previous, current); err != nil {
			stdLog("monitor[%s] alert hook: %v", mon.Name, err)
		} else {
			mon.LastAlertStatus = current
			mon.LastAlertAt = time.Now().UTC()
			_ = s.saveMonitor(mon)
		}
	}
	return mon, nil
}

func probeMonitor(ctx context.Context, mon *api.Monitor) api.RouteHealth {
	timeout := time.Duration(mon.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultMonitorTimeoutSeconds * time.Second
	}
	expected := mon.ExpectedStatus
	if expected <= 0 {
		expected = defaultMonitorExpectedStatus
	}
	return probeHTTPURL(ctx, mon.URL, "blob-monitor/1", timeout, validateMonitorURL, func(statusCode int, _ string) (bool, string, string) {
		if statusCode == expected {
			return true, "reachable", ""
		}
		return false, "failing", "HTTP " + strconv.Itoa(statusCode) + ", expected " + strconv.Itoa(expected)
	})
}

func monitorHealthState(health api.RouteHealth) string {
	if health.Status == "" {
		return ""
	}
	if health.OK {
		return "up"
	}
	return "down"
}

func sendMonitorAlert(ctx context.Context, mon *api.Monitor, previous, current string) error {
	payload := map[string]any{
		"monitor":         mon.Name,
		"app":             mon.App,
		"url":             mon.URL,
		"previous_status": previous,
		"status":          current,
		"checked_at":      mon.LastCheck.CheckedAt,
		"status_code":     mon.LastCheck.StatusCode,
		"error":           mon.LastCheck.Error,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	alertCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(alertCtx, "POST", mon.AlertWebhook, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "blob-monitor/1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %s", resp.Status)
	}
	return nil
}

func publicMonitorStatuses(all []api.Monitor, app string) []api.PublicMonitorStatus {
	out := make([]api.PublicMonitorStatus, 0)
	for _, mon := range all {
		if mon.App != app {
			continue
		}
		out = append(out, api.PublicMonitorStatus{
			Name:      mon.Name,
			URL:       mon.URL,
			Health:    mon.LastCheck,
			UpdatedAt: mon.UpdatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func monitorURLWithPath(base, p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return base
	}
	u, err := urlpkg.Parse(base)
	if err != nil {
		return base
	}
	if strings.HasPrefix(p, "/") {
		u.Path = p
	} else {
		u.Path = "/" + p
	}
	u.RawQuery = ""
	return u.String()
}

func validateMonitorURL(rawURL string) error {
	return validatePublicHTTPURL(rawURL, "monitor url", "monitor")
}

func sanitizeMonitorName(name string) string {
	return strings.Trim(strings.ToLower(strings.ReplaceAll(name, "_", "-")), "-")
}
