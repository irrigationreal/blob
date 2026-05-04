// Package server: ephemeral preview environments (v0.12).
//
// `blob preview create <app> --branch <name>` deploys an isolated copy of
// the parent app under a synthesized job name `<app>-<branch>` reachable
// at `<app>-<branch>.<base-domain>`. The source tarball is the same one
// already uploaded for the parent app (we don't re-upload). State at
// /srv/blob/previews/<app>/<branch>.json.
//
// Destruction reuses the standard destroy path on the synthesized job
// id, then removes the JSON sentinel.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/darvell/blob/internal/api"
	"github.com/darvell/blob/internal/manifest"
)

var branchRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

func (s *Server) previewsDir(app string) string {
	return filepath.Join(s.cfg.StateDir, "previews", app)
}

func (s *Server) previewPath(app, branch string) string {
	return filepath.Join(s.previewsDir(app), branch+".json")
}

func (s *Server) loadPreview(app, branch string) (*api.Preview, error) {
	b, err := os.ReadFile(s.previewPath(app, branch))
	if err != nil {
		return nil, err
	}
	p := &api.Preview{}
	if err := json.Unmarshal(b, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Server) savePreview(p *api.Preview) error {
	if err := os.MkdirAll(s.previewsDir(p.App), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.previewPath(p.App, p.Branch), b, 0o600)
}

// previewJobName is the synthetic Nomad job id for a preview deploy.
// We deliberately don't use jobID() to avoid the env-prefixed shape —
// the URL must be a clean `<app>-<branch>.<base>` for human readability.
func previewJobName(app, branch string) string {
	return app + "-" + branch
}

// HTTP handlers — wired under /v1/apps/<app>/preview[/<branch>]

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request, app, branch string) {
	if !validName(app) {
		writeErr(w, 400, "invalid app")
		return
	}
	if branch != "" && !branchRE.MatchString(branch) {
		writeErr(w, 400, "invalid branch (lowercase a-z 0-9 hyphens)")
		return
	}
	switch {
	case branch == "" && r.Method == "GET":
		out, err := s.listPreviews(app)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, api.ListPreviewsResponse{App: app, Previews: out})
	case branch != "" && r.Method == "GET":
		p, err := s.loadPreview(app, branch)
		if err != nil {
			writeErr(w, 404, "no such preview")
			return
		}
		writeJSON(w, 200, p)
	case branch != "" && r.Method == "POST":
		p, err := s.createPreview(r.Context(), app, branch)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, p)
	case branch != "" && r.Method == "DELETE":
		if err := s.destroyPreview(r.Context(), app, branch); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"app": app, "branch": branch, "destroyed": true})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) listPreviews(app string) ([]api.Preview, error) {
	dir := s.previewsDir(app)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []api.Preview
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p, err := s.loadPreview(app, strings.TrimSuffix(e.Name(), ".json"))
		if err == nil {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Branch < out[j].Branch })
	return out, nil
}

