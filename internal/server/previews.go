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
// source tarball under the job name <app>-<branch>.<base-domain>. It
// reuses the same form/port/etc the parent app declared (read from the
// parent's job meta).
func (s *Server) createPreview(ctx context.Context, app, branch string) (*api.Preview, error) {
	src := filepath.Join(s.cfg.SourcesDir, app)
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("no uploaded source for parent app %q — run `blob deploy` for it first", app)
	}
	parentMeta, ok := s.loadJobMeta(app)
	if !ok {
		return nil, fmt.Errorf("parent app %q has no job meta — deploy it first", app)
	}
	previewApp := previewJobName(app, branch)
	domain := previewApp + "." + s.cfg.BaseDomain

	// Read the parent's blob.yaml from the source tarball so we get the
	// full shape (port, env, secrets, services, command, ...). The CLI
	// is what normally loads this and packs it into DeployRequest, but
	// we don't go through the CLI for previews.
	req := &api.DeployRequest{
		App:         previewApp,
		Environment: "prod",
		Form:        parentMeta.Form,
		Domain:      domain,
	}
	if m, err := manifest.Load(filepath.Join(src, "blob.yaml")); err == nil {
		// For multi-component apps we deploy only the first component as
		// a preview — keeps the URL shape simple. Operators with App
		// manifests can drive multi-component previews as a follow-up
		// once the simple shape is proven.
		c := m.Component
		if m.IsApp() && len(m.Components) > 0 {
			c = m.Components[0]
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
	} else {
		stdLog("preview %s/%s: blob.yaml load failed, using parent jobMeta only: %v", app, branch, err)
	}

	resp, err := s.deployFromSource(ctx, src, req, app)
	if err != nil {
		return nil, err
	}

	p := &api.Preview{
		App:       app,
		Branch:    branch,
		JobID:     previewApp,
		Domain:    domain,
		URL:       "https://" + domain,
		CreatedAt: time.Now(),
	}
	if resp != nil && resp.URL != "" {
		p.URL = resp.URL
	}
	if err := s.savePreview(p); err != nil {
		// Don't unwind the deploy on a sentinel-write failure; just log.
		stdLog("preview save failed for %s/%s: %v (deploy is live anyway)", app, branch, err)
	}
	return p, nil
}

// destroyPreview tears down the synthetic Nomad job and removes the
// sentinel. Idempotent: missing-job is not an error.
func (s *Server) destroyPreview(ctx context.Context, app, branch string) error {
	if !branchRE.MatchString(branch) {
		return errors.New("invalid branch")
	}
	jobName := previewJobName(app, branch)
	if err := s.destroyApp(ctx, jobName); err != nil {
		stdLog("preview destroy %s/%s: %v (continuing)", app, branch, err)
	}
	_ = os.Remove(s.previewPath(app, branch))
	return nil
}
