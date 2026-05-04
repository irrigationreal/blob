package server

import (
	"bytes"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/darvell/blob/internal/api"
)

type uiPage struct {
	Title  string
	Active string
	Actor  authActor
	Body   template.HTML
}

var uiFuncs = template.FuncMap{
	"join": strings.Join,
	"time": func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.Format(time.RFC3339)
	},
	"usage": formatUIUsage,
}

func formatUIUsage(u api.ResourceUsage, suffix string) string {
	if suffix != "" {
		return itoa(u.Reserved) + "/" + itoa(u.Available) + "/" + itoa(u.Total) + suffix
	}
	return itoa(u.Reserved) + "/" + itoa(u.Available) + "/" + itoa(u.Total)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
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
	actor := actorFromContext(r.Context())
	var title string
	var body template.HTML
	var err error
	switch page {
	case "apps":
		title = "Apps"
		body, err = s.uiApps(r)
	case "nodes":
		title = "Nodes"
		body, err = s.uiNodes(r)
	case "costs":
		title = "Costs"
		body, err = s.uiCosts(r)
	case "doctor":
		title = "Doctor"
		body, err = s.uiDoctor(r)
	case "status-pages":
		title = "Status pages"
		body, err = s.uiStatusPages(r)
	case "audit":
		title = "Audit"
		body, err = s.uiAudit(r)
	case "identity":
		title = "Identity"
		body, err = s.uiIdentity(r)
	default:
		writeErr(w, 404, "not found")
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = uiLayout.Execute(w, uiPage{Title: title, Active: page, Actor: actor, Body: body})
}

func (s *Server) uiApps(r *http.Request) (template.HTML, error) {
	apps, err := s.listApps(r.Context())
	if err != nil {
		return "", err
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].App < apps[j].App })
	return renderUIFragment(`
<h1>Apps</h1>
<table><thead><tr><th>App</th><th>Status</th><th>Form</th><th>Replicas</th><th>URL</th><th>Image</th></tr></thead><tbody>
{{range .}}<tr><td>{{.App}}</td><td><span class="pill">{{.Status}}</span></td><td>{{.Form}}</td><td>{{.Replicas}}</td><td>{{if .URL}}<a href="{{.URL}}">{{.URL}}</a>{{else}}-{{end}}</td><td class="muted">{{.Image}}</td></tr>{{end}}
</tbody></table>`, apps)
}

func (s *Server) uiNodes(r *http.Request) (template.HTML, error) {
	out, err := s.listNodes(r.Context())
	if err != nil {
		return "", err
	}
	return renderUIFragment(`
<h1>Nodes</h1><p class="muted">Generated {{time .GeneratedAt}}</p>
<table><thead><tr><th>ID</th><th>Name</th><th>Address</th><th>Status</th><th>Eligible</th><th>CPU R/A/T</th><th>Memory R/A/T</th><th>Disk R/A/T</th><th>Allocs</th></tr></thead><tbody>
{{range .Nodes}}<tr><td>{{shortID .ID}}</td><td>{{.Name}}</td><td>{{.Address}}</td><td>{{.Status}}</td><td>{{if .Drain}}draining{{else}}{{.Eligible}}{{end}}</td><td>{{usage .Resources.CPU ""}}</td><td>{{usage .Resources.MemoryMB "MiB"}}</td><td>{{usage .Resources.DiskMB "MiB"}}</td><td>{{.ActiveAllocations}}</td></tr>{{end}}
</tbody></table>`, out)
}

func (s *Server) uiCosts(r *http.Request) (template.HTML, error) {
	snap, err := s.collectCostSnapshot(r.Context(), 0)
	if err != nil {
		if cached, ok := s.loadCostSnapshot(); ok {
			snap = cached
		} else {
			return "", err
		}
	}
	return renderUIFragment(`
<h1>Costs</h1><p class="muted">Generated {{time .GeneratedAt}}</p>
<div class="cards"><div><b>Nodes</b><span>{{.Summary.NodeCount}}</span></div><div><b>Apps</b><span>{{.Summary.AppCount}}</span></div><div><b>Allocs</b><span>{{.Summary.ActiveAllocations}}</span></div><div><b>Memory</b><span>{{usage .Summary.MemoryMB "MiB"}}</span></div></div>
<h2>Top memory apps</h2>
<table><thead><tr><th>App</th><th>CPU</th><th>Memory MiB</th><th>Disk MiB</th><th>Allocs</th><th>Nodes</th></tr></thead><tbody>
{{range .Apps}}<tr><td>{{.App}}</td><td>{{.CPU}}</td><td>{{.MemoryMB}}</td><td>{{.DiskMB}}</td><td>{{.Allocations}}</td><td>{{join .Nodes ", "}}</td></tr>{{end}}
</tbody></table>`, snap)
}

func (s *Server) uiDoctor(r *http.Request) (template.HTML, error) {
	out := s.runDoctor(r.Context())
	return renderUIFragment(`
<h1>Doctor</h1><p>{{.Checked}} checks, {{len .Issues}} issues</p>
<table><thead><tr><th>Severity</th><th>Category</th><th>App</th><th>Title</th><th>Detail</th><th>Fix</th></tr></thead><tbody>
{{range .Issues}}<tr><td>{{.Severity}}</td><td>{{.Category}}</td><td>{{.App}}</td><td>{{.Title}}</td><td>{{.Detail}}</td><td>{{.Remediate}}</td></tr>{{else}}<tr><td colspan="6" class="muted">No issues</td></tr>{{end}}
</tbody></table>`, out)
}