// createPreview deploys an ephemeral copy of <app>'s last-uploaded
// source tarball under the synthesized job name <app>-<branch>. For
// multi-component App manifests, every component lands as its own
// Nomad job under the branch namespace: <app>-<branch>-<component>.
// Each component gets its own subdomain
// <app>-<branch>-<component>.<base-domain>; the canonical preview URL
// (the one returned in the top-level URL field) points at the first
// component for back-compat.
//
// Reuses the parent's source dir (no re-upload). Reads each component's
// shape from the source's blob.yaml so port/env/services/command/etc.
// match what `blob deploy` would have produced for the same source.
func (s *Server) createPreview(ctx context.Context, app, branch string) (*api.Preview, error) {
	src := filepath.Join(s.cfg.SourcesDir, app)
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("no uploaded source for parent app %q — run `blob deploy` for it first", app)
	}
	parentMeta, ok := s.loadJobMeta(app)
	if !ok {
		return nil, fmt.Errorf("parent app %q has no job meta — deploy it first", app)
	}
	m, err := manifest.Load(filepath.Join(src, "blob.yaml"))
	if err != nil {
		stdLog("preview %s/%s: blob.yaml load failed: %v (falling back to parent jobMeta only)", app, branch, err)
		// Without a manifest we can only do the single-component
		// shortcut using whatever the parent's jobMeta tells us.
		m = nil
	}

	// Decide the component list. Single-component manifests (or the
	// fallback path) deploy one job named <app>-<branch>. App manifests
	// with N components deploy N jobs named <app>-<branch>-<component>.
	type compToDeploy struct {
		name     string // component name; empty means "this is the single-component case"
		jobID    string
		domain   string
		req      *api.DeployRequest
	}
	var plan []compToDeploy
	mkReq := func(c manifest.Component, jobID, domain string) *api.DeployRequest {
		req := &api.DeployRequest{
			App:         jobID,
			Environment: "prod",
			Form:        parentMeta.Form,
			Domain:      domain,
		}
		if c.Form != "" {
			req.Form = c.Form
		}
		if c.Port > 0 {
			req.Port = c.Port
		}
		if len(c.Env) > 0 {
			req.Env = c.Env
		}
		if len(c.Services) > 0 {
			req.Services = c.Services
		}
		if len(c.Command) > 0 {
			req.Command = c.Command
		}
		if c.CPU > 0 {
			req.CPU = c.CPU
		}
		if c.Memory > 0 {
			req.Memory = c.Memory
		}
		if c.Replicas > 0 {
			req.Replicas = c.Replicas
		}
		if c.Root != "" {
			req.Root = c.Root
		}
		if c.Build != "" {
			req.Build = c.Build
		}
		return req
	}
	if m != nil && m.IsApp() && len(m.Components) > 0 {
		for _, c := range m.Components {
			id := previewJobName(app, branch) + "-" + c.Name
			d := id + "." + s.cfg.BaseDomain
			plan = append(plan, compToDeploy{
				name:   c.Name,
				jobID:  id,
				domain: d,
				req:    mkReq(c, id, d),
			})
		}
	} else {
		c := manifest.Component{}
		if m != nil {
			c = m.Component
		}
		id := previewJobName(app, branch)
		d := id + "." + s.cfg.BaseDomain
		plan = append(plan, compToDeploy{
			name:   "", // single-component case
			jobID:  id,
			domain: d,
			req:    mkReq(c, id, d),
		})
	}

	// Drive the deploys. If any single component fails we tear down the
	// ones that already landed so we don't leave half a preview in place.
	var deployed []compToDeploy
	for _, c := range plan {
		_, derr := s.deployFromSource(ctx, src, c.req, app)
		if derr != nil {
			// Roll back what we already deployed.
			for _, prev := range deployed {
				if e := s.destroyApp(ctx, prev.jobID); e != nil {
					stdLog("preview rollback %s: destroyApp %s failed: %v", app, prev.jobID, e)
				}
			}
			return nil, fmt.Errorf("deploy component %q: %w", componentLabel(c.name), derr)
		}
		deployed = append(deployed, c)
	}

	// Persist the sentinel. Top-level fields point at the first
	// component for the single-URL display path; Components is
	// populated for multi-component previews.
	p := &api.Preview{
		App:       app,
		Branch:    branch,
		JobID:     deployed[0].jobID,
		Domain:    deployed[0].domain,
		URL:       "https://" + deployed[0].domain,
		CreatedAt: time.Now(),
	}
	if len(deployed) > 1 || (len(deployed) == 1 && deployed[0].name != "") {
		for _, c := range deployed {
			p.Components = append(p.Components, api.PreviewComponent{
				Name:   c.name,
				JobID:  c.jobID,
				Domain: c.domain,
				URL:    "https://" + c.domain,
			})
		}
	}
	if err := s.savePreview(p); err != nil {
		stdLog("preview save failed for %s/%s: %v (deploy is live anyway)", app, branch, err)
	}
	return p, nil
}

func componentLabel(name string) string {
	if name == "" {
		return "<single>"
	}
	return name
}

// destroyPreview tears down every Nomad job in the preview's branch
// namespace, then removes the sentinel. We read the sentinel first
// when present so we know which components to destroy; if it's missing
// (e.g. the file was nuked manually) we fall back to destroying just
// the canonical <app>-<branch> job for back-compat with v0.12 previews.
func (s *Server) destroyPreview(ctx context.Context, app, branch string) error {
	if !branchRE.MatchString(branch) {
		return errors.New("invalid branch")
	}
	if p, err := s.loadPreview(app, branch); err == nil {
		jobs := []string{}
		if len(p.Components) > 0 {
			for _, c := range p.Components {
				jobs = append(jobs, c.JobID)
			}
		} else {
			jobs = append(jobs, p.JobID)
		}
		for _, j := range jobs {
			if err := s.destroyApp(ctx, j); err != nil {
				stdLog("preview destroy %s/%s: destroyApp %s: %v (continuing)", app, branch, j, err)
			}
		}
	} else {
		// No sentinel — best effort on the v0.12 single-job shape.
		jobName := previewJobName(app, branch)
		if err := s.destroyApp(ctx, jobName); err != nil {
			stdLog("preview destroy %s/%s: %v (continuing)", app, branch, err)
		}
	}
	_ = os.Remove(s.previewPath(app, branch))
	return nil
}
