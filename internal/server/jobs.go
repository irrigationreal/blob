// Package server: one-shot and scheduled batch jobs (v0.23).
//
// blob jobs run …    → Nomad type=batch, single fire, blocking until
//                       the alloc terminates (or we hit a configured
//                       timeout). Logs are slurped from `nomad alloc
//                       logs` and returned to the caller.
//
// blob jobs schedule → Nomad type=batch with a periodic { cron = … }
//                       block. Each fire creates a child instance whose
//                       logs are addressable by fire index.
//
// Both flows can attach to a parent app (`--app web`) — when set, the
// job's env is seeded with that app's resolved services (the same
// MONGODB_URL/CLICKHOUSE_URL/… your web-service sees). This is the
// whole point of having jobs as a first-class form: they should run
// against the same secrets and bound services as the rest of the app,
// not a parallel universe.
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
	"strconv"
	"strings"
	"time"

	"github.com/darvell/blob/internal/api"
)

func (s *Server) jobsMetaDir() string {
	return filepath.Join(s.cfg.StateDir, "userjobs")
}

// jobMetaUser is the on-disk record for jobs created via the v0.23
// /v1/jobs surface. Distinct from the existing app-level jobMeta
// struct because the job lifecycle is different (one-shot or periodic
// parent vs long-running deploy).
type jobMetaUser struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	App       string            `json:"app,omitempty"`
	Kind      string            `json:"kind"` // "job" | "cronjob"
	Cron      string            `json:"cron,omitempty"`
	Image     string            `json:"image"`
	Command   []string          `json:"command,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	CPU       int               `json:"cpu"`
	Memory    int               `json:"memory"`
	Timeout   int               `json:"timeout,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

func (s *Server) saveUserJob(j *jobMetaUser) error {
	if err := os.MkdirAll(s.jobsMetaDir(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(j, "", "  ")
	return os.WriteFile(filepath.Join(s.jobsMetaDir(), j.ID+".json"), b, 0o600)
}

func (s *Server) loadUserJob(id string) (*jobMetaUser, error) {
	b, err := os.ReadFile(filepath.Join(s.jobsMetaDir(), id+".json"))
	if err != nil {
		return nil, err
	}
	j := &jobMetaUser{}
	if err := json.Unmarshal(b, j); err != nil {
		return nil, err
	}
	return j, nil
}

func (s *Server) deleteUserJob(id string) error {
	p := filepath.Join(s.jobsMetaDir(), id+".json")
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// resolveAppEnvForJob looks up the parent app's stored services list
// and re-runs resolveServices so the job sees the same env as the app.
// Per-job literal env overrides anything inherited.
func (s *Server) resolveAppEnvForJob(app string, override map[string]string) (map[string]string, error) {
	env := map[string]string{}
	if app != "" {
		meta, ok := s.loadJobMeta(app)
		if !ok {
			return nil, fmt.Errorf("no metadata for parent app %q — re-deploy with v0.23+ blobctl so we can persist Services list", app)
		}
		if len(meta.Services) > 0 {
			req := &api.DeployRequest{
				App:         app,
				Environment: meta.Environment,
				Services:    meta.Services,
				Env:         env,
			}
			if err := s.resolveServices(req); err != nil {
				return nil, fmt.Errorf("resolve parent services: %w", err)
			}
			env = req.Env
		}
	}
	for k, v := range override {
		env[k] = v
	}
	return env, nil
}

// --- HTTP handlers -----------------------------------------------------------

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeErr(w, 405, "method not allowed")
		return
	}
	out, err := s.listUserJobs(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleJobsRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeErr(w, 405, "method not allowed")
		return
	}
	var req api.RunJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	out, err := s.runUserJob(r.Context(), &req)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleJobsSchedule(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeErr(w, 405, "method not allowed")
		return
	}
	var req api.ScheduleJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	out, err := s.scheduleUserJob(r.Context(), &req)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleJobsItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/jobs/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	if !validName(id) {
		writeErr(w, 400, "invalid job id")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == "GET":
		out, err := s.statusUserJob(r.Context(), id)
		if err != nil {
			writeErr(w, 404, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case len(parts) == 1 && r.Method == "DELETE":
		if err := s.cancelUserJob(r.Context(), id); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"id": id, "cancelled": true})
	case len(parts) == 2 && parts[1] == "logs" && r.Method == "GET":
		fire, _ := strconv.Atoi(r.URL.Query().Get("fire"))
		out, err := s.logsUserJob(r.Context(), id, fire)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 404, "not found")
	}
}

