// Package server implements the blobd HTTP API.
//
// blobd runs on a platform host that already has Nomad, Docker, Traefik,
// and a private container registry. It wraps that substrate with a clean
// agent-friendly API: source upload, multi-form deploy (web-service,
// daemon, job, cronjob, app), secrets, environments, scaling, custom
// domains, and a doctor.
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
	urlpkg "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/darvell/blob/internal/api"
	"github.com/darvell/blob/internal/secrets"
)

type Config struct {
	Listen           string
	Token            string
	BaseDomain       string
	Datacenter       string
	Registry         string
	StateDir         string
	JobsDir          string
	SourcesDir       string
	SecretsDir       string
	SecretKey        string
	NomadAddr        string
	RegistryCreds    string
	PlatformPublicIP string // optional; used when attaching user-external domains so we can print the correct A record
}

func DefaultConfig() Config {
	return Config{
		Listen:        ":8787",
		BaseDomain:    "irrigate.cc",
		Datacenter:    "pve",
		Registry:      "registry.irrigate.cc",
		StateDir:      "/srv/blob",
		JobsDir:       "/srv/blob/jobs",
		SourcesDir:    "/srv/blob/sources",
		SecretsDir:    "/srv/blob/secrets",
		SecretKey:     "/etc/blob/secret-key",
		NomadAddr:     "http://127.0.0.1:4646",
		RegistryCreds: "/etc/blob/registry-credentials.txt",
	}
}

type Server struct {
	cfg       Config
	secrets   *secrets.Store
	scheduler *Scheduler
	mu        sync.Mutex
}

func New(cfg Config) (*Server, error) {
	store, err := secrets.New(cfg.SecretsDir, cfg.SecretKey)
	if err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}
	s := &Server{cfg: cfg, secrets: store}
	s.scheduler = newScheduler(s)
	s.scheduler.Start()
	return s, nil
}

// Close releases resources held by the server (currently the scheduler goroutine).
func (s *Server) Close() {
	if s.scheduler != nil {
		s.scheduler.Stop()
	}
}

