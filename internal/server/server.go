// Package server implements the blobd HTTP API.
//
// It runs on the platform host and wraps the existing Nomad/Docker/Traefik
// pipeline. Source uploads are unpacked into /srv/blob/sources/<app>, built
// with `docker build` (or compose), pushed to the registry, then submitted
// as a Nomad job with Traefik routing tags.
package server

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/darvell/blob/internal/api"
)

type Config struct {
	Listen          string
	Token           string // bearer token for API access
	BaseDomain      string // e.g. irrigate.cc
	Datacenter      string // Nomad DC, e.g. pve
	Registry        string // e.g. registry.irrigate.cc
	StateDir        string // /srv/blob
	JobsDir         string // /srv/blob/jobs
	SourcesDir      string // /srv/blob/sources
	NomadAddr       string // http://127.0.0.1:4646
	BuilderUser     string // user owning /srv/blob (defaults to current)
	RegistryCreds   string // path to credentials file (key: username/password)
	HostBuildOnLoad bool   // build images on this host
}

func DefaultConfig() Config {
	return Config{
		Listen:          ":8787",
		BaseDomain:      "irrigate.cc",
		Datacenter:      "pve",
		Registry:        "registry.irrigate.cc",
		StateDir:        "/srv/blob",
		JobsDir:         "/srv/blob/jobs",
		SourcesDir:      "/srv/blob/sources",
		NomadAddr:       "http://127.0.0.1:4646",
		RegistryCreds:   "/etc/blob/registry-credentials.txt",
		HostBuildOnLoad: true,
	}
}

type Server struct {
	cfg Config
	mu  sync.Mutex
}

func New(cfg Config) *Server {
	return &Server{cfg: cfg}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/whoami", s.handleWhoAmI)
	mux.HandleFunc("/v1/sources/", s.handleUploadSource)
	mux.HandleFunc("/v1/deploy", s.handleDeploy)
	mux.HandleFunc("/v1/deploy-image", s.handleDeployImage)
	mux.HandleFunc("/v1/apps", s.handleApps)
	mux.HandleFunc("/v1/apps/", s.handleAppItem)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	return s.withAuth(mux)
}

func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if s.cfg.Token == "" {
			next.ServeHTTP(w, r)
			return
		}
		ah := r.Header.Get("Authorization")
		if !strings.HasPrefix(ah, "Bearer ") || subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(ah, "Bearer ")), []byte(s.cfg.Token)) != 1 {
			writeErr(w, http.StatusUnauthorized, "invalid token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, api.ErrorBody{Error: msg})
}

func (s *Server) handleWhoAmI(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	writeJSON(w, 200, api.WhoAmI{Name: host, OK: true})
}

var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func validName(s string) bool { return nameRE.MatchString(s) }

func (s *Server) handleUploadSource(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeErr(w, 405, "method not allowed")
		return
	}
	app := strings.TrimPrefix(r.URL.Path, "/v1/sources/")
	if !validName(app) {
		writeErr(w, 400, "invalid app name")
		return
	}
	dest := filepath.Join(s.cfg.SourcesDir, app)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	tmp := dest + ".incoming"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := untar(tmp, r.Body); err != nil {
		writeErr(w, 400, "tar: "+err.Error())
		return
	}
	_ = os.RemoveAll(dest)
	if err := os.Rename(tmp, dest); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"app": app, "path": dest})
}

func untar(dest string, r io.Reader) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return fmt.Errorf("unsafe tar path: %q", hdr.Name)
		}
		out := filepath.Join(dest, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(out, os.FileMode(hdr.Mode)|0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777|0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		case tar.TypeSymlink:
			_ = os.Symlink(hdr.Linkname, out)
		}
	}
	return nil
}

func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeErr(w, 405, "method not allowed")
		return
	}
	var req api.DeployRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "decode: "+err.Error())
		return
	}
	if !validName(req.App) {
		writeErr(w, 400, "invalid app name")
		return
	}
	src := filepath.Join(s.cfg.SourcesDir, req.App)
	if _, err := os.Stat(src); err != nil {
		writeErr(w, 400, "no source uploaded for "+req.App)
		return
	}
	out, err := s.deployFromSource(r.Context(), src, &req)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleDeployImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeErr(w, 405, "method not allowed")
		return
	}
	var req api.DeployRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "decode: "+err.Error())
		return
	}
	if !validName(req.App) {
		writeErr(w, 400, "invalid app name")
		return
	}
	if req.Tag == "" {
		writeErr(w, 400, "image (tag) is required for deploy-image")
		return
	}
	if req.Port <= 0 {
		writeErr(w, 400, "port is required for deploy-image")
		return
	}
	out, err := s.deployImage(r.Context(), &req)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeErr(w, 405, "method not allowed")
		return
	}
	apps, err := s.listApps(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, api.ListResponse{Apps: apps})
}

