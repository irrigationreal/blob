// Package server: managed S3-compatible object storage (v0.14).
//
// One MinIO instance per `blob storage create` call, one bucket
// auto-provisioned per instance with the same name as the instance
// (e.g. `blob storage create assets` brings up MinIO + creates a
// bucket called `assets`). Apps bind via `services: [<name>]`; the
// resolver injects S3_ENDPOINT / S3_BUCKET / S3_ACCESS_KEY /
// S3_SECRET_KEY into the workload's environment.
//
// State at /srv/blob/storage/<name>.json (mode 0600). Persistent data
// on a Docker named volume blob-storage-<name> mounted at /data.
//
// Internal-only by default — MinIO binds the host port we allocate, no
// external Traefik route. Operators expose it manually if they need
// public access (matches the v0.7 dogfood shape where MinIO was
// deployed as a regular blob app at minio.irrigate.cc).
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/darvell/blob/internal/api"
)

const (
	storagePortFloor = 14500
	storagePortCeil  = 14600
	// MinIO console runs on the next port over (api+1) per upstream
	// convention. We allocate them as a pair.
	storageConsolePortOffset = 1
)

type storageMeta struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	APIPort   int       `json:"api_port"`
	UIPort    int       `json:"ui_port"`
	AccessKey string    `json:"access_key"`
	SecretKey string    `json:"secret_key"`
	Bucket    string    `json:"bucket"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) storageMetaDir() string {
	return filepath.Join(s.cfg.StateDir, "storage")
}

func (s *Server) loadStorage(name string) (*storageMeta, error) {
	b, err := os.ReadFile(filepath.Join(s.storageMetaDir(), name+".json"))
	if err != nil {
		return nil, err
	}
	m := &storageMeta{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Server) saveStorage(m *storageMeta) error {
	if err := os.MkdirAll(s.storageMetaDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.storageMetaDir(), m.Name+".json"), b, 0o600)
}

func (s *Server) allocateStoragePorts() (apiPort, uiPort int, err error) {
	used := map[int]bool{}
	if entries, e := os.ReadDir(s.storageMetaDir()); e == nil {
		for _, ent := range entries {
			if !strings.HasSuffix(ent.Name(), ".json") {
				continue
			}
			m, e := s.loadStorage(strings.TrimSuffix(ent.Name(), ".json"))
			if e == nil {
				used[m.APIPort] = true
				used[m.UIPort] = true
			}
		}
	}
	// Allocate in pairs (API even, UI odd within a 2-slot window) so two
	// instances never collide on the console port.
	for p := storagePortFloor; p < storagePortCeil-1; p += 2 {
		if !used[p] && !used[p+storageConsolePortOffset] {
			return p, p + storageConsolePortOffset, nil
		}
	}
	return 0, 0, errors.New("storage port pool exhausted")
}

// --- HTTP handlers -----------------------------------------------------------

func (s *Server) handleStorage(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listStorage(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.CreateStorageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.createStorage(r.Context(), &req)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleStorageItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/storage/")
	parts := strings.SplitN(rest, "/", 2)
	name := parts[0]
	if !validName(name) {
		writeErr(w, 400, "invalid name")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == "GET":
		m, err := s.loadStorage(name)
		if err != nil {
			writeErr(w, 404, "no such storage")
			return
		}
		writeJSON(w, 200, s.storagePublic(r.Context(), m))
	case len(parts) == 1 && r.Method == "DELETE":
		if err := s.destroyStorage(r.Context(), name); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"name": name, "destroyed": true})
	case len(parts) == 2 && parts[1] == "url" && r.Method == "GET":
		m, err := s.loadStorage(name)
		if err != nil {
			writeErr(w, 404, "no such storage")
			return
		}
		writeJSON(w, 200, api.StorageURL{
			Endpoint:  s.storageEndpoint(m),
			Bucket:    m.Bucket,
			AccessKey: m.AccessKey,
			SecretKey: m.SecretKey,
			Console:   fmt.Sprintf("http://%s:%d", s.postgresHost(), m.UIPort),
		})
	default:
		writeErr(w, 404, "not found")
	}
}

// --- create / destroy / list --------------------------------------------------

func (s *Server) createStorage(ctx context.Context, req *api.CreateStorageRequest) (*api.Storage, error) {
	if !validName(req.Name) {
		return nil, errors.New("invalid name")
	}
	if _, err := s.loadStorage(req.Name); err == nil {
		return nil, fmt.Errorf("storage %q already exists", req.Name)
	}
	if req.Version == "" {
		// Pin a known-good RELEASE tag rather than `latest` so the
		// scheduled job is reproducible.
		req.Version = "RELEASE.2025-04-08T15-41-24Z"
	}
	if req.CPU <= 0 {
		req.CPU = 300
	}
	if req.Memory <= 0 {
		req.Memory = 512
	}
	bucket := req.Bucket
	if bucket == "" {
		bucket = req.Name
	}
	apiPort, uiPort, err := s.allocateStoragePorts()
	if err != nil {
		return nil, err
	}
	m := &storageMeta{
		Name:      req.Name,
		Version:   req.Version,
		APIPort:   apiPort,
		UIPort:    uiPort,
		AccessKey: "blob-" + req.Name,
		SecretKey: randomStorageSecret(),
		Bucket:    bucket,
		CreatedAt: time.Now(),
	}
	if err := s.saveStorage(m); err != nil {
		return nil, err
	}
	id := "storage-" + m.Name
	hcl := renderStorageJob(m, s.cfg.Datacenter, id, req.CPU, req.Memory)
	if err := os.MkdirAll(s.cfg.JobsDir, 0o755); err != nil {
		return nil, err
	}
	jobPath := filepath.Join(s.cfg.JobsDir, id+".nomad")
	if err := os.WriteFile(jobPath, []byte(hcl), 0o644); err != nil {
		return nil, err
	}
	if err := s.run(ctx, "nomad", "job", "run", jobPath); err != nil {
		_ = os.Remove(filepath.Join(s.storageMetaDir(), m.Name+".json"))
		return nil, fmt.Errorf("schedule storage: %w", err)
	}
	if err := s.waitJobRunning(ctx, id, 90*time.Second); err != nil {
		return nil, fmt.Errorf("storage %q did not become ready: %w", m.Name, err)
	}
	// Provision the default bucket via `mc` against the just-started
	// MinIO. This is a one-shot best-effort: if the bucket already
	// exists (re-create after destroy left the volume), `mc mb` returns
	// an error we swallow.
	if err := s.ensureStorageBucket(ctx, m); err != nil {
		stdLog("storage %s: bucket bootstrap returned %v (instance is up; create the bucket manually with mc)", m.Name, err)
	}
	return s.storagePublic(ctx, m), nil
}

// ensureStorageBucket runs `mc alias set` + `mc mb --ignore-existing`
// inside a one-off docker container so we don't need mc on the host.
// minio's official mc image is small and stable.
func (s *Server) ensureStorageBucket(ctx context.Context, m *storageMeta) error {
	endpoint := s.storageEndpoint(m)
	// `mc mb --ignore-existing alias/bucket` is the idempotent shape.
	script := fmt.Sprintf(
		`mc alias set s %s %s %s --api S3v4 >/dev/null && mc mb --ignore-existing s/%s >/dev/null`,
		endpoint, m.AccessKey, m.SecretKey, m.Bucket,
	)
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm",
		"--network", "host",
		"--entrypoint", "sh",
		"minio/mc:latest",
		"-c", script,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mc: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *Server) destroyStorage(ctx context.Context, name string) error {
	m, err := s.loadStorage(name)
	if err != nil {
		return errors.New("no such storage")
	}
	id := "storage-" + m.Name
	if err := s.run(ctx, "nomad", "job", "stop", "-purge", id); err != nil {
		stdLog("storage destroy %s: nomad stop returned %v (continuing)", name, err)
	}
	_ = os.Remove(filepath.Join(s.cfg.JobsDir, id+".nomad"))
	_ = os.Remove(filepath.Join(s.storageMetaDir(), m.Name+".json"))
	// Docker volume preserved (matches postgres/valkey semantics).
	return nil
}

func (s *Server) listStorage(ctx context.Context) (*api.ListStorageResponse, error) {
	out := &api.ListStorageResponse{}
	entries, err := os.ReadDir(s.storageMetaDir())
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
		m, err := s.loadStorage(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out.Storage = append(out.Storage, *s.storagePublic(ctx, m))
	}
	sort.Slice(out.Storage, func(i, j int) bool { return out.Storage[i].Name < out.Storage[j].Name })
	return out, nil
}

func (s *Server) storagePublic(ctx context.Context, m *storageMeta) *api.Storage {
	host := s.postgresHost()
	status := "unknown"
	if body, err := s.nomadGET(ctx, "/v1/job/storage-"+m.Name); err == nil {
		var j struct{ Status string }
		_ = json.Unmarshal(body, &j)
		status = j.Status
	}
	return &api.Storage{
		Name:      m.Name,
		Version:   m.Version,
		Host:      host,
		APIPort:   m.APIPort,
		UIPort:    m.UIPort,
		Endpoint:  s.storageEndpoint(m),
		Bucket:    m.Bucket,
		JobID:     "storage-" + m.Name,
		Status:    status,
		CreatedAt: m.CreatedAt,
	}
}

func (s *Server) storageEndpoint(m *storageMeta) string {
	return fmt.Sprintf("http://%s:%d", s.postgresHost(), m.APIPort)
}

// lookupStorageForBinding resolves a Storage instance for a `services:`
// binding and injects the S3-style env into the consumer. The first
// storage binding wins the canonical S3_* slot; subsequent bindings
// get only their name-prefixed env (so apps with two buckets can
// distinguish them via <NAME>_BUCKET / <NAME>_ENDPOINT).
func (s *Server) lookupStorageForBinding(name string, env map[string]string, primary *bool) bool {
	m, err := s.loadStorage(name)
	if err != nil {
		return false
	}
	endpoint := s.storageEndpoint(m)
	if *primary {
		env["S3_ENDPOINT"] = endpoint
		env["S3_BUCKET"] = m.Bucket
		env["S3_ACCESS_KEY"] = m.AccessKey
		env["S3_SECRET_KEY"] = m.SecretKey
		env["S3_REGION"] = "us-east-1"
		env["S3_USE_PATH_STYLE"] = "true"
		// Also export the AWS-SDK-conventional names so apps using the
		// canonical SDK env work without remapping.
		env["AWS_ACCESS_KEY_ID"] = m.AccessKey
		env["AWS_SECRET_ACCESS_KEY"] = m.SecretKey
		env["AWS_REGION"] = "us-east-1"
		env["AWS_ENDPOINT_URL_S3"] = endpoint
		*primary = false
	}
	prefix := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	env[prefix+"_ENDPOINT"] = endpoint
	env[prefix+"_BUCKET"] = m.Bucket
	env[prefix+"_ACCESS_KEY"] = m.AccessKey
	env[prefix+"_SECRET_KEY"] = m.SecretKey
	return true
}

func randomStorageSecret() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		// Fall back to time-based — OK because saveStorage stores it
		// once and never derives from it again. Real entropy is the
		// expectation; this branch is just defensive.
		return fmt.Sprintf("blob-storage-secret-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
