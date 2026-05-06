package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/irrigationreal/blob/internal/api"
)

const (
	defaultPluginHookTimeout = 30
	maxPluginHookOutput      = 64 * 1024
	pluginHookPre            = "pre"
	pluginHookPost           = "post"
)

type pluginHookContext struct {
	JobID string
	Image string
	URL   string
}

type cappedHookOutput struct {
	mu        sync.Mutex
	buf       strings.Builder
	remaining int
	truncated bool
}

func newCappedHookOutput(max int) *cappedHookOutput {
	return &cappedHookOutput{remaining: max}
}

func (o *cappedHookOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.remaining <= 0 {
		o.truncated = true
		return len(p), nil
	}
	if len(p) > o.remaining {
		_, _ = o.buf.Write(p[:o.remaining])
		o.remaining = 0
		o.truncated = true
		return len(p), nil
	}
	_, _ = o.buf.Write(p)
	o.remaining -= len(p)
	return len(p), nil
}

func (o *cappedHookOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := o.buf.String()
	if o.truncated {
		out += "\n[plugin hook output truncated]"
	}
	return out
}

func (s *Server) pluginsDir() string {
	return filepath.Join(s.cfg.StateDir, "plugins")
}

func (s *Server) pluginPath(app string) string {
	return filepath.Join(s.pluginsDir(), app+".json")
}

func (s *Server) handlePlugins(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listPlugins()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handlePluginItem(w http.ResponseWriter, r *http.Request) {
	app := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/plugins/"), "/")
	if !validName(app) {
		writeErr(w, 400, "invalid app name")
		return
	}
	switch r.Method {
	case "GET":
		cfg, err := s.loadPlugin(app)
		if err != nil {
			writeErr(w, 404, "no plugin config for app")
			return
		}
		writeJSON(w, 200, cfg)
	case "PUT":
		var req api.SetPluginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		cfg, err := s.setPlugin(app, &req)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, cfg)
	case "DELETE":
		if err := s.deletePlugin(app); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"app": app, "deleted": true})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) listPlugins() (*api.ListPluginsResponse, error) {
	out := &api.ListPluginsResponse{}
	entries, err := os.ReadDir(s.pluginsDir())
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
		cfg, err := s.loadPlugin(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out.Plugins = append(out.Plugins, *cfg)
	}
	sort.Slice(out.Plugins, func(i, j int) bool { return out.Plugins[i].App < out.Plugins[j].App })
	return out, nil
}

func (s *Server) loadPlugin(app string) (*api.PluginConfig, error) {
	b, err := os.ReadFile(s.pluginPath(app))
	if err != nil {
		return nil, err
	}
	cfg := &api.PluginConfig{}
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func normalizePluginHookTimeout(seconds int) (int, error) {
	if seconds <= 0 {
		seconds = defaultPluginHookTimeout
	}
	if seconds > 300 {
		return 0, errors.New("timeout_seconds must be <= 300")
	}
	return seconds, nil
}

func samePluginConfig(a, b *api.PluginConfig) bool {
	return a != nil && b != nil && a.App == b.App && a.Enabled == b.Enabled && a.PreDeploy == b.PreDeploy && a.PostDeploy == b.PostDeploy && a.TimeoutSeconds == b.TimeoutSeconds
}

func (s *Server) setPlugin(app string, req *api.SetPluginRequest) (*api.PluginConfig, error) {
	preDeploy := strings.TrimSpace(req.PreDeploy)
	postDeploy := strings.TrimSpace(req.PostDeploy)
	if preDeploy == "" && postDeploy == "" {
		return nil, errors.New("at least one hook command is required")
	}
	if len(preDeploy) > 1000 || len(postDeploy) > 1000 {
		return nil, errors.New("hook command must be <= 1000 chars")
	}
	timeout, err := normalizePluginHookTimeout(req.TimeoutSeconds)
	if err != nil {
		return nil, err
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	cfg := &api.PluginConfig{
		App:            app,
		Enabled:        enabled,
		PreDeploy:      preDeploy,
		PostDeploy:     postDeploy,
		TimeoutSeconds: timeout,
		UpdatedAt:      time.Now().UTC(),
	}
	if existing, err := s.loadPlugin(app); err == nil && samePluginConfig(existing, cfg) {
		return existing, nil
	}
	if err := os.MkdirAll(s.pluginsDir(), 0o700); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(s.pluginPath(app), b, 0o600); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (s *Server) deletePlugin(app string) error {
	return removeIgnoringMissing(s.pluginPath(app))
}

func (s *Server) runDeployHook(ctx context.Context, phase string, req *api.DeployRequest, hook pluginHookContext) error {
	cfg, err := s.loadPlugin(req.App)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !cfg.Enabled {
		return nil
	}
	cmdline := ""
	switch phase {
	case pluginHookPre:
		cmdline = cfg.PreDeploy
	case pluginHookPost:
		cmdline = cfg.PostDeploy
	default:
		return fmt.Errorf("unknown plugin hook phase %q", phase)
	}
	if strings.TrimSpace(cmdline) == "" {
		return nil
	}
	timeout, err := normalizePluginHookTimeout(cfg.TimeoutSeconds)
	if err != nil {
		return err
	}
	hookCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	cmd := exec.Command("/bin/sh", "-c", cmdline)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(os.Environ(),
		"BLOB_HOOK="+phase,
		"BLOB_APP="+req.App,
		"BLOB_ENVIRONMENT="+req.Environment,
		"BLOB_JOB_ID="+hook.JobID,
		"BLOB_IMAGE="+hook.Image,
		"BLOB_URL="+hook.URL,
	)
	out := newCappedHookOutput(maxPluginHookOutput)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("plugin %s hook start failed: %w", phase, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-hookCtx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		waitErr = <-done
	}
	if hookCtx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("plugin %s hook timed out after %ds", phase, timeout)
	}
	if hookCtx.Err() != nil {
		return hookCtx.Err()
	}
	if waitErr != nil {
		msg := strings.TrimSpace(out.String())
		if msg != "" {
			return fmt.Errorf("plugin %s hook failed: %w: %s", phase, waitErr, msg)
		}
		return fmt.Errorf("plugin %s hook failed: %w", phase, waitErr)
	}
	return nil
}