func (s *Server) uiStatusPages(r *http.Request) (template.HTML, error) {
	out, err := s.listStatusPages()
	if err != nil {
		return "", err
	}
	return renderUIFragment(`
<h1>Status pages</h1>
<table><thead><tr><th>App</th><th>URL</th><th>Created</th></tr></thead><tbody>
{{range .Pages}}<tr><td>{{.App}}</td><td><a href="{{.URL}}">{{.URL}}</a></td><td>{{time .CreatedAt}}</td></tr>{{else}}<tr><td colspan="3" class="muted">No status pages</td></tr>{{end}}
</tbody></table>`, out)
}

func (s *Server) uiAudit(r *http.Request) (template.HTML, error) {
	events, err := s.listAuditEvents(50)
	if err != nil {
		return "", err
	}
	return renderUIFragment(`
<h1>Audit</h1><p class="muted">Last 50 mutating API actions. Bodies are not stored.</p>
<table><thead><tr><th>Time</th><th>Actor</th><th>Method</th><th>Action</th><th>Status</th><th>Path</th></tr></thead><tbody>
{{range .}}<tr><td>{{time .CreatedAt}}</td><td>{{.Actor}}</td><td>{{.Method}}</td><td>{{.Action}}</td><td>{{.StatusCode}}</td><td>{{.Path}}</td></tr>{{else}}<tr><td colspan="6" class="muted">No audit events</td></tr>{{end}}
</tbody></table>`, events)
}

func (s *Server) uiIdentity(r *http.Request) (template.HTML, error) {
	tokens, err := s.listIdentityTokens()
	if err != nil {
		return "", err
	}
	grants, err := s.listIdentityGrants("")
	if err != nil {
		return "", err
	}
	data := struct {
		Tokens []api.IdentityToken
		Grants []api.IdentityGrant
	}{Tokens: tokens.Tokens, Grants: grants.Grants}
	return renderUIFragment(`
<h1>Identity</h1><p class="muted">Service-token metadata only. Secrets are shown once at creation and never stored here.</p>
<h2>Tokens</h2>
<table><thead><tr><th>ID</th><th>Name</th><th>Created</th><th>Revoked</th><th>Scopes</th></tr></thead><tbody>
{{range .Tokens}}<tr><td>{{.ID}}</td><td>{{.Name}}</td><td>{{time .CreatedAt}}</td><td>{{time .RevokedAt}}</td><td>{{join .Scopes ", "}}</td></tr>{{else}}<tr><td colspan="5" class="muted">No service tokens</td></tr>{{end}}
</tbody></table>
<h2>Grants</h2>
<table><thead><tr><th>Token</th><th>Scope</th><th>Updated</th></tr></thead><tbody>
{{range .Grants}}<tr><td>{{.TokenID}}</td><td>{{.Scope}}</td><td>{{time .CreatedAt}}</td></tr>{{else}}<tr><td colspan="3" class="muted">No grants</td></tr>{{end}}
</tbody></table>`, data)
}

func renderUIFragment(src string, data any) (template.HTML, error) {
	funcs := template.FuncMap{}
	for k, v := range uiFuncs {
		funcs[k] = v
	}
	funcs["shortID"] = shortUIID
	t, err := template.New("fragment").Funcs(funcs).Parse(src)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

func shortUIID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

var uiLayout = template.Must(template.New("ui").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.Title}} - Blob</title>
<style>
:root{color-scheme:dark light;font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#0b1020;color:#e7edf7}body{margin:0}a{color:#8ecbff}header{display:flex;align-items:center;justify-content:space-between;padding:16px 22px;border-bottom:1px solid #26324b;background:#11182b;position:sticky;top:0}nav{display:flex;gap:10px;flex-wrap:wrap}nav a{color:#c9d7ef;text-decoration:none;padding:7px 10px;border-radius:8px}.brand{font-weight:700;letter-spacing:.04em}.shell{max-width:1280px;margin:0 auto;padding:24px}table{border-collapse:collapse;width:100%;font-size:14px;background:#11182b;border:1px solid #26324b;border-radius:10px;overflow:hidden}th,td{text-align:left;padding:10px 12px;border-bottom:1px solid #26324b;vertical-align:top}th{color:#9fb4d8;font-weight:600;background:#141d33}.muted{color:#9fb4d8}.pill{display:inline-block;padding:2px 8px;border-radius:999px;background:#20304f}.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:12px;margin:18px 0}.cards div{background:#11182b;border:1px solid #26324b;border-radius:12px;padding:14px}.cards b{display:block;color:#9fb4d8;font-size:12px;text-transform:uppercase}.cards span{font-size:22px}h1{margin:0 0 16px}h2{margin-top:28px}.actor{color:#9fb4d8;font-size:13px}
</style></head><body><header><div class="brand">Blob console</div><nav><a href="/ui/apps">Apps</a><a href="/ui/nodes">Nodes</a><a href="/ui/costs">Costs</a><a href="/ui/doctor">Doctor</a><a href="/ui/status-pages">Status</a><a href="/ui/audit">Audit</a><a href="/ui/identity">Identity</a></nav><div class="actor">{{.Actor.Name}} {{if .Actor.Owner}}owner{{end}}</div></header><main class="shell">{{.Body}}</main></body></html>`))