// removeIgnoringMissing deletes a file, returning nil if the file is already gone.
func removeIgnoringMissing(path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("/v1/whoami", s.handleWhoAmI)
	mux.HandleFunc("/v1/sources/", s.handleUploadSource)
	mux.HandleFunc("/v1/deploy", s.handleDeploy)
	mux.HandleFunc("/v1/deploy-image", s.handleDeployImage)
	mux.HandleFunc("/v1/deploy-app", s.handleDeployApp)
	mux.HandleFunc("/v1/apps", s.handleApps)
	mux.HandleFunc("/v1/apps/", s.handleAppItem)
	mux.HandleFunc("/v1/secrets", s.handleSecrets)
	mux.HandleFunc("/v1/secrets/", s.handleSecretItem)
	mux.HandleFunc("/v1/doctor", s.handleDoctor)
	mux.HandleFunc("/v1/nodes", s.handleNodes)
	mux.HandleFunc("/v1/nodes/", s.handleNodeItem)
	mux.HandleFunc("/v1/join", s.handleJoin)
	mux.HandleFunc("/v1/volumes", s.handleVolumes)
	mux.HandleFunc("/v1/postgres", s.handlePostgres)
	mux.HandleFunc("/v1/postgres/", s.handlePostgresItem)
	mux.HandleFunc("/v1/valkey", s.handleValkey)
	mux.HandleFunc("/v1/valkey/", s.handleValkeyItem)
	mux.HandleFunc("/v1/loki", s.handleLoki)
	mux.HandleFunc("/v1/loki/", s.handleLokiItem)
	mux.HandleFunc("/v1/grafana", s.handleGrafana)
	mux.HandleFunc("/v1/grafana/", s.handleGrafanaItem)
	mux.HandleFunc("/v1/promtail", s.handlePromtail)
	mux.HandleFunc("/v1/promtail/", s.handlePromtailItem)
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
var envRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

func validName(s string) bool { return nameRE.MatchString(s) }
func validEnv(s string) bool  { return s == "" || envRE.MatchString(s) }

// jobID returns the Nomad job ID for an app/component/environment combination.
// prod is bare (`<app>` or `<app>-<component>`); other environments append `-env-<name>`.
func jobID(app, env, component string) string {
	id := app
	if component != "" && component != app {
		id = app + "-" + component
	}
	if env != "" && env != "prod" {
		id = id + "-env-" + env
	}
	return id
}

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

// --- deploy handlers ---------------------------------------------------------

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
	if !validEnv(req.Environment) {
		writeErr(w, 400, "invalid environment")
		return
	}
	src := filepath.Join(s.cfg.SourcesDir, req.App)
	if _, err := os.Stat(src); err != nil {
		writeErr(w, 400, "no source uploaded for "+req.App)
		return
	}
	out, err := s.deployFromSource(r.Context(), src, &req, req.App)
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
	if !validEnv(req.Environment) {
		writeErr(w, 400, "invalid environment")
		return
	}
	if req.Tag == "" {
		writeErr(w, 400, "image (tag) is required")
		return
	}
	if req.Form == "" || req.Form == "web-service" {
		if req.Port <= 0 {
			writeErr(w, 400, "port is required for web-service")
			return
		}
	}
	out, err := s.deployImage(r.Context(), &req, req.App)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleDeployApp(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeErr(w, 405, "method not allowed")
		return
	}
	var req api.DeployAppRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "decode: "+err.Error())
		return
	}
	if !validName(req.App) {
		writeErr(w, 400, "invalid app name")
		return
	}
	if !validEnv(req.Environment) {
		writeErr(w, 400, "invalid environment")
		return
	}
	if len(req.Components) == 0 {
		writeErr(w, 400, "components: at least one component required")
		return
	}
	resp := &api.DeployAppResponse{App: req.App}
	for i := range req.Components {
		c := &req.Components[i]
		if c.Environment == "" {
			c.Environment = req.Environment
		}
		if !validName(c.App) {
			writeErr(w, 400, "component name invalid: "+c.App)
			return
		}
		var cdr *api.DeployResponse
		var err error
		if c.Tag != "" {
			// component used a pre-built image
			cdr, err = s.deployImage(r.Context(), c, req.App)
		} else {
			src := filepath.Join(s.cfg.SourcesDir, req.App)
			if _, err2 := os.Stat(src); err2 != nil {
				writeErr(w, 400, "no source uploaded for "+req.App)
				return
			}
			cdr, err = s.deployFromSource(r.Context(), src, c, req.App)
		}
		if err != nil {
			writeErr(w, 500, "component "+c.App+": "+err.Error())
			return
		}
		resp.Components = append(resp.Components, *cdr)
	}
	writeJSON(w, 200, resp)
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
		opts := logsOptions{
			Lines: n,
			Since: r.URL.Query().Get("since"),
			Grep:  r.URL.Query().Get("grep"),
		}
		lines, source, err := s.appLogs(r.Context(), app, opts)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, api.LogsResponse{App: app, Lines: lines, Source: source})
	case len(parts) == 2 && parts[1] == "scale" && r.Method == "POST":
		var sr api.ScaleRequest
		if err := json.NewDecoder(r.Body).Decode(&sr); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if sr.Replicas < 0 {
			writeErr(w, 400, "replicas must be >= 0")
			return
		}
		if err := s.scaleApp(r.Context(), app, sr.Replicas); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"app": app, "replicas": sr.Replicas})
	case len(parts) == 2 && parts[1] == "restart" && r.Method == "POST":
		if err := s.restartApp(r.Context(), app); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"app": app, "restarted": true})
	case len(parts) == 2 && parts[1] == "releases" && r.Method == "GET":
		out, err := s.appReleases(r.Context(), app)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case len(parts) == 2 && parts[1] == "domains" && r.Method == "POST":
		var dr api.DomainAttachRequest
		if err := json.NewDecoder(r.Body).Decode(&dr); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		dr.App = app
		out, err := s.attachDomain(r.Context(), &dr)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case len(parts) == 2 && parts[1] == "exec" && r.Method == "POST":
		var er api.ExecRequest
		if err := json.NewDecoder(r.Body).Decode(&er); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.appExec(r.Context(), app, er.Command)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 404, "not found")
	}
}

// --- deploy implementation ----------------------------------------------------

type buildResult struct {
	Image string
	Port  int
}