// --- core flows --------------------------------------------------------------

func (s *Server) runUserJob(ctx context.Context, req *api.RunJobRequest) (*api.JobRun, error) {
	if req.Image == "" {
		return nil, errors.New("image is required")
	}
	if req.Name == "" {
		req.Name = fmt.Sprintf("run-%d", time.Now().Unix())
	}
	if !validName(req.Name) {
		return nil, errors.New("invalid name")
	}
	if req.CPU <= 0 {
		req.CPU = 200
	}
	if req.Memory <= 0 {
		req.Memory = 256
	}

	env, err := s.resolveAppEnvForJob(req.App, req.Env)
	if err != nil {
		return nil, err
	}
	id := "blob-job-" + req.Name
	dr := &api.DeployRequest{
		App:         req.Name,
		Environment: "prod",
		Form:        "job",
		Replicas:    1,
		CPU:         req.CPU,
		Memory:      req.Memory,
		Env:         env,
		Command:     req.Command,
	}
	hcl := renderBatch(id, s.cfg.Datacenter, req.Image, dr,
		renderEnvBlock(env, nil), "", "", "", false, "")
	if err := os.MkdirAll(s.cfg.JobsDir, 0o755); err != nil {
		return nil, err
	}
	jobPath := filepath.Join(s.cfg.JobsDir, id+".nomad")
	if err := os.WriteFile(jobPath, []byte(hcl), 0o644); err != nil {
		return nil, err
	}
	if err := s.run(ctx, "nomad", "job", "run", jobPath); err != nil {
		return nil, fmt.Errorf("schedule job: %w", err)
	}
	jm := &jobMetaUser{
		ID:        id,
		Name:      req.Name,
		App:       req.App,
		Kind:      "job",
		Image:     req.Image,
		Command:   req.Command,
		Env:       req.Env,
		CPU:       req.CPU,
		Memory:    req.Memory,
		Timeout:   req.Timeout,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.saveUserJob(jm); err != nil {
		return nil, err
	}
	// Best-effort wait for the alloc to terminate so the caller can
	// fetch logs immediately. Bounded by Timeout (default 120s) and
	// uses nomad's own status polling — no kill on timeout, the job
	// is left running.
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 120
	}
	_ = s.waitJobTerminal(ctx, id, time.Duration(timeout)*time.Second)
	return s.statusUserJob(ctx, id)
}

