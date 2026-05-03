// Package server: per-app horizontal autoscaler (v0.11).
//
// One in-process loop ticks every 30s. For each app with persisted
// autoscale config, it queries the first registered Prometheus for the
// metric and target, computes desired = ceil(current_replicas * value/target),
// clamps to [min,max], applies cooldown windows, and calls scaleApp.
//
// Persistence: /srv/blob/autoscale/<app>.json (mode 0600). Last-action
// timestamp lives in-memory only (an autoscaler restart resets cooldowns
// — this is acceptable for a controller that runs forever in normal ops).
//
// Metric-fetch failures are surfaced via stdLog and result in a no-op.
// We never scale to zero just because Prometheus is briefly unreachable.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	urlpkg "net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/darvell/blob/internal/api"
)

// autoscaler is owned by Server. It consults the persisted configs every
// tick. Cooldown timestamps are kept in memory (lastUp / lastDown per app).
type autoscaler struct {
	srv      *Server
	mu       sync.Mutex
	lastUp   map[string]time.Time
	lastDown map[string]time.Time
	stop     chan struct{}
}

func newAutoscaler(s *Server) *autoscaler {
	return &autoscaler{
		srv:      s,
		lastUp:   map[string]time.Time{},
		lastDown: map[string]time.Time{},
		stop:     make(chan struct{}),
	}
}

func (a *autoscaler) Start() {
	go a.loop()
}

func (a *autoscaler) Stop() {
	select {
	case <-a.stop:
	default:
		close(a.stop)
	}
}

func (a *autoscaler) loop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
			a.tickAll()
		}
	}
}

// tickAll iterates every persisted autoscale config and tries one
// reconcile pass per app. Errors are logged; one failing app does not
// stop us from looking at the next.
func (a *autoscaler) tickAll() {
	dir := a.srv.autoscaleDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // no configs yet
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		app := strings.TrimSuffix(e.Name(), ".json")
		if err := a.reconcile(context.Background(), app); err != nil {
			stdLog("autoscale[%s]: %v", app, err)
		}
	}
}

// reconcile is the controller's per-tick logic for one app. Pure modulo
// the metric-fetch + scaleApp side effects, so it's straightforward to
// test by injecting fake fetch + scale closures (see tests).
func (a *autoscaler) reconcile(ctx context.Context, app string) error {
	cfg, err := a.srv.loadAutoscale(app)
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return nil
	}
	current, err := a.srv.currentReplicas(ctx, app)
	if err != nil {
		return fmt.Errorf("read replicas: %w", err)
	}
	value, err := a.srv.queryAutoscaleMetric(ctx, cfg)
	if err != nil {
		// Don't scale on metric outage — explicit no-op, with a log.
		stdLog("autoscale[%s]: metric fetch failed: %v (skipping tick)", app, err)
		return nil
	}
	desired := desiredReplicas(current, value, cfg.Target, cfg.Min, cfg.Max)
	if desired == current {
		return nil
	}
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if desired > current {
		if cd := cfg.CooldownUp; cd > 0 {
			if last, ok := a.lastUp[app]; ok && now.Sub(last) < cd {
				return nil // cooling down from last scale-up
			}
		}
		a.lastUp[app] = now
	} else {
		if cd := cfg.CooldownDown; cd > 0 {
			if last, ok := a.lastDown[app]; ok && now.Sub(last) < cd {
				return nil
			}
		}
		a.lastDown[app] = now
	}
	stdLog("autoscale[%s]: %d → %d (metric=%s value=%.3f target=%.3f)",
		app, current, desired, cfg.Metric, value, cfg.Target)
	return a.srv.scaleApp(ctx, app, desired)
}

// desiredReplicas computes the desired replica count using Kubernetes-style
// ratio scaling: desired = ceil(current * value / target). Clamped to
// [min, max]. Pure function — tested directly.
func desiredReplicas(current int, value, target float64, minR, maxR int) int {
	if current <= 0 {
		current = 1
	}
	if target <= 0 {
		return current
	}
	ratio := value / target
	d := int(math.Ceil(float64(current) * ratio))
	if d < minR {
		d = minR
	}
	if d > maxR {
		d = maxR
	}
	return d
}

