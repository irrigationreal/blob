package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/darvell/blob/internal/api"
)

func newPluginTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.StateDir = dir
	cfg.JobsDir = dir + "/jobs"
	cfg.SourcesDir = dir + "/sources"
	cfg.SecretsDir = dir + "/secrets"
	cfg.SecretKey = dir + "/key"
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	return srv
}

func TestPluginRoutesPersistConfig(t *testing.T) {
	srv := newPluginTestServer(t)
	req := httptest.NewRequest(http.MethodPut, "/v1/plugins/demo", bytes.NewBufferString(`{"post_deploy":"printf ok","timeout_seconds":5}`))
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /v1/plugins/demo = %d: %s", rec.Code, rec.Body.String())
	}
	cfg, err := srv.loadPlugin("demo")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.PostDeploy != "printf ok" || cfg.TimeoutSeconds != 5 {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestRunDeployHookExecutesConfiguredHook(t *testing.T) {
	srv := newPluginTestServer(t)
	marker := filepath.Join(t.TempDir(), "hooked")
	_, err := srv.setPlugin("demo", &api.SetPluginRequest{PostDeploy: "printf '%s' \"$BLOB_APP:$BLOB_HOOK:$BLOB_JOB_ID\" > " + marker})
	if err != nil {
		t.Fatal(err)
	}
	req := &api.DeployRequest{App: "demo", Environment: "prod"}
	hook := pluginHookContext{JobID: "demo", Image: "registry/demo:test", URL: "https://demo.example.com"}
	if err := srv.runDeployHook(context.Background(), pluginHookPost, req, hook); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "demo:post:demo" {
		t.Fatalf("marker = %q", string(b))
	}
}

func TestSetPluginNoopPreservesUpdatedAt(t *testing.T) {
	srv := newPluginTestServer(t)
	req := &api.SetPluginRequest{PreDeploy: "printf pre"}
	first, err := srv.setPlugin("demo", req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := srv.setPlugin("demo", req)
	if err != nil {
		t.Fatal(err)
	}
	if !second.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("noop set changed UpdatedAt: %s -> %s", first.UpdatedAt, second.UpdatedAt)
	}
}