func (s *Server) handleAppItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/apps/")
	parts := strings.SplitN(rest, "/", 2)
	app := parts[0]
	if !validName(app) {
		writeErr(w, 400, "invalid app name")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == "GET":
		out, err := s.appStatus(r.Context(), app)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case len(parts) == 1 && r.Method == "DELETE":
		if err := s.destroyApp(r.Context(), app); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"app": app, "destroyed": true})
	case len(parts) == 2 && parts[1] == "logs" && r.Method == "GET":
		n, _ := strconv.Atoi(r.URL.Query().Get("lines"))
		if n <= 0 {
			n = 200
		}
		lines, err := s.appLogs(r.Context(), app, n)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, api.LogsResponse{App: app, Lines: lines})
	default:
		writeErr(w, 404, "not found")
	}
}

// --- deploy implementation ----------------------------------------------------

type buildResult struct {
	Image string
	Port  int
}

func (s *Server) deployFromSource(ctx context.Context, src string, req *api.DeployRequest) (*api.DeployResponse, error) {
	out := &api.DeployResponse{App: req.App, StartedAt: time.Now()}
	domain := req.Domain
	if domain == "" {
		domain = req.App + "." + s.cfg.BaseDomain
	}
	out.Domain = domain
	out.URL = "https://" + domain

	tag := req.Tag
	if tag == "" {
		tag = strconv.FormatInt(time.Now().Unix(), 10)
	}
	image := fmt.Sprintf("%s/%s:%s", s.cfg.Registry, req.App, tag)

	// Phase: registry login
	if err := s.recordPhase(out, "registry-login", func() error { return s.dockerLoginRegistry(ctx) }); err != nil {
		return nil, err
	}

	// Phase: build
	br := buildResult{Image: image, Port: req.Port}
	if err := s.recordPhase(out, "build", func() error {
		built, err := s.buildSource(ctx, src, image, req.Port)
		if err != nil {
			return err
		}
		br = built
		return nil
	}); err != nil {
		return nil, err
	}
	if br.Port == 0 && req.Form == "web-service" {
		return nil, errors.New("could not detect a port; set port in blob.yaml or pass --port")
	}

	// Phase: push
	if err := s.recordPhase(out, "push", func() error {
		return s.run(ctx, "docker", "push", br.Image)
	}); err != nil {
		return nil, err
	}

	// Phase: schedule
	jobID := req.App
	if err := s.recordPhase(out, "schedule", func() error {
		return s.scheduleNomad(ctx, req, br.Image, br.Port, domain)
	}); err != nil {
		return nil, err
	}
	out.Image = br.Image
	out.JobID = jobID

	// Phase: wait
	if err := s.recordPhase(out, "ready", func() error { return s.waitJobRunning(ctx, jobID, 60*time.Second) }); err != nil {
		return out, fmt.Errorf("did not become ready: %w", err)
	}
	return out, nil
}

func (s *Server) deployImage(ctx context.Context, req *api.DeployRequest) (*api.DeployResponse, error) {
	out := &api.DeployResponse{App: req.App, StartedAt: time.Now()}
	domain := req.Domain
	if domain == "" {
		domain = req.App + "." + s.cfg.BaseDomain
	}
	out.Domain = domain
	out.URL = "https://" + domain
	image := req.Tag
	if !strings.Contains(image, "/") {
		image = s.cfg.Registry + "/" + image
	}
	out.Image = image
	if err := s.recordPhase(out, "schedule", func() error {
		return s.scheduleNomad(ctx, req, image, req.Port, domain)
	}); err != nil {
		return nil, err
	}
	out.JobID = req.App
	if err := s.recordPhase(out, "ready", func() error { return s.waitJobRunning(ctx, req.App, 60*time.Second) }); err != nil {
		return out, fmt.Errorf("did not become ready: %w", err)
	}
	return out, nil
}

func (s *Server) recordPhase(out *api.DeployResponse, name string, fn func() error) error {
	t0 := time.Now()
	err := fn()
	d := time.Since(t0)
	p := api.Phase{
		Name:       name,
		DurationMS: d.Milliseconds(),
		OK:         err == nil,
		When:       t0,
	}
	if err != nil {
		p.Note = err.Error()
	}
	out.Phases = append(out.Phases, p)
	if err != nil {
		log.Printf("phase %s failed after %s: %v", name, d, err)
		return fmt.Errorf("%s: %w", name, err)
	}
	log.Printf("phase %s ok in %s", name, d)
	return nil
}

