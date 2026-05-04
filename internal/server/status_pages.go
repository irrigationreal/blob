package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/darvell/blob/internal/api"
)

var uuidLikeRE = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)

func (s *Server) statusPagesDir() string {
	return filepath.Join(s.cfg.StateDir, "status-pages")
}

func (s *Server) statusPageURL(app string) string {
	base := strings.Trim(s.cfg.BaseDomain, ".")
	if base == "" {
		return "/status/" + app
	}
	return "https://blob." + base + "/status/" + app
}

func (s *Server) statusPageFile(app string) string {
	return filepath.Join(s.statusPagesDir(), app+".json")
}

func (s *Server) loadStatusPage(app string) (*api.StatusPageBinding, error) {
	b, err := os.ReadFile(s.statusPageFile(app))
	if err != nil {
		return nil, err
	}
	p := &api.StatusPageBinding{}
	if err := json.Unmarshal(b, p); err != nil {
		return nil, err
	}
	p.URL = s.statusPageURL(p.App)
	return p, nil
}

func (s *Server) saveStatusPage(p *api.StatusPageBinding) error {
	if err := os.MkdirAll(s.statusPagesDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.statusPageFile(p.App), b, 0o600)
}

func (s *Server) deleteStatusPage(app string) error {
	if err := os.Remove(s.statusPageFile(app)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *Server) handleStatusPages(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listStatusPages()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.EnableStatusPageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.enableStatusPage(r.Context(), &req)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleStatusPagesItem(w http.ResponseWriter, r *http.Request) {
	app := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/status-pages/"), "/")
	if !validName(app) {
		writeErr(w, 400, "invalid app name")
		return
	}
	switch r.Method {
	case "GET":
		out, err := s.showStatusPage(r.Context(), app)
		if err != nil {
			writeErr(w, 404, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "DELETE":
		if err := s.disableStatusPage(app); err != nil {
			writeErr(w, 404, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"app": app, "disabled": true})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handlePublicStatusPage(w http.ResponseWriter, r *http.Request) {
	app, asJSON := publicStatusPagePath(r.URL.Path)
	if !validName(app) {
		writeErr(w, 404, "status page not found")
		return
	}
	out, err := s.publicStatusPage(r.Context(), app)
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	if asJSON || strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, 200, out)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(200)
	_, _ = w.Write([]byte(renderStatusPageHTML(out)))
}

func publicStatusPagePath(path string) (app string, asJSON bool) {
	rest := strings.Trim(strings.TrimPrefix(path, "/status/"), "/")
	if strings.Contains(rest, "/") {
		return "", false
	}
	if strings.HasSuffix(rest, ".json") {
		asJSON = true
		rest = strings.TrimSuffix(rest, ".json")
	}
	return rest, asJSON
}

func (s *Server) enableStatusPage(ctx context.Context, req *api.EnableStatusPageRequest) (*api.StatusPageResponse, error) {
	if !validName(req.App) {
		return nil, errors.New("invalid app name")
	}
	if _, err := s.appStatus(ctx, req.App); err != nil {
		return nil, err
	}
	binding, err := s.loadStatusPage(req.App)
	if err != nil {
		binding = &api.StatusPageBinding{App: req.App, CreatedAt: time.Now().UTC()}
	}
	binding.URL = s.statusPageURL(req.App)
	if err := s.saveStatusPage(binding); err != nil {
		return nil, err
	}
	status, err := s.publicStatusPage(ctx, req.App)
	if err != nil {
		return nil, err
	}
	return &api.StatusPageResponse{Binding: *binding, Status: *status}, nil
}

func (s *Server) listStatusPages() (*api.ListStatusPagesResponse, error) {
	out := &api.ListStatusPagesResponse{}
	entries, err := os.ReadDir(s.statusPagesDir())
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
		app := strings.TrimSuffix(e.Name(), ".json")
		binding, err := s.loadStatusPage(app)
		if err != nil {
			continue
		}
		out.Pages = append(out.Pages, *binding)
	}
	sort.Slice(out.Pages, func(i, j int) bool { return out.Pages[i].App < out.Pages[j].App })
	return out, nil
}

func (s *Server) showStatusPage(ctx context.Context, app string) (*api.StatusPageResponse, error) {
	binding, err := s.loadStatusPage(app)
	if err != nil {
		return nil, errors.New("status page not enabled")
	}
	status, err := s.publicStatusPage(ctx, app)
	if err != nil {
		return nil, err
	}
	return &api.StatusPageResponse{Binding: *binding, Status: *status}, nil
}

func (s *Server) disableStatusPage(app string) error {
	if _, err := s.loadStatusPage(app); err != nil {
		return errors.New("status page not enabled")
	}
	return s.deleteStatusPage(app)
}

func (s *Server) publicStatusPage(ctx context.Context, app string) (*api.PublicStatusPage, error) {
	binding, err := s.loadStatusPage(app)
	if err != nil {
		return nil, errors.New("status page not enabled")
	}
	out := &api.PublicStatusPage{
		App:         app,
		URL:         binding.URL,
		GeneratedAt: time.Now().UTC(),
	}
	if st, err := s.appStatus(ctx, app); err == nil {
		out.AppStatus = publicAppStatus(st)
		out.RouteHealth = probeRoute(ctx, st.URL)
	} else {
		out.AppStatus = api.PublicAppStatus{App: app, Status: "missing"}
		out.RouteHealth = api.RouteHealth{Status: "skipped", Error: "app is missing"}
	}
	if monitors, err := s.listMonitors(); err == nil {
		out.Monitors = publicMonitorStatuses(monitors.Monitors, app)
	}
	out.DoctorIssues = s.publicDoctorIssues(ctx, app)
	out.Overall = overallStatus(out.AppStatus, out.RouteHealth, out.DoctorIssues, out.Monitors)
	return out, nil
}

func publicAppStatus(st *api.StatusResponse) api.PublicAppStatus {
	return api.PublicAppStatus{
		App:       st.App,
		Form:      st.Form,
		Domain:    st.Domain,
		URL:       st.URL,
		Status:    st.Status,
		Replicas:  st.Replicas,
		UpdatedAt: st.UpdatedAt,
	}
}

type probeStatusClassifier func(statusCode int, statusLine string) (ok bool, status string, errText string)

func probeRoute(ctx context.Context, rawURL string) api.RouteHealth {
	if rawURL == "" {
		return api.RouteHealth{URL: rawURL, CheckedAt: time.Now().UTC(), Status: "skipped", Error: "app has no public route"}
	}
	return probeHTTPURL(ctx, rawURL, "blob-status-page/1", 5*time.Second, validateStatusProbeURL, func(statusCode int, statusLine string) (bool, string, string) {
		if statusCode < 500 {
			return true, "reachable", ""
		}
		return false, "failing", statusLine
	})
}

func probeHTTPURL(ctx context.Context, rawURL, userAgent string, timeout time.Duration, validate func(string) error, classify probeStatusClassifier) api.RouteHealth {
	out := api.RouteHealth{URL: rawURL, CheckedAt: time.Now().UTC()}
	if err := validate(rawURL); err != nil {
		out.Status = "skipped"
		out.Error = err.Error()
		return out
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, "GET", rawURL, nil)
	if err != nil {
		out.Status = "skipped"
		out.Error = sanitizePublicText(err.Error())
		return out
	}
	req.Header.Set("User-Agent", userAgent)
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	out.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		out.Status = "unreachable"
		out.Error = sanitizePublicText(err.Error())
		return out
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	out.StatusCode = resp.StatusCode
	out.OK, out.Status, out.Error = classify(resp.StatusCode, resp.Status)
	return out
}

func validateStatusProbeURL(rawURL string) error {
	return validatePublicHTTPURL(rawURL, "app route", "route probe")
}

func validatePublicHTTPURL(rawURL, thing, skipSubject string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid %s", thing)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("%s is not http or https", thing)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%s has no hostname", thing)
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".local") {
		return fmt.Errorf("%s skipped for local hostname", skipSubject)
	}
	if ip := net.ParseIP(host); ip != nil {
		return fmt.Errorf("%s skipped for literal IP hostname", skipSubject)
	}
	return nil
}

func (s *Server) publicDoctorIssues(ctx context.Context, app string) []api.PublicDoctorIssue {
	doc := s.runDoctor(ctx)
	out := make([]api.PublicDoctorIssue, 0, len(doc.Issues))
	for _, issue := range doc.Issues {
		if issue.App != app && !(issue.App == "" && isPublicGlobalIssue(issue)) {
			continue
		}
		out = append(out, api.PublicDoctorIssue{
			Severity:  issue.Severity,
			Category:  issue.Category,
			App:       issue.App,
			Title:     sanitizePublicText(issue.Title),
			Detail:    sanitizePublicText(issue.Detail),
			Remediate: sanitizePublicText(issue.Remediate),
		})
	}
	return out
}

func isPublicGlobalIssue(issue api.DoctorIssue) bool {
	return issue.Severity == "P1" || issue.Severity == "P2"
}

func sanitizePublicText(s string) string {
	return uuidLikeRE.ReplaceAllString(s, "<redacted-id>")
}

func overallStatus(app api.PublicAppStatus, route api.RouteHealth, issues []api.PublicDoctorIssue, monitors []api.PublicMonitorStatus) string {
	if app.Status == "dead" || app.Status == "missing" {
		return "down"
	}
	for _, issue := range issues {
		if issue.Severity == "P1" {
			return "down"
		}
	}
	for _, mon := range monitors {
		if monitorHealthState(mon.Health) == "down" {
			return "down"
		}
	}
	if app.Status != "running" {
		return "degraded"
	}
	if route.Status == "unreachable" || route.Status == "failing" {
		return "degraded"
	}
	for _, issue := range issues {
		if issue.Severity == "P2" {
			return "degraded"
		}
	}
	return "operational"
}

func renderStatusPageHTML(p *api.PublicStatusPage) string {
	var b strings.Builder
	statusColor := "#16794c"
	if p.Overall == "degraded" {
		statusColor = "#9a6700"
	} else if p.Overall == "down" {
		statusColor = "#b42318"
	}
	fmt.Fprintf(&b, "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">")
	fmt.Fprintf(&b, "<title>Blob status: %s</title>", html.EscapeString(p.App))
	fmt.Fprintf(&b, "<style>body{font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,sans-serif;margin:0;background:#f6f7f9;color:#111827}.wrap{max-width:760px;margin:48px auto;padding:0 20px}.card{background:white;border:1px solid #e5e7eb;border-radius:18px;padding:24px;box-shadow:0 8px 30px rgba(15,23,42,.06)}.pill{display:inline-block;border-radius:999px;padding:6px 12px;color:white;background:%s;font-weight:700}.muted{color:#667085}.grid{display:grid;grid-template-columns:160px 1fr;gap:10px;margin-top:22px}.issues{margin-top:24px}.issue{border-top:1px solid #e5e7eb;padding:12px 0}.mono{font-family:ui-monospace,SFMono-Regular,Menlo,monospace}</style></head><body><main class=\"wrap\"><section class=\"card\">", statusColor)
	fmt.Fprintf(&b, "<p class=\"muted\">The Blob status page</p><h1>%s</h1><span class=\"pill\">%s</span>", html.EscapeString(p.App), html.EscapeString(p.Overall))
	fmt.Fprintf(&b, "<div class=\"grid\"><div class=\"muted\">app status</div><div>%s</div><div class=\"muted\">route</div><div>%s, HTTP %d, %dms</div><div class=\"muted\">url</div><div><a href=\"%s\">%s</a></div><div class=\"muted\">generated</div><div class=\"mono\">%s</div></div>", html.EscapeString(p.AppStatus.Status), html.EscapeString(p.RouteHealth.Status), p.RouteHealth.StatusCode, p.RouteHealth.LatencyMS, html.EscapeString(p.AppStatus.URL), html.EscapeString(p.AppStatus.URL), html.EscapeString(p.GeneratedAt.Format(time.RFC3339)))
	if len(p.Monitors) > 0 {
		fmt.Fprintf(&b, "<div class=\"issues\"><h2>Monitors</h2>")
		for _, mon := range p.Monitors {
			fmt.Fprintf(&b, "<div class=\"issue\"><strong>%s</strong> %s", html.EscapeString(mon.Name), html.EscapeString(mon.Health.Status))
			if mon.Health.StatusCode != 0 {
				fmt.Fprintf(&b, " HTTP %d", mon.Health.StatusCode)
			}
			if mon.Health.LatencyMS != 0 {
				fmt.Fprintf(&b, " %dms", mon.Health.LatencyMS)
			}
			if mon.Health.Error != "" {
				fmt.Fprintf(&b, "<p class=\"muted\">%s</p>", html.EscapeString(mon.Health.Error))
			}
			fmt.Fprintf(&b, "</div>")
		}
		fmt.Fprintf(&b, "</div>")
	}
	fmt.Fprintf(&b, "<div class=\"issues\"><h2>Doctor issues</h2>")
	if len(p.DoctorIssues) == 0 {
		fmt.Fprintf(&b, "<p class=\"muted\">No current public issues for this app.</p>")
	} else {
		for _, issue := range p.DoctorIssues {
			fmt.Fprintf(&b, "<div class=\"issue\"><strong>%s %s</strong><br><span>%s</span>", html.EscapeString(issue.Severity), html.EscapeString(issue.Category), html.EscapeString(issue.Title))
			if issue.Detail != "" {
				fmt.Fprintf(&b, "<p class=\"muted\">%s</p>", html.EscapeString(issue.Detail))
			}
			fmt.Fprintf(&b, "</div>")
		}
	}
	fmt.Fprintf(&b, "</div><p class=\"muted\"><a href=\"/status/%s.json\">JSON</a></p></section></main></body></html>", html.EscapeString(p.App))
	return b.String()
}