// --- persistence -------------------------------------------------------------

func (s *Server) autoscaleDir() string {
	return filepath.Join(s.cfg.StateDir, "autoscale")
}

func (s *Server) loadAutoscale(app string) (*api.AutoscaleConfig, error) {
	b, err := os.ReadFile(filepath.Join(s.autoscaleDir(), app+".json"))
	if err != nil {
		return nil, err
	}
	c := &api.AutoscaleConfig{}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, err
	}
	if c.App == "" {
		c.App = app
	}
	return c, nil
}

func (s *Server) saveAutoscale(c *api.AutoscaleConfig) error {
	if err := os.MkdirAll(s.autoscaleDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.autoscaleDir(), c.App+".json"), b, 0o600)
}

func (s *Server) deleteAutoscale(app string) error {
	return removeIgnoringMissing(filepath.Join(s.autoscaleDir(), app+".json"))
}

func (s *Server) listAutoscale() ([]api.AutoscaleConfig, error) {
	entries, err := os.ReadDir(s.autoscaleDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []api.AutoscaleConfig
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		c, err := s.loadAutoscale(strings.TrimSuffix(e.Name(), ".json"))
		if err == nil {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].App < out[j].App })
	return out, nil
}

// --- metric fetch ------------------------------------------------------------

// queryAutoscaleMetric resolves a metric expression for the configured kind
// and queries the first registered Prometheus instance. Returns the
// scalar/instantaneous value (a single number — we sum across replicas
// when multiple are running so the controller scales on aggregate load).
//
// Built-in metric kinds map to canonical PromQL:
//   - cpu      → sum(rate(container_cpu_usage_seconds_total{container_label_com_hashicorp_nomad_job_name="<app>"}[1m])) * 100 / replicas
//                (percent CPU per replica, 0..100). Requires cAdvisor metrics.
//   - memory   → sum(container_memory_working_set_bytes{container_label_com_hashicorp_nomad_job_name="<app>"}) / 1024 / 1024 / replicas
//                (MiB per replica). Same source.
//   - http_qps → sum(rate(blob_http_requests_total{app="<app>"}[1m])) / replicas
//                (RPS per replica — requires the app to expose /metrics with this counter)
//   - <raw>    → if the metric string contains `(` or `{`, treated as raw PromQL
//                with `__APP__` substituted with the app name.
func (s *Server) queryAutoscaleMetric(ctx context.Context, cfg *api.AutoscaleConfig) (float64, error) {
	base := s.firstPrometheusBase()
	if base == "" {
		return 0, errors.New("no prometheus instance registered (run `blob prometheus create ...`)")
	}
	q := buildAutoscaleQuery(cfg.App, cfg.Metric)
	if q == "" {
		return 0, fmt.Errorf("unknown metric %q", cfg.Metric)
	}
	u := fmt.Sprintf("%s/api/v1/query?query=%s", base, urlpkg.QueryEscape(q))
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(tctx, "GET", u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("prom %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var pr struct {
		Data struct {
			ResultType string          `json:"resultType"`
			Result     json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return 0, err
	}
	// Vector result: [{value: [ts, "v"]}, ...] — sum across series.
	// Scalar result: [ts, "v"]
	switch pr.Data.ResultType {
	case "scalar":
		var pair [2]any
		if err := json.Unmarshal(pr.Data.Result, &pair); err != nil {
			return 0, err
		}
		str, _ := pair[1].(string)
		var f float64
		fmt.Sscanf(str, "%f", &f)
		return f, nil
	case "vector":
		var arr []struct {
			Value [2]any `json:"value"`
		}
		if err := json.Unmarshal(pr.Data.Result, &arr); err != nil {
			return 0, err
		}
		if len(arr) == 0 {
			return 0, fmt.Errorf("prom returned no result for query %q", q)
		}
		// Sum across all series — for cpu/memory queries this yields total
		// (the queries already aggregate per-replica via avg/sum).
		total := 0.0
		for _, s := range arr {
			str, _ := s.Value[1].(string)
			var f float64
			fmt.Sscanf(str, "%f", &f)
			total += f
		}
		return total, nil
	default:
		return 0, fmt.Errorf("unsupported prom result type %q", pr.Data.ResultType)
	}
}

func buildAutoscaleQuery(app, metric string) string {
	// Raw PromQL passthrough. Operators can use any expression that
	// includes `__APP__` as a placeholder.
	if strings.ContainsAny(metric, "({") {
		return strings.ReplaceAll(metric, "__APP__", app)
	}
	switch metric {
	case "cpu":
		// Percent CPU per replica, averaged over 1 minute.
		return fmt.Sprintf(
			`sum(rate(container_cpu_usage_seconds_total{container_label_com_hashicorp_nomad_job_name=%q}[1m])) * 100 / count(container_cpu_usage_seconds_total{container_label_com_hashicorp_nomad_job_name=%q})`,
			app, app)
	case "memory":
		// MiB per replica, working-set.
		return fmt.Sprintf(
			`avg(container_memory_working_set_bytes{container_label_com_hashicorp_nomad_job_name=%q}) / 1024 / 1024`,
			app)
	case "http_qps":
		// Requires app to expose blob_http_requests_total with app="<n>".
		return fmt.Sprintf(
			`sum(rate(blob_http_requests_total{app=%q}[1m]))`,
			app)
	}
	return ""
}

// firstPrometheusBase returns the first registered Prometheus URL, or "".
func (s *Server) firstPrometheusBase() string {
	entries, err := os.ReadDir(s.prometheusMetaDir())
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		m, err := s.loadPrometheus(strings.TrimSuffix(e.Name(), ".json"))
		if err == nil {
			return fmt.Sprintf("http://%s:%d", s.postgresHost(), m.Port)
		}
	}
	return ""
}

// currentReplicas asks Nomad for the current count of running allocations
// (post-deployment count). Used as the input to ratio scaling.
func (s *Server) currentReplicas(ctx context.Context, app string) (int, error) {
	body, err := s.nomadGET(ctx, "/v1/job/"+app+"/allocations")
	if err != nil {
		return 0, err
	}
	var allocs []struct {
		ClientStatus, DesiredStatus string
	}
	if err := json.Unmarshal(body, &allocs); err != nil {
		return 0, err
	}
	count := 0
	for _, a := range allocs {
		if a.ClientStatus == "running" && a.DesiredStatus == "run" {
			count++
		}
	}
	if count == 0 {
		count = 1 // never report 0 — the autoscaler would propose scale-to-zero
	}
	return count, nil
}

// --- HTTP handlers -----------------------------------------------------------

func (s *Server) handleAutoscale(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listAutoscale()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, api.ListAutoscaleResponse{Autoscale: out})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleAutoscaleItem(w http.ResponseWriter, r *http.Request) {
	app := strings.TrimPrefix(r.URL.Path, "/v1/autoscale/")
	if !validName(app) {
		writeErr(w, 400, "invalid app")
		return
	}
	switch r.Method {
	case "GET":
		c, err := s.loadAutoscale(app)
		if err != nil {
			writeErr(w, 404, "no autoscale for "+app)
			return
		}
		writeJSON(w, 200, c)
	case "PUT":
		var req api.AutoscaleConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		req.App = app
		if req.Min < 0 || req.Max <= 0 || req.Min > req.Max {
			writeErr(w, 400, "min must be 0..max")
			return
		}
		if req.Target <= 0 {
			writeErr(w, 400, "target must be > 0")
			return
		}
		if buildAutoscaleQuery(app, req.Metric) == "" {
			writeErr(w, 400, "unknown metric "+req.Metric)
			return
		}
		req.Enabled = true
		if err := s.saveAutoscale(&req); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, &req)
	case "DELETE":
		if err := s.deleteAutoscale(app); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"app": app, "removed": true})
	default:
		writeErr(w, 405, "method not allowed")
	}
}