func (s *Server) dockerLoginRegistry(ctx context.Context) error {
	user, pass, err := s.readRegistryCreds()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "docker", "login", s.cfg.Registry, "-u", user, "--password-stdin")
	cmd.Stdin = strings.NewReader(pass)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	return cmd.Run()
}

func (s *Server) readRegistryCreds() (string, string, error) {
	b, err := os.ReadFile(s.cfg.RegistryCreds)
	if err != nil {
		return "", "", fmt.Errorf("registry creds: %w", err)
	}
	var user, pass string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "username:") {
			user = strings.TrimSpace(strings.TrimPrefix(line, "username:"))
		}
		if strings.HasPrefix(line, "password:") {
			pass = strings.TrimSpace(strings.TrimPrefix(line, "password:"))
		}
	}
	if user == "" || pass == "" {
		return "", "", errors.New("registry creds: missing username or password")
	}
	return user, pass, nil
}

func (s *Server) buildSource(ctx context.Context, src, image string, portArg int) (buildResult, error) {
	br := buildResult{Image: image, Port: portArg}
	// Compose first
	for _, n := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		p := filepath.Join(src, n)
		if _, err := os.Stat(p); err == nil {
			return s.buildCompose(ctx, src, p, image, portArg)
		}
	}
	// Dockerfile
	df := filepath.Join(src, "Dockerfile")
	if _, err := os.Stat(df); err == nil {
		if err := s.run(ctx, "docker", "build", "-t", image, src); err != nil {
			return br, err
		}
		return br, nil
	}
	return br, errors.New("no Dockerfile or compose file found")
}