func (s *Server) deployFromSource(ctx context.Context, src string, req *api.DeployRequest, sourceApp string) (*api.DeployResponse, error) {
	out := &api.DeployResponse{App: req.App, Environment: req.Environment, StartedAt: time.Now()}
	id := jobID(req.App, req.Environment, "")
	form := req.Form
	if form == "" {
		form = "web-service"
	}
	domain := req.Domain
	if isHTTPForm(form) {
		if domain == "" {
			if req.Environment != "" && req.Environment != "prod" {
				domain = id + "." + s.cfg.BaseDomain
			} else {
				domain = req.App + "." + s.cfg.BaseDomain
			}
		}
		out.Domain = domain
		out.URL = "https://" + domain
	}

	tag := req.Tag
	if tag == "" {
		tag = strconv.FormatInt(time.Now().Unix(), 10)
	}
	image := fmt.Sprintf("%s/%s:%s", s.cfg.Registry, sourceApp, tag)

	if err := s.recordPhase(out, "registry-login", func() error { return s.dockerLoginRegistry(ctx) }); err != nil {
		return nil, err
	}

	br := buildResult{Image: image, Port: req.Port}
	if err := s.recordPhase(out, "build", func() error {
		built, err := s.buildSource(ctx, src, image, req.Port, req)
		if err != nil {
			return err
		}
		br = built
		return nil
	}); err != nil {
		return nil, err
	}
	if br.Port == 0 && form == "web-service" {
		return nil, errors.New("could not detect a port; set port in blob.yaml or pass --port")
	}

	if err := s.recordPhase(out, "push", func() error {
		return s.run(ctx, "docker", "push", br.Image)
	}); err != nil {
		return nil, err
	}

	if err := s.resolveSecrets(req); err != nil {
		return nil, err
	}
	if err := s.resolveServices(req); err != nil {
		return nil, err
	}

	if err := s.recordPhase(out, "schedule", func() error {
		return s.scheduleJob(ctx, req, br.Image, br.Port, domain, id)
	}); err != nil {
		return nil, err
	}
	out.Image = br.Image
	out.JobID = id

	if isLongRunningForm(form) {
		if err := s.recordPhase(out, "ready", func() error { return s.waitJobRunning(ctx, id, 60*time.Second) }); err != nil {
			return out, fmt.Errorf("did not become ready: %w", err)
		}
	}
	return out, nil
}

func isHTTPForm(form string) bool   { return form == "web-service" || form == "static" }
func isLongRunningForm(f string) bool { return f == "web-service" || f == "daemon" || f == "static" }

