package server

import (
	"strings"
	"testing"

	"github.com/darvell/blob/internal/api"
)

func TestRenderJobWebService(t *testing.T) {
	req := &api.DeployRequest{App: "hello", Form: "web-service", CPU: 200, Memory: 256, Replicas: 1, Env: map[string]string{"FOO": "bar"}}
	job := renderJob(req, "registry.example/hello:1", 8080, "hello.example.com", "dc1", "hello")
	for _, want := range []string{
		`job "hello"`,
		`datacenters = ["dc1"]`,
		"Host(`hello.example.com`)",
		`image = "registry.example/hello:1"`,
		`to = 8080`,
		`FOO = "bar"`,
		`cpu    = 200`,
		`memory = 256`,
	} {
		if !strings.Contains(job, want) {
			t.Errorf("job missing %q\n--- job ---\n%s", want, job)
		}
	}
}

func TestRenderJobDaemonHasNoTraefikTags(t *testing.T) {
	req := &api.DeployRequest{App: "worker", Form: "daemon", CPU: 100, Memory: 128, Replicas: 1}
	job := renderJob(req, "img:1", 0, "", "dc1", "worker")
	if strings.Contains(job, "traefik.enable=true") {
		t.Fatalf("daemon should not have traefik tags, got: %s", job)
	}
	if !strings.Contains(job, `image = "img:1"`) {
		t.Fatalf("daemon job missing image")
	}
}

func TestRenderJobBatch(t *testing.T) {
	req := &api.DeployRequest{App: "migrate", Form: "job", CPU: 100, Memory: 128, Replicas: 1}
	job := renderJob(req, "img:1", 0, "", "dc1", "migrate")
	if !strings.Contains(job, `type = "batch"`) {
		t.Fatalf("job form should be Nomad batch type:\n%s", job)
	}
	if strings.Contains(job, "periodic {") {
		t.Fatalf("non-cron batch should not have periodic block")
	}
}

func TestRenderJobCronjob(t *testing.T) {
	req := &api.DeployRequest{App: "nightly", Form: "cronjob", Schedule: "0 3 * * *", CPU: 100, Memory: 128, Replicas: 1}
	job := renderJob(req, "img:1", 0, "", "dc1", "nightly")
	for _, want := range []string{
		`type = "batch"`,
		`periodic {`,
		`cron             = "0 3 * * *"`,
		`prohibit_overlap = true`,
	} {
		if !strings.Contains(job, want) {
			t.Errorf("cronjob missing %q\n--- job ---\n%s", want, job)
		}
	}
}

func TestRenderJobBundleSidecars(t *testing.T) {
	req := &api.DeployRequest{
		App: "bundle", Form: "web-service", CPU: 100, Memory: 128, Replicas: 1,
		Sidecars: []api.Sidecar{
			{Name: "tunnel", Image: "cloudflare/cloudflared:latest", Args: []string{"tunnel", "run"}, CPU: 50, Memory: 64},
		},
	}
	job := renderJob(req, "img:1", 8080, "bundle.example.com", "dc1", "bundle")
	for _, want := range []string{
		`task "app"`,
		`task "tunnel"`,
		`args  = ["tunnel", "run"]`,
		`image = "cloudflare/cloudflared:latest"`,
	} {
		if !strings.Contains(job, want) {
			t.Errorf("bundle missing %q\n--- job ---\n%s", want, job)
		}
	}
}

func TestRenderJobVolumes(t *testing.T) {
	req := &api.DeployRequest{
		App: "stateful", Form: "web-service", CPU: 100, Memory: 128, Replicas: 1,
		Volumes: []api.VolumeMount{{Name: "data", Path: "/var/data"}},
	}
	job := renderJob(req, "img:1", 8080, "stateful.example.com", "dc1", "stateful")
	if !strings.Contains(job, `volume "data"`) {
		t.Fatalf("expected group-level volume block:\n%s", job)
	}
	if !strings.Contains(job, `destination = "/var/data"`) {
		t.Fatalf("expected task volume_mount:\n%s", job)
	}
	if !strings.Contains(job, `source = "blob-stateful-data"`) {
		t.Fatalf("expected scoped volume name:\n%s", job)
	}
}

func TestRenderJobCommandOverride(t *testing.T) {
	req := &api.DeployRequest{App: "x", Form: "daemon", CPU: 50, Memory: 64, Replicas: 1, Command: []string{"node", "worker.js"}}
	job := renderJob(req, "img:1", 0, "", "dc1", "x")
	if !strings.Contains(job, `command = "node"`) {
		t.Fatalf("missing command:\n%s", job)
	}
	if !strings.Contains(job, `args    = ["worker.js"]`) {
		t.Fatalf("missing args:\n%s", job)
	}
}

func TestSplitJobID(t *testing.T) {
	cases := []struct {
		id, app, env string
	}{
		{"hello", "hello", ""},
		{"hello-env-staging", "hello", "staging"},
		{"hello-env-pr-1234", "hello", "pr-1234"},
	}
	for _, c := range cases {
		app, env := splitJobID(c.id)
		if app != c.app || env != c.env {
			t.Errorf("splitJobID(%q) = (%q,%q); want (%q,%q)", c.id, app, env, c.app, c.env)
		}
	}
}

func TestJobID(t *testing.T) {
	cases := []struct {
		app, env, comp, want string
	}{
		{"hello", "", "", "hello"},
		{"hello", "prod", "", "hello"},
		{"hello", "staging", "", "hello-env-staging"},
		{"myapp", "prod", "web", "myapp-web"},
		{"myapp", "staging", "web", "myapp-web-env-staging"},
	}
	for _, c := range cases {
		got := jobID(c.app, c.env, c.comp)
		if got != c.want {
			t.Errorf("jobID(%q,%q,%q) = %q; want %q", c.app, c.env, c.comp, got, c.want)
		}
	}
}