func (s *Server) buildCompose(ctx context.Context, src, composeFile, image string, portArg int) (buildResult, error) {
	br := buildResult{Image: image, Port: portArg}
	// Resolve config to JSON to find the first service.
	cmd := exec.CommandContext(ctx, "docker", "compose", "-f", composeFile, "config", "--format", "json")
	cmd.Dir = src
	var stdout strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return br, fmt.Errorf("compose config: %w", err)
	}
	var config struct {
		Services map[string]struct {
			Ports []struct {
				Target int `json:"target"`
			} `json:"ports"`
			Expose []any  `json:"expose"`
			Image  string `json:"image"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &config); err != nil {
		return br, fmt.Errorf("compose config json: %w", err)
	}
	if len(config.Services) == 0 {
		return br, errors.New("compose has no services")
	}
	svc := "web"
	if _, ok := config.Services[svc]; !ok {
		for k := range config.Services {
			svc = k
			break
		}
	}
	if br.Port == 0 {
		for _, p := range config.Services[svc].Ports {
			if p.Target > 0 {
				br.Port = p.Target
				break
			}
		}
		if br.Port == 0 && len(config.Services[svc].Expose) > 0 {
			switch v := config.Services[svc].Expose[0].(type) {
			case string:
				br.Port, _ = strconv.Atoi(strings.SplitN(v, "/", 2)[0])
			case float64:
				br.Port = int(v)
			}
		}
	}
	if err := s.runIn(ctx, src, "docker", "compose", "-f", composeFile, "build", svc); err != nil {
		return br, err
	}
	imageID := strings.TrimSpace(s.outputIn(ctx, src, "docker", "compose", "-f", composeFile, "images", "-q", svc))
	if imageID == "" {
		return br, fmt.Errorf("could not find built image for compose service %q", svc)
	}
	if err := s.run(ctx, "docker", "tag", imageID, image); err != nil {
		return br, err
	}
	return br, nil
}

func (s *Server) scheduleNomad(ctx context.Context, req *api.DeployRequest, image string, port int, domain string) error {
	if err := os.MkdirAll(s.cfg.JobsDir, 0o755); err != nil {
		return err
	}
	job := renderNomadJob(req.App, image, port, domain, req.CPU, req.Memory, req.Replicas, req.Form, req.Env, s.cfg.Datacenter)
	jobPath := filepath.Join(s.cfg.JobsDir, req.App+".nomad")
	if err := os.WriteFile(jobPath, []byte(job), 0o644); err != nil {
		return err
	}
	return s.run(ctx, "nomad", "job", "run", jobPath)
}

func (s *Server) waitJobRunning(ctx context.Context, jobID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out := s.output(ctx, "nomad", "job", "status", "-short", jobID)
		if strings.Contains(out, "running") {
			return nil
		}
		if strings.Contains(out, "dead") {
			return fmt.Errorf("job %s is dead", jobID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("timeout waiting for %s", jobID)
}

// --- listing / status / logs / destroy ---------------------------------------

func (s *Server) nomadGET(ctx context.Context, path string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(s.cfg.NomadAddr, "/")+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("nomad %s: %s: %s", path, resp.Status, string(body))
	}
	return io.ReadAll(resp.Body)
}

type nomadJob struct {
	ID         string `json:"ID"`
	Type       string `json:"Type"`
	Status     string `json:"Status"`
	ModifyTime int64  `json:"ModifyIndex"`
	JobSummary struct {
		Summary map[string]struct {
			Running  int `json:"Running"`
			Starting int `json:"Starting"`
			Failed   int `json:"Failed"`
		} `json:"Summary"`
	} `json:"JobSummary"`
}

func (s *Server) listApps(ctx context.Context) ([]api.AppSummary, error) {
	body, err := s.nomadGET(ctx, "/v1/jobs")
	if err != nil {
		return nil, err
	}
	var jobs []nomadJob
	if err := json.Unmarshal(body, &jobs); err != nil {
		return nil, fmt.Errorf("parse jobs: %w", err)
	}
	hidden := map[string]struct{}{
		"edge-traefik": {},
		"blobd-edge":   {},
		"registry":     {},
	}
	var apps []api.AppSummary
	for _, j := range jobs {
		if _, hide := hidden[j.ID]; hide {
			continue
		}
		running := 0
		for _, g := range j.JobSummary.Summary {
			running += g.Running
		}
		apps = append(apps, api.AppSummary{
			App:      j.ID,
			Domain:   j.ID + "." + s.cfg.BaseDomain,
			URL:      "https://" + j.ID + "." + s.cfg.BaseDomain,
			Status:   j.Status,
			Form:     "web-service",
			Replicas: running,
		})
	}
	return apps, nil
}

func (s *Server) appStatus(ctx context.Context, app string) (*api.StatusResponse, error) {
	body, err := s.nomadGET(ctx, "/v1/job/"+app)
	if err != nil {
		return nil, errors.New("no such app")
	}
	var job struct {
		ID, Status string
	}
	_ = json.Unmarshal(body, &job)
	resp := &api.StatusResponse{
		AppSummary: api.AppSummary{
			App:    app,
			Domain: app + "." + s.cfg.BaseDomain,
			URL:    "https://" + app + "." + s.cfg.BaseDomain,
			Form:   "web-service",
			Status: job.Status,
		},
	}
	allocBody, err := s.nomadGET(ctx, "/v1/job/"+app+"/allocations")
	if err == nil {
		var allocs []struct {
			ID           string `json:"ID"`
			NodeID       string `json:"NodeID"`
			ClientStatus string `json:"ClientStatus"`
		}
		_ = json.Unmarshal(allocBody, &allocs)
		running := 0
		for _, a := range allocs {
			resp.Allocations = append(resp.Allocations, api.Allocation{
				ID: a.ID, Node: a.NodeID, Status: a.ClientStatus, Health: a.ClientStatus,
			})
			if a.ClientStatus == "running" {
				running++
			}
		}
		resp.Replicas = running
	}
	resp.UpdatedAt = time.Now()
	return resp, nil
}

func (s *Server) appLogs(ctx context.Context, app string, n int) ([]string, error) {
	body, err := s.nomadGET(ctx, "/v1/job/"+app+"/allocations")
	if err != nil {
		return nil, err
	}
	var allocs []struct {
		ID           string `json:"ID"`
		ClientStatus string `json:"ClientStatus"`
	}
	if err := json.Unmarshal(body, &allocs); err != nil {
		return nil, err
	}
	for _, a := range allocs {
		if a.ClientStatus != "running" {
			continue
		}
		out := s.output(ctx, "nomad", "alloc", "logs", "-tail", "-n", strconv.Itoa(n), a.ID)
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		return lines, nil
	}
	return []string{"(no running allocation)"}, nil
}

func (s *Server) destroyApp(ctx context.Context, app string) error {
	if err := s.run(ctx, "nomad", "job", "stop", "-purge", app); err != nil {
		return err
	}
	jobPath := filepath.Join(s.cfg.JobsDir, app+".nomad")
	_ = os.Remove(jobPath)
	_ = os.RemoveAll(filepath.Join(s.cfg.SourcesDir, app))
	return nil
}

// --- exec helpers -------------------------------------------------------------

func (s *Server) run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (s *Server) runIn(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (s *Server) output(ctx context.Context, name string, args ...string) string {
	cmd := exec.CommandContext(ctx, name, args...)
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func (s *Server) outputIn(ctx context.Context, dir, name string, args ...string) string {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()
	return string(out)
}