func (s *Server) scheduleUserJob(ctx context.Context, req *api.ScheduleJobRequest) (*api.JobRun, error) {
	if req.Image == "" || req.Cron == "" {
		return nil, errors.New("image and cron are required")
	}
	if !validName(req.Name) {
		return nil, errors.New("invalid name")
	}
	if req.CPU <= 0 {
		req.CPU = 200
	}
	if req.Memory <= 0 {
		req.Memory = 256
	}
	env, err := s.resolveAppEnvForJob(req.App, req.Env)
	if err != nil {
		return nil, err
	}
	id := "blob-job-" + req.Name
	dr := &api.DeployRequest{
		App:         req.Name,
		Environment: "prod",
		Form:        "cronjob",
		Schedule:    req.Cron,
		Replicas:    1,
		CPU:         req.CPU,
		Memory:      req.Memory,
		Env:         env,
		Command:     req.Command,
	}
	hcl := renderBatch(id, s.cfg.Datacenter, req.Image, dr,
		renderEnvBlock(env, nil), "", "", "", true, req.Cron)
	if err := os.MkdirAll(s.cfg.JobsDir, 0o755); err != nil {
		return nil, err
	}
	jobPath := filepath.Join(s.cfg.JobsDir, id+".nomad")
	if err := os.WriteFile(jobPath, []byte(hcl), 0o644); err != nil {
		return nil, err
	}
	if err := s.run(ctx, "nomad", "job", "run", jobPath); err != nil {
		return nil, fmt.Errorf("schedule cron: %w", err)
	}
	jm := &jobMetaUser{
		ID:        id,
		Name:      req.Name,
		App:       req.App,
		Kind:      "cronjob",
		Cron:      req.Cron,
		Image:     req.Image,
		Command:   req.Command,
		Env:       req.Env,
		CPU:       req.CPU,
		Memory:    req.Memory,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.saveUserJob(jm); err != nil {
		return nil, err
	}
	return s.statusUserJob(ctx, id)
}

func (s *Server) listUserJobs(ctx context.Context) (*api.ListJobsResponse, error) {
	out := &api.ListJobsResponse{}
	entries, err := os.ReadDir(s.jobsMetaDir())
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
		id := strings.TrimSuffix(e.Name(), ".json")
		jr, err := s.statusUserJob(ctx, id)
		if err != nil {
			continue
		}
		out.Jobs = append(out.Jobs, *jr)
	}
	sort.Slice(out.Jobs, func(i, j int) bool { return out.Jobs[i].Name < out.Jobs[j].Name })
	return out, nil
}

func (s *Server) statusUserJob(ctx context.Context, id string) (*api.JobRun, error) {
	jm, err := s.loadUserJob(id)
	if err != nil {
		return nil, fmt.Errorf("no such job %q", id)
	}
	out := &api.JobRun{
		ID:        id,
		Name:      jm.Name,
		App:       jm.App,
		Kind:      jm.Kind,
		Cron:      jm.Cron,
		Image:     jm.Image,
		Command:   jm.Command,
		CreatedAt: jm.CreatedAt,
		Status:    "unknown",
	}
	// For one-shots, look at the most recent alloc state. For cronjobs,
	// look at the periodic parent's "Stopped" flag.
	body, err := s.nomadGET(ctx, "/v1/job/"+id)
	if err == nil {
		var j struct {
			Status  string
			Stopped bool
		}
		_ = json.Unmarshal(body, &j)
		out.Status = j.Status
		if j.Stopped {
			out.Status = "stopped"
		}
	}
	if jm.Kind == "job" {
		// Pull the last alloc to populate ExitCode + FinishedAt.
		ab, err := s.nomadGET(ctx, "/v1/job/"+id+"/allocations")
		if err == nil {
			var allocs []struct {
				ID         string
				ClientStatus string
				TaskStates   map[string]struct {
					State    string
					FinishedAt time.Time
					Events     []struct {
						ExitCode int
					}
				}
			}
			_ = json.Unmarshal(ab, &allocs)
			for _, a := range allocs {
				if ts, ok := a.TaskStates["app"]; ok {
					if !ts.FinishedAt.IsZero() {
						out.FinishedAt = ts.FinishedAt
					}
					for _, ev := range ts.Events {
						if ev.ExitCode != 0 {
							out.ExitCode = ev.ExitCode
						}
					}
				}
			}
		}
	}
	return out, nil
}

func (s *Server) cancelUserJob(ctx context.Context, id string) error {
	if _, err := s.loadUserJob(id); err != nil {
		return errors.New("no such job")
	}
	if err := s.run(ctx, "nomad", "job", "stop", "-purge", id); err != nil {
		stdLog("jobs cancel %s: nomad stop returned %v (continuing)", id, err)
	}
	_ = os.Remove(filepath.Join(s.cfg.JobsDir, id+".nomad"))
	return s.deleteUserJob(id)
}

