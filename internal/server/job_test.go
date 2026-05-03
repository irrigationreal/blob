package server

import (
	"strings"
	"testing"
)

func TestRenderNomadJobWebService(t *testing.T) {
	job := renderNomadJob("hello", "registry.example/hello:1", 8080, "hello.example.com", 200, 256, 1, "web-service", map[string]string{"FOO": "bar"}, "dc1")
	for _, want := range []string{
		`job "hello"`,
		`datacenters = ["dc1"]`,
		`Host(` + "`hello.example.com`" + `)`,
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

func TestRenderNomadJobDaemonHasNoTraefikTags(t *testing.T) {
	job := renderNomadJob("worker", "img:1", 0, "", 100, 128, 1, "daemon", nil, "dc1")
	if strings.Contains(job, "traefik.enable=true") {
		t.Fatalf("daemon should not have traefik tags, got: %s", job)
	}
	if !strings.Contains(job, `image = "img:1"`) {
		t.Fatalf("daemon job missing image")
	}
}
