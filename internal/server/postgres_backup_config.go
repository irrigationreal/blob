// Off-host backup configuration for managed Postgres.
//
// Per-instance config lives at /srv/blob/postgres/<instance>/backup-config.json
// (mode 0600). The S3 secret access key is stored in plaintext on disk —
// blobd already runs as a single user owning the rest of the secret material
// (the secret store key, registry credentials), so this is consistent with
// the existing trust model. The API never returns it to clients in GET.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/irrigationreal/blob/internal/api"
)

const (
	defaultBackupSchedule         = "0 3 * * *"
	defaultBackupRetentionDaily   = 7
	defaultBackupRetentionWeekly  = 4
	defaultBackupRetentionMonthly = 6
)

func (s *Server) backupConfigPath(instance string) string {
	return filepath.Join(s.cfg.StateDir, "postgres", instance, "backup-config.json")
}

func (s *Server) loadBackupConfig(instance string) (*api.PostgresBackupConfig, error) {
	b, err := os.ReadFile(s.backupConfigPath(instance))
	if err != nil {
		return nil, err
	}
	cfg := &api.PostgresBackupConfig{}
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, err
	}
	cfg.Instance = instance
	applyBackupConfigDefaults(cfg)
	return cfg, nil
}

func applyBackupConfigDefaults(c *api.PostgresBackupConfig) {
	if c.DestinationKind == "" {
		c.DestinationKind = "s3"
	}
	if c.S3Region == "" {
		c.S3Region = "us-east-1"
	}
	if c.Schedule == "" {
		c.Schedule = defaultBackupSchedule
	}
	if c.RetentionDaily == 0 {
		c.RetentionDaily = defaultBackupRetentionDaily
	}
	if c.RetentionWeekly == 0 {
		c.RetentionWeekly = defaultBackupRetentionWeekly
	}
	if c.RetentionMonthly == 0 {
		c.RetentionMonthly = defaultBackupRetentionMonthly
	}
	c.S3Prefix = strings.TrimSuffix(c.S3Prefix, "/")
	if c.S3Prefix != "" {
		c.S3Prefix += "/"
	}
}

func (s *Server) saveBackupConfig(c *api.PostgresBackupConfig) error {
	if c.Instance == "" {
		return errors.New("instance required")
	}
	if _, err := s.loadPostgres(c.Instance); err != nil {
		return fmt.Errorf("instance %q not found", c.Instance)
	}
	if c.Enabled {
		if c.S3Bucket == "" {
			return errors.New("s3_bucket required when enabled")
		}
		if c.S3AccessKeyID == "" || c.S3SecretAccessKey == "" {
			return errors.New("s3_access_key_id and s3_secret_access_key required when enabled")
		}
	}
	applyBackupConfigDefaults(c)
	dir := filepath.Dir(s.backupConfigPath(c.Instance))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.backupConfigPath(c.Instance), b, 0o600)
}

func (s *Server) deleteBackupConfig(instance string) error {
	err := os.Remove(s.backupConfigPath(instance))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// publicBackupConfig returns the config with the secret key masked for API responses.
func publicBackupConfig(c *api.PostgresBackupConfig) *api.PostgresBackupConfig {
	out := *c
	if out.S3SecretAccessKey != "" {
		out.S3SecretAccessKey = "***"
	}
	return &out
}

// listAllBackupConfigs returns the configs for every Postgres instance that
// has one. Used by the scheduler at startup.
func (s *Server) listAllBackupConfigs() ([]*api.PostgresBackupConfig, error) {
	pgList, err := s.listPostgres(context.Background())
	if err != nil {
		return nil, err
	}
	var out []*api.PostgresBackupConfig
	for _, p := range pgList.Postgres {
		c, err := s.loadBackupConfig(p.Name)
		if err != nil {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// --- HTTP handlers -----------------------------------------------------------

func (s *Server) handlePostgresBackupConfig(w http.ResponseWriter, r *http.Request, instance string) {
	switch r.Method {
	case "GET":
		c, err := s.loadBackupConfig(instance)
		if err != nil {
			if os.IsNotExist(err) {
				writeErr(w, 404, "no backup config for "+instance)
				return
			}
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, publicBackupConfig(c))
	case "PUT":
		var req api.SetPostgresBackupConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		req.Config.Instance = instance
		// Preserve existing secret if the caller sent "***" (mask) or empty.
		if req.Config.S3SecretAccessKey == "" || req.Config.S3SecretAccessKey == "***" {
			if existing, err := s.loadBackupConfig(instance); err == nil {
				req.Config.S3SecretAccessKey = existing.S3SecretAccessKey
			}
		}
		if err := s.saveBackupConfig(&req.Config); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		// Notify the scheduler to reload this instance's job.
		s.scheduler.Reload(instance)
		out, _ := s.loadBackupConfig(instance)
		writeJSON(w, 200, publicBackupConfig(out))
	case "DELETE":
		if err := s.deleteBackupConfig(instance); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		s.scheduler.Reload(instance)
		writeJSON(w, 200, map[string]any{"instance": instance, "cleared": true})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handlePostgresBackupConfigTest(w http.ResponseWriter, r *http.Request, instance string) {
	if r.Method != "POST" {
		writeErr(w, 405, "method not allowed")
		return
	}
	c, err := s.loadBackupConfig(instance)
	if err != nil {
		writeErr(w, 404, "no backup config for "+instance)
		return
	}
	if err := s.testBackupDestination(r.Context(), c); err != nil {
		writeJSON(w, 200, api.TestPostgresBackupConfigResponse{OK: false, Detail: err.Error()})
		return
	}
	writeJSON(w, 200, api.TestPostgresBackupConfigResponse{OK: true, Detail: "head bucket succeeded"})
}