// logsUserJob retrieves stdout + stderr for the most recent (or
// fire-N'th) terminal allocation. fire=0 → most recent. For periodic
// parents, fire=1 is the *first* fire, fire=2 the second, etc.
func (s *Server) logsUserJob(ctx context.Context, id string, fire int) (*api.JobLogsResponse, error) {
	jm, err := s.loadUserJob(id)
	if err != nil {
		return nil, errors.New("no such job")
	}
	out := &api.JobLogsResponse{Job: id, Fire: fire}

	// For cronjobs, the alloc lives under the child periodic instance
	// (id "blob-job-NAME/periodic-<unix>"). Periodic parents don't own
	// allocs directly — we have to enumerate children jobs first via
	// the prefix query, then sort chronologically and pick the
	// fire-N'th child's most recent alloc.
	allocID := ""
	if jm.Kind == "cronjob" {
		body, err := s.nomadGET(ctx, "/v1/jobs?prefix="+id+"%2Fperiodic-")
		if err != nil {
			return out, fmt.Errorf("list children: %w", err)
		}
		var children []struct {
			ID         string
			SubmitTime int64
		}
		_ = json.Unmarshal(body, &children)
		// Filter to direct children of this periodic parent (the prefix
		// query may return unrelated jobs that share a name prefix).
		filtered := children[:0]
		for _, ch := range children {
			if strings.HasPrefix(ch.ID, id+"/periodic-") {
				filtered = append(filtered, ch)
			}
		}
		sort.Slice(filtered, func(i, j int) bool { return filtered[i].SubmitTime < filtered[j].SubmitTime })
		idx := fire - 1
		if fire <= 0 {
			idx = len(filtered) - 1
		}
		if idx < 0 || idx >= len(filtered) {
			return out, fmt.Errorf("fire index %d out of range (have %d fires)", fire, len(filtered))
		}
		childID := filtered[idx].ID
		// Look up that child's alloc.
		ab, err := s.nomadGET(ctx, "/v1/job/"+childID+"/allocations")
		if err != nil {
			return out, fmt.Errorf("list child allocations: %w", err)
		}
		var allocs []struct {
			ID         string
			CreateTime int64
		}
		_ = json.Unmarshal(ab, &allocs)
		sort.Slice(allocs, func(i, j int) bool { return allocs[i].CreateTime < allocs[j].CreateTime })
		if len(allocs) == 0 {
			return out, fmt.Errorf("fire %d has no allocations yet", fire)
		}
		allocID = allocs[len(allocs)-1].ID
	} else {
		body, err := s.nomadGET(ctx, "/v1/job/"+id+"/allocations")
		if err != nil {
			return out, fmt.Errorf("list allocations: %w", err)
		}
		var allocs []struct {
			ID         string
			CreateTime int64
		}
		_ = json.Unmarshal(body, &allocs)
		sort.Slice(allocs, func(i, j int) bool { return allocs[i].CreateTime < allocs[j].CreateTime })
		if len(allocs) == 0 {
			return out, errors.New("no allocations yet")
		}
		allocID = allocs[len(allocs)-1].ID
	}
	out.Stdout = nomadAllocLogs(ctx, allocID, "app", false)
	out.Stderr = nomadAllocLogs(ctx, allocID, "app", true)
	return out, nil
}

func nomadAllocLogs(ctx context.Context, allocID, task string, stderr bool) string {
	args := []string{"alloc", "logs"}
	if stderr {
		args = append(args, "-stderr")
	}
	args = append(args, "-task", task, allocID)
	cmd := exec.CommandContext(ctx, "nomad", args...)
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// waitJobTerminal polls Nomad for the job until all its allocations
// reach a terminal state, or the deadline elapses. Best-effort — used
// only to make `blob jobs run` block long enough that immediate logs
// fetches see the captured output.
func (s *Server) waitJobTerminal(ctx context.Context, id string, deadline time.Duration) error {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		body, err := s.nomadGET(ctx, "/v1/job/"+id+"/allocations")
		if err == nil {
			var allocs []struct {
				ClientStatus string
			}
			if err := json.Unmarshal(body, &allocs); err == nil && len(allocs) > 0 {
				allTerminal := true
				for _, a := range allocs {
					if a.ClientStatus != "complete" && a.ClientStatus != "failed" && a.ClientStatus != "lost" {
						allTerminal = false
						break
					}
				}
				if allTerminal {
					return nil
				}
			}
		}
		time.Sleep(1 * time.Second)
	}
	return errors.New("timed out waiting for job to terminate")
}