func (s *Server) deployImage(ctx context.Context, req *api.DeployRequest, sourceApp string) (*api.DeployResponse, error) {
	out := &api.DeployResponse{App: req.App, Environment: req.Environment, StartedAt: time.Now()}
	id := jobID(req.App, req.Environment, "")
	form := req.Form
	if form == "" {
		form = "web-service"
	}
	domain := req.Domain
	if isHTTPForm(form) {
		if domain == "" {
			if req.Environment != "" && req.Environment != "prod" {
				domain = id + "." + s.cfg.BaseDomain
			} else {
				domain = req.App + "." + s.cfg.BaseDomain
			}
		}
		out.Domain = domain
		out.URL = "https://" + domain
	}
	image := req.Tag
	if !strings.Contains(image, "/") {
		image = s.cfg.Registry + "/" + image
	}
	out.Image = image
	if err := s.resolveSecrets(req); err != nil {
		return nil, err
	}
	if err := s.resolveServices(req); err != nil {
		return nil, err
	}
	if err := s.recordPhase(out, "schedule", func() error {
		return s.scheduleJob(ctx, req, image, req.Port, domain, id)
	}); err != nil {
		return nil, err
	}
	out.JobID = id
	if isLongRunningForm(form) {
		if err := s.recordPhase(out, "ready", func() error { return s.waitJobRunning(ctx, id, 60*time.Second) }); err != nil {
			return out, fmt.Errorf("did not become ready: %w", err)
		}
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

func (s *Server) resolveSecrets(req *api.DeployRequest) error {
	if len(req.Secrets) == 0 {
		return nil
	}
	if req.Env == nil {
		req.Env = map[string]string{}
	}
	for _, sb := range req.Secrets {
		v, err := s.secrets.Get(req.Environment, sb.Name)
		if err != nil {
			return fmt.Errorf("secret %q (env=%q) not found", sb.Name, req.Environment)
		}
		req.Env[sb.Env] = v
	}
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

func (s *Server) buildSource(ctx context.Context, src, image string, portArg int, req *api.DeployRequest) (buildResult, error) {
	br := buildResult{Image: image, Port: portArg}
	if req != nil && req.Form == "static" {
		if err := s.prepareStaticBuild(ctx, src, req); err != nil {
			return br, err
		}
		if err := s.run(ctx, "docker", "build", "-t", image, "-f", filepath.Join(src, "Dockerfile.blob-static"), src); err != nil {
			return br, err
		}
		br.Port = 8080
		return br, nil
	}
	for _, n := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		p := filepath.Join(src, n)
		if _, err := os.Stat(p); err == nil {
			return s.buildCompose(ctx, src, p, image, portArg)
		}
	}
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

func (s *Server) scheduleJob(ctx context.Context, req *api.DeployRequest, image string, port int, domain, id string) error {
	if err := os.MkdirAll(s.cfg.JobsDir, 0o755); err != nil {
		return err
	}
	hcl := renderJob(req, image, port, domain, s.cfg.Datacenter, id)
	jobPath := filepath.Join(s.cfg.JobsDir, id+".nomad")
	if err := os.WriteFile(jobPath, []byte(hcl), 0o644); err != nil {
		return err
	}
	// Side-by-side metadata: form, environment, domain. Used by listApps/status
	// because the Nomad API alone can't distinguish web-service from daemon.
	meta := jobMeta{
		ID:          id,
		App:         req.App,
		Environment: req.Environment,
		Form:        req.Form,
		Domain:      domain,
		Image:       image,
		UpdatedAt:   time.Now(),
	}
	if meta.Form == "" {
		meta.Form = "web-service"
	}
	mb, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(filepath.Join(s.cfg.JobsDir, id+".meta.json"), mb, 0o644)
	return s.run(ctx, "nomad", "job", "run", jobPath)
}

type jobMeta struct {
	ID          string    `json:"id"`
	App         string    `json:"app"`
	Environment string    `json:"environment"`
	Form        string    `json:"form"`
	Domain      string    `json:"domain"`
	Image       string    `json:"image"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (s *Server) loadJobMeta(id string) (jobMeta, bool) {
	b, err := os.ReadFile(filepath.Join(s.cfg.JobsDir, id+".meta.json"))
	if err != nil {
		return jobMeta{}, false
	}
	var m jobMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return jobMeta{}, false
	}
	return m, true
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
	Periodic   bool   `json:"Periodic"`
	ParentID   string `json:"ParentID"`
	JobSummary struct {
		Summary map[string]struct {
			Running, Starting, Failed, Complete int
		} `json:"Summary"`
	} `json:"JobSummary"`
}

func formFromNomadJob(j nomadJob, hasTraefik bool) string {
	switch j.Type {
	case "batch":
		if j.Periodic {
			return "cronjob"
		}
		return "job"
	case "service":
		// Without metadata we cannot definitively tell daemon from web-service.
		// Default to web-service because:
		//   1. Most service-type Nomad jobs in this platform are routed via Traefik.
		//   2. New deploys always write a meta file with the correct form.
		//   3. The Replicas count and URL probe in the client clearly show
		//      whether routing actually works.
		return "web-service"
	}
	return j.Type
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
		"edge-traefik": {}, "blobd-edge": {}, "registry": {},
	}
	var apps []api.AppSummary
	for _, j := range jobs {
		if _, hide := hidden[j.ID]; hide {
			continue
		}
		// Periodic batch instances: skip — the parent cronjob is shown instead.
		if j.ParentID != "" {
			continue
		}
		running := 0
		for _, g := range j.JobSummary.Summary {
			running += g.Running
		}
		appName, env := splitJobID(j.ID)
		domain := appName + "." + s.cfg.BaseDomain
		if env != "" {
			domain = j.ID + "." + s.cfg.BaseDomain
		}
		form := ""
		var image string
		if meta, ok := s.loadJobMeta(j.ID); ok {
			form = meta.Form
			image = meta.Image
			if meta.Domain != "" {
				domain = meta.Domain
			}
		}
		if form == "" {
			form = formFromNomadJob(j, false)
		}
		url := ""
		if isHTTPForm(form) {
			url = "https://" + domain
		}
		apps = append(apps, api.AppSummary{
			App:         j.ID,
			Environment: env,
			Domain:      domain,
			Image:       image,
			URL:         url,
			Status:      j.Status,
			Form:        form,
			Replicas:    running,
		})
	}
	return apps, nil
}

func splitJobID(id string) (app, env string) {
	if i := strings.Index(id, "-env-"); i >= 0 {
		return id[:i], id[i+len("-env-"):]
	}
	return id, ""
}

func (s *Server) appStatus(ctx context.Context, app string) (*api.StatusResponse, error) {
	body, err := s.nomadGET(ctx, "/v1/job/"+app)
	if err != nil {
		return nil, errors.New("no such app")
	}
	var job struct {
		ID, Status, Type string
		Periodic         *struct{} `json:"Periodic,omitempty"`
	}
	_ = json.Unmarshal(body, &job)
	form := ""
	var domain, image string
	if meta, ok := s.loadJobMeta(app); ok {
		form = meta.Form
		domain = meta.Domain
		image = meta.Image
	}
	if form == "" {
		form = formFromNomadJob(nomadJob{Type: job.Type, Periodic: job.Periodic != nil}, false)
	}
	if domain == "" {
		appName, env := splitJobID(app)
		if env != "" {
			domain = app + "." + s.cfg.BaseDomain
		} else {
			domain = appName + "." + s.cfg.BaseDomain
		}
	}
	url := ""
	if isHTTPForm(form) {
		url = "https://" + domain
	}
	resp := &api.StatusResponse{
		AppSummary: api.AppSummary{
			App:    app,
			Domain: domain,
			URL:    url,
			Image:  image,
			Form:   form,
			Status: job.Status,
		},
	}
	allocBody, err := s.nomadGET(ctx, "/v1/job/"+app+"/allocations")
	if err == nil {
		var allocs []struct {
			ID, NodeID, ClientStatus string
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

// logsOptions controls how appLogs assembles the response. Loki is
// preferred when at least one Loki instance is registered AND --since or
// --grep was supplied; the nomad-tail path is used otherwise (preserves
// the v0.1 behavior on a brand-new cluster).
type logsOptions struct {
	Lines int
	Since string // "5m", "2h", "30s" — passed to Loki as start=now-<since>
	Grep  string // substring filter; pushed into the LogQL query as |~ "<grep>"
}

func (s *Server) appLogs(ctx context.Context, app string, opts logsOptions) (lines []string, source string, err error) {
	// Prefer Loki when registered and a since/grep filter is requested.
	// Without a filter, the historical-tail story is the same as nomad's
	// `alloc logs -tail`, so we keep the cheap path.
	useLoki := (opts.Since != "" || opts.Grep != "")
	if useLoki {
		if base := s.firstLokiBase(); base != "" {
			lines, lerr := s.queryLokiLogs(ctx, base, app, opts)
			if lerr == nil {
				return lines, "loki", nil
			}
			stdLog("loki query failed for %s: %v (falling back to nomad tail)", app, lerr)
		}
	}
	body, err := s.nomadGET(ctx, "/v1/job/"+app+"/allocations")
	if err != nil {
		return nil, "", err
	}
	var allocs []struct {
		ID, ClientStatus string
	}
	if err := json.Unmarshal(body, &allocs); err != nil {
		return nil, "", err
	}
	for _, a := range allocs {
		if a.ClientStatus != "running" {
			continue
		}
		out := s.output(ctx, "nomad", "alloc", "logs", "-tail", "-n", strconv.Itoa(opts.Lines), a.ID)
		split := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if opts.Grep != "" {
			filtered := split[:0]
			for _, l := range split {
				if strings.Contains(l, opts.Grep) {
					filtered = append(filtered, l)
				}
			}
			split = filtered
		}
		return split, "nomad", nil
	}
	return []string{"(no running allocation)"}, "nomad", nil
}

// appAllocIDs returns every allocation ID for the named app (running OR
// recently-stopped, so historical-window queries still resolve). Empty
// list if the job doesn't exist.
func (s *Server) appAllocIDs(ctx context.Context, app string) ([]string, error) {
	body, err := s.nomadGET(ctx, "/v1/job/"+app+"/allocations")
	if err != nil {
		return nil, err
	}
	var allocs []struct {
		ID string
	}
	if err := json.Unmarshal(body, &allocs); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(allocs))
	for _, a := range allocs {
		if a.ID != "" {
			out = append(out, a.ID)
		}
	}
	return out, nil
}

// firstLokiBase returns the http://host:port of the first registered Loki
// instance, or "" if none are configured.
func (s *Server) firstLokiBase() string {
	entries, err := os.ReadDir(s.lokiMetaDir())
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		m, err := s.loadLoki(strings.TrimSuffix(e.Name(), ".json"))
		if err == nil {
			return fmt.Sprintf("http://%s:%d", s.postgresHost(), m.Port)
		}
	}
	return ""
}

// queryLokiLogs hits Loki's /loki/api/v1/query_range. The Nomad docker
// driver names the task generically inside the job ("app", "web", etc.)
// so the app's identity lives in the alloc id, not the task name. We
// resolve the app's running allocations via Nomad and query Loki with
// {alloc=~"<id1>|<id2>|..."} so a single LogQL call returns just this app.
func (s *Server) queryLokiLogs(ctx context.Context, base, app string, opts logsOptions) ([]string, error) {
	allocIDs, err := s.appAllocIDs(ctx, app)
	if err != nil {
		return nil, fmt.Errorf("resolve allocs for %s: %w", app, err)
	}
	if len(allocIDs) == 0 {
		return nil, fmt.Errorf("no allocations for app %q", app)
	}
	q := fmt.Sprintf(`{job="nomad-alloc",alloc=~%q}`, strings.Join(allocIDs, "|"))
	if opts.Grep != "" {
		safe := strings.ReplaceAll(opts.Grep, `"`, `\"`)
		q = q + ` |~ "` + safe + `"`
	}
	since := opts.Since
	if since == "" {
		since = "1h"
	}
	dur, err := time.ParseDuration(since)
	if err != nil {
		return nil, fmt.Errorf("invalid since %q: %w", since, err)
	}
	end := time.Now().UTC()
	start := end.Add(-dur)
	limit := opts.Lines
	if limit <= 0 {
		limit = 200
	}
	u := fmt.Sprintf("%s/loki/api/v1/query_range?query=%s&start=%d&end=%d&limit=%d&direction=BACKWARD",
		base,
		urlpkg.QueryEscape(q),
		start.UnixNano(),
		end.UnixNano(),
		limit,
	)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("loki query: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out struct {
		Data struct {
			Result []struct {
				Stream map[string]string `json:"stream"`
				Values [][2]string       `json:"values"` // [ns-string, line]
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	type stamped struct {
		ts   int64
		line string
	}
	var rows []stamped
	for _, r := range out.Data.Result {
		for _, v := range r.Values {
			ns, _ := strconv.ParseInt(v[0], 10, 64)
			rows = append(rows, stamped{ts: ns, line: v[1]})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ts < rows[j].ts })
	lines := make([]string, len(rows))
	for i, r := range rows {
		t := time.Unix(0, r.ts).UTC().Format(time.RFC3339)
		lines[i] = t + " " + r.line
	}
	return lines, nil
}

func (s *Server) destroyApp(ctx context.Context, app string) error {
	if err := s.run(ctx, "nomad", "job", "stop", "-purge", app); err != nil {
		return err
	}
	jobPath := filepath.Join(s.cfg.JobsDir, app+".nomad")
	_ = os.Remove(jobPath)
	_ = os.Remove(filepath.Join(s.cfg.JobsDir, app+".meta.json"))
	_ = os.RemoveAll(filepath.Join(s.cfg.SourcesDir, app))
	return nil
}

func (s *Server) scaleApp(ctx context.Context, app string, replicas int) error {
	// Use Nomad's scaling API; for now we use the CLI which is stable.
	return s.run(ctx, "nomad", "job", "scale", app, strconv.Itoa(replicas))
}

// --- secrets handlers --------------------------------------------------------

func (s *Server) handleSecrets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		env := r.URL.Query().Get("environment")
		if env == "" {
			env = "prod"
		}
		metas, err := s.secrets.List(env)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		out := api.ListSecretsResponse{}
		for _, m := range metas {
			out.Secrets = append(out.Secrets, api.Secret{Name: m.Name, Environment: m.Env, UpdatedAt: m.UpdatedAt, Length: m.Length})
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.SetSecretRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if !validName(req.Name) {
			writeErr(w, 400, "invalid secret name")
			return
		}
		if !validEnv(req.Environment) {
			writeErr(w, 400, "invalid environment")
			return
		}
		if err := s.secrets.Set(req.Environment, req.Name, req.Value); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"name": req.Name, "environment": req.Environment, "ok": true})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleSecretItem(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/v1/secrets/")
	if !validName(name) {
		writeErr(w, 400, "invalid name")
		return
	}
	env := r.URL.Query().Get("environment")
	if !validEnv(env) {
		writeErr(w, 400, "invalid environment")
		return
	}
	switch r.Method {
	case "DELETE":
		if err := s.secrets.Delete(env, name); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"name": name, "environment": env, "deleted": true})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

// --- doctor ------------------------------------------------------------------

func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeErr(w, 405, "method not allowed")
		return
	}
	out := s.runDoctor(r.Context())
	writeJSON(w, 200, out)
}

func (s *Server) runDoctor(ctx context.Context) api.DoctorResponse {
	resp := api.DoctorResponse{}
	add := func(sev, cat, app, title, detail, fix string) {
		resp.Issues = append(resp.Issues, api.DoctorIssue{Severity: sev, Category: cat, App: app, Title: title, Detail: detail, Remediate: fix})
	}

	// 1. registry credentials readable?
	resp.Checked++
	if _, _, err := s.readRegistryCreds(); err != nil {
		add("P1", "registry", "", "registry credentials unreadable", err.Error(),
			fmt.Sprintf("Ensure %s is owned by the blobd user and contains username:/password: lines", s.cfg.RegistryCreds))
	}

	// 2. Nomad reachable?
	resp.Checked++
	if _, err := s.nomadGET(ctx, "/v1/agent/self"); err != nil {
		add("P1", "nomad", "", "Nomad API unreachable", err.Error(),
			"Ensure Nomad is running and NomadAddr is correct")
	}

	// 3. job statuses (skip periodic instances — they exit dead by design)
	resp.Checked++
	if body, err := s.nomadGET(ctx, "/v1/jobs"); err == nil {
		var jobs []nomadJob
		if json.Unmarshal(body, &jobs) == nil {
			hidden := map[string]struct{}{"edge-traefik": {}, "blobd-edge": {}, "registry": {}}
			for _, j := range jobs {
				if _, h := hidden[j.ID]; h {
					continue
				}
				if j.ParentID != "" {
					continue
				}
				if j.Status == "dead" {
					// A non-periodic batch job that has run to completion is fine.
					if j.Type == "batch" {
						continue
					}
					add("P1", "nomad", j.ID, "job is dead", "",
						fmt.Sprintf("`blob logs %s -n 200` to inspect failure, then `blob deploy` to redeploy or `blob destroy %s`", j.ID, j.ID))
				} else if j.Status == "pending" {
					add("P2", "nomad", j.ID, "job is pending placement", "", "Check fleet capacity and node eligibility")
				}
			}
		}
	}

	// 4. orphan job files (no matching Nomad job)
	resp.Checked++
	if entries, err := os.ReadDir(s.cfg.JobsDir); err == nil {
		running := map[string]bool{}
		if body, err := s.nomadGET(ctx, "/v1/jobs"); err == nil {
			var jobs []nomadJob
			_ = json.Unmarshal(body, &jobs)
			for _, j := range jobs {
				running[j.ID] = true
			}
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".nomad") {
				continue
			}
			id := strings.TrimSuffix(e.Name(), ".nomad")
			if !running[id] {
				add("P3", "drift", id, "stale job file on disk", filepath.Join(s.cfg.JobsDir, e.Name()),
					"Run `blob destroy "+id+"` if no longer wanted; otherwise re-deploy")
			}
		}
	}

	// 5. orphan source dirs — a source belongs to any deployed job whose
	// source-app prefix matches. App deploys land as <app>-<component>, so
	// the source dir is "blob-app" and jobs are "blob-app-web", "blob-app-worker".
	resp.Checked++
	if entries, err := os.ReadDir(s.cfg.SourcesDir); err == nil {
		var jobIDs []string
		if body, err := s.nomadGET(ctx, "/v1/jobs"); err == nil {
			var jobs []nomadJob
			_ = json.Unmarshal(body, &jobs)
			for _, j := range jobs {
				if j.ParentID != "" {
					continue
				}
				jobIDs = append(jobIDs, j.ID)
			}
		}
		hasUser := func(srcName string) bool {
			for _, id := range jobIDs {
				if id == srcName || strings.HasPrefix(id, srcName+"-") || strings.HasPrefix(id, srcName+"-env-") {
					return true
				}
			}
			return false
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if !hasUser(e.Name()) {
				add("info", "drift", e.Name(), "uploaded source for non-existent app",
					filepath.Join(s.cfg.SourcesDir, e.Name()), "Safe to delete; will be re-uploaded on next deploy")
			}
		}
	}

	return resp
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
