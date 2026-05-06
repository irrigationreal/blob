package server

import (
	"bytes"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/irrigationreal/blob/internal/api"
	"github.com/irrigationreal/blob/internal/display"
)

type uiPage struct {
	Title  string
	Active string
	Actor  authActor
	Routes []uiRoute
	Body   template.HTML
}

type uiRoute struct {
	Path   string
	Label  string
	Title  string
	Scope  string
	Render func(*Server, *http.Request) (template.HTML, error)
}

var uiRoutes = []uiRoute{
	{Path: "apps", Label: "Apps", Title: "Apps", Scope: "apps:read", Render: (*Server).uiApps},
	{Path: "nodes", Label: "Nodes", Title: "Nodes", Scope: "admin:read", Render: (*Server).uiNodes},
	{Path: "costs", Label: "Costs", Title: "Costs", Scope: "admin:read", Render: (*Server).uiCosts},
	{Path: "doctor", Label: "Doctor", Title: "Doctor", Scope: "admin:read", Render: (*Server).uiDoctor},
	{Path: "status-pages", Label: "Status", Title: "Status pages", Scope: "admin:read", Render: (*Server).uiStatusPages},
	{Path: "audit", Label: "Audit", Title: "Audit", Scope: "audit:read", Render: (*Server).uiAudit},
	{Path: "identity", Label: "Identity", Title: "Identity", Scope: "identity:admin", Render: (*Server).uiIdentity},
}

var uiFuncs = template.FuncMap{
	"join":    strings.Join,
	"shortID": display.ShortID,
	"time": func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.Format(time.RFC3339)
	},
	"usage": display.ResourceUsage,
}

func uiScopeForPath(path string) (string, bool) {
	route, ok := uiRouteForRequestPath(path)
	if !ok {
		return "", false
	}
	return route.Scope, true
}

func uiRouteForRequestPath(path string) (uiRoute, bool) {
	page := strings.Trim(strings.TrimPrefix(path, "/ui"), "/")
	if page == "" {
		page = "apps"
	}
	if strings.Contains(page, "/") {
		return uiRoute{}, false
	}
	for _, route := range uiRoutes {
		if route.Path == page {
			return route, true
		}
	}
	return uiRoute{}, false
}

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeErr(w, 405, "method not allowed")
		return
	}
	page := strings.Trim(strings.TrimPrefix(r.URL.Path, "/ui"), "/")
	if page == "" {
		http.Redirect(w, r, "/ui/apps", http.StatusFound)
		return
	}
	route, ok := uiRouteForRequestPath(r.URL.Path)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	body, err := route.Render(s, r)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = uiLayout.Execute(w, uiPage{Title: route.Title, Active: route.Path, Actor: actorFromContext(r.Context()), Routes: uiRoutes, Body: body})
}

func (s *Server) uiApps(r *http.Request) (template.HTML, error) {
	apps, err := s.listApps(r.Context())
	if err != nil {
		return "", err
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].App < apps[j].App })
	return renderUIFragment("apps", apps)
}

func (s *Server) uiNodes(r *http.Request) (template.HTML, error) {
	out, err := s.listNodes(r.Context())
	if err != nil {
		return "", err
	}
	return renderUIFragment("nodes", out)
}

func (s *Server) uiCosts(r *http.Request) (template.HTML, error) {
	snap, err := s.collectCostSnapshot(r.Context(), 0)
	if err != nil {
		cached, ok := s.loadCostSnapshot()
		if !ok {
			return "", err
		}
		snap = cached
	}
	return renderUIFragment("costs", snap)
}

func (s *Server) uiDoctor(r *http.Request) (template.HTML, error) {
	return renderUIFragment("doctor", s.runDoctor(r.Context()))
}

func (s *Server) uiStatusPages(r *http.Request) (template.HTML, error) {
	out, err := s.listStatusPages()
	if err != nil {
		return "", err
	}
	return renderUIFragment("status-pages", out)
}

func (s *Server) uiAudit(r *http.Request) (template.HTML, error) {
	events, err := s.listAuditEvents(50)
	if err != nil {
		return "", err
	}
	return renderUIFragment("audit", events)
}

func (s *Server) uiIdentity(r *http.Request) (template.HTML, error) {
	tokens, grants, err := s.identityOverview()
	if err != nil {
		return "", err
	}
	return renderUIFragment("identity", struct {
		Tokens []api.IdentityToken
		Grants []api.IdentityGrant
	}{Tokens: tokens.Tokens, Grants: grants.Grants})
}

func renderUIFragment(name string, data any) (template.HTML, error) {
	var buf bytes.Buffer
	if err := uiFragments.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

var uiFragments = template.Must(template.New("fragments").Funcs(uiFuncs).Parse(`
{{define "apps"}}
<h1>Apps</h1>
<table><thead><tr><th>App</th><th>Status</th><th>Form</th><th>Replicas</th><th>URL</th><th>Image</th></tr></thead><tbody>
{{range .}}<tr><td>{{.App}}</td><td><span class="pill">{{.Status}}</span></td><td>{{.Form}}</td><td>{{.Replicas}}</td><td>{{if .URL}}<a href="{{.URL}}">{{.URL}}</a>{{else}}-{{end}}</td><td class="muted">{{.Image}}</td></tr>{{else}}<tr><td colspan="6" class="muted">No apps</td></tr>{{end}}
</tbody></table>
{{end}}

{{define "nodes"}}
<h1>Nodes</h1><p class="muted">Generated {{time .GeneratedAt}}</p>
<table><thead><tr><th>ID</th><th>Name</th><th>Address</th><th>Status</th><th>Eligible</th><th>CPU R/A/T</th><th>Memory R/A/T</th><th>Disk R/A/T</th><th>Allocs</th></tr></thead><tbody>
{{range .Nodes}}<tr><td>{{shortID .ID}}</td><td>{{.Name}}</td><td>{{.Address}}</td><td>{{.Status}}</td><td>{{if .Drain}}draining{{else}}{{.Eligible}}{{end}}</td><td>{{usage .Resources.CPU ""}}</td><td>{{usage .Resources.MemoryMB "MiB"}}</td><td>{{usage .Resources.DiskMB "MiB"}}</td><td>{{.ActiveAllocations}}</td></tr>{{else}}<tr><td colspan="9" class="muted">No nodes</td></tr>{{end}}
</tbody></table>
{{end}}

{{define "costs"}}
<h1>Costs</h1><p class="muted">Generated {{time .GeneratedAt}} · <a href="/ui/costs?refresh=1">refresh</a></p>
<div class="cards"><div><b>Nodes</b><span>{{.Summary.NodeCount}}</span></div><div><b>Apps</b><span>{{.Summary.AppCount}}</span></div><div><b>Allocs</b><span>{{.Summary.ActiveAllocations}}</span></div><div><b>Memory</b><span>{{usage .Summary.MemoryMB "MiB"}}</span></div></div>
<h2>Top memory apps</h2>
<table><thead><tr><th>App</th><th>CPU</th><th>Memory MiB</th><th>Disk MiB</th><th>Allocs</th><th>Nodes</th></tr></thead><tbody>
{{range .Apps}}<tr><td>{{.App}}</td><td>{{.CPU}}</td><td>{{.MemoryMB}}</td><td>{{.DiskMB}}</td><td>{{.Allocations}}</td><td>{{join .Nodes ", "}}</td></tr>{{else}}<tr><td colspan="6" class="muted">No active app allocations</td></tr>{{end}}
</tbody></table>
{{end}}

{{define "doctor"}}
<h1>Doctor</h1><p>{{.Checked}} checks, {{len .Issues}} issues</p>
<table><thead><tr><th>Severity</th><th>Category</th><th>App</th><th>Title</th><th>Detail</th><th>Fix</th></tr></thead><tbody>
{{range .Issues}}<tr><td>{{.Severity}}</td><td>{{.Category}}</td><td>{{.App}}</td><td>{{.Title}}</td><td>{{.Detail}}</td><td>{{.Remediate}}</td></tr>{{else}}<tr><td colspan="6" class="muted">No issues</td></tr>{{end}}
</tbody></table>
{{end}}

{{define "status-pages"}}
<h1>Status pages</h1>
<table><thead><tr><th>App</th><th>URL</th><th>Created</th></tr></thead><tbody>
{{range .Pages}}<tr><td>{{.App}}</td><td><a href="{{.URL}}">{{.URL}}</a></td><td>{{time .CreatedAt}}</td></tr>{{else}}<tr><td colspan="3" class="muted">No status pages</td></tr>{{end}}
</tbody></table>
{{end}}

{{define "audit"}}
<h1>Audit</h1><p class="muted">Last 50 mutating API actions. Bodies are not stored.</p>
<table><thead><tr><th>Time</th><th>Actor</th><th>Method</th><th>Action</th><th>Status</th><th>Path</th></tr></thead><tbody>
{{range .}}<tr><td>{{time .CreatedAt}}</td><td>{{.Actor}}</td><td>{{.Method}}</td><td>{{.Action}}</td><td>{{.StatusCode}}</td><td>{{.Path}}</td></tr>{{else}}<tr><td colspan="6" class="muted">No audit events</td></tr>{{end}}
</tbody></table>
{{end}}

{{define "identity"}}
<h1>Identity</h1><p class="muted">Service-token metadata only. Secrets are shown once at creation and never stored here.</p>
<h2>Tokens</h2>
<table><thead><tr><th>ID</th><th>Name</th><th>Created</th><th>Revoked</th><th>Scopes</th></tr></thead><tbody>
{{range .Tokens}}<tr><td>{{.ID}}</td><td>{{.Name}}</td><td>{{time .CreatedAt}}</td><td>{{time .RevokedAt}}</td><td>{{join .Scopes ", "}}</td></tr>{{else}}<tr><td colspan="5" class="muted">No service tokens</td></tr>{{end}}
</tbody></table>
<h2>Grants</h2>
<table><thead><tr><th>Token</th><th>Scope</th><th>Updated</th></tr></thead><tbody>
{{range .Grants}}<tr><td>{{.TokenID}}</td><td>{{.Scope}}</td><td>{{time .CreatedAt}}</td></tr>{{else}}<tr><td colspan="3" class="muted">No grants</td></tr>{{end}}
</tbody></table>
{{end}}
`))

var uiLayout = template.Must(template.New("ui").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.Title}} - Blob</title>
<style>
:root{color-scheme:dark light;font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#0b1020;color:#e7edf7}body{margin:0}a{color:#8ecbff}header{display:flex;align-items:center;justify-content:space-between;padding:16px 22px;border-bottom:1px solid #26324b;background:#11182b;position:sticky;top:0}nav{display:flex;gap:10px;flex-wrap:wrap}nav a{color:#c9d7ef;text-decoration:none;padding:7px 10px;border-radius:8px}.brand{font-weight:700;letter-spacing:.04em}.shell{max-width:1280px;margin:0 auto;padding:24px}table{border-collapse:collapse;width:100%;font-size:14px;background:#11182b;border:1px solid #26324b;border-radius:10px;overflow:hidden}th,td{text-align:left;padding:10px 12px;border-bottom:1px solid #26324b;vertical-align:top}th{color:#9fb4d8;font-weight:600;background:#141d33}.muted{color:#9fb4d8}.pill{display:inline-block;padding:2px 8px;border-radius:999px;background:#20304f}.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:12px;margin:18px 0}.cards div{background:#11182b;border:1px solid #26324b;border-radius:12px;padding:14px}.cards b{display:block;color:#9fb4d8;font-size:12px;text-transform:uppercase}.cards span{font-size:22px}h1{margin:0 0 16px}h2{margin-top:28px}.actor{color:#9fb4d8;font-size:13px}.active{background:#20304f;color:#fff}
</style></head><body><header><div class="brand">Blob console</div><nav>{{range .Routes}}<a {{if eq $.Active .Path}}class="active"{{end}} href="/ui/{{.Path}}">{{.Label}}</a>{{end}}</nav><div class="actor">{{.Actor.Name}} {{if .Actor.Owner}}owner{{end}}</div></header><main class="shell">{{.Body}}</main></body></html>`))
