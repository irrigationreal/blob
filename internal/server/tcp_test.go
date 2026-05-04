package server

import (
	"strings"
	"testing"

	"github.com/darvell/blob/internal/api"
)

func TestRenderJobTCPDaemon(t *testing.T) {
	req := &api.DeployRequest{App: "tcp-echo", Form: "daemon", Exposure: "tcp", Port: 5678, CPU: 100, Memory: 128, Replicas: 1}
	job := renderJob(req, "img:1", 5678, "", "dc1", "tcp-echo")
	for _, want := range []string{
		`type = "service"`,
		`port "tcp"`,
		`to = 5678`,
		`port     = "tcp"`,
		`ports = ["tcp"]`,
	} {
		if !strings.Contains(job, want) {
			t.Fatalf("tcp daemon job missing %q:\n%s", want, job)
		}
	}
	if strings.Contains(job, "traefik.enable=true") || strings.Contains(job, "traefik.http.routers") {
		t.Fatalf("unbound tcp daemon should not be enabled in Traefik:\n%s", job)
	}
}

func TestValidateTCPExposureRequest(t *testing.T) {
	if err := validateTCPExposureRequest(&api.DeployRequest{Form: "daemon", Exposure: "tcp", Port: 5678}); err != nil {
		t.Fatal(err)
	}
	if err := validateTCPExposureRequest(&api.DeployRequest{Form: "web-service", Exposure: "tcp", Port: 5678}); err == nil {
		t.Fatal("expected form validation error")
	}
	if err := validateTCPExposureRequest(&api.DeployRequest{Form: "daemon", Exposure: "tcp"}); err == nil {
		t.Fatal("expected port validation error")
	}
}

func TestAddRemoveTCPTagsToJob(t *testing.T) {
	job := `job "tcp-echo" {
  group "main" {
    service {
      name     = "tcp-echo"
      tags = [
        "traefik.enable=true"
      ]
    }
  }
}`
	binding := api.TCPBinding{App: "tcp-echo", PublicPort: 20000, TargetPort: 5678, Entrypoint: tcpEntrypoint(20000)}
	updated, err := addTCPTagsToJob(job, binding)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`traefik.enable=true`,
		`traefik.tcp.routers.blobtcp-tcp-echo-20000.entrypoints=blobtcp20000`,
		`traefik.tcp.routers.blobtcp-tcp-echo-20000.rule=HostSNI(`,
		`traefik.tcp.routers.blobtcp-tcp-echo-20000.service=blobtcp-tcp-echo-20000`,
		`traefik.tcp.services.blobtcp-tcp-echo-20000.loadbalancer.server.tls=false`,
	} {
		if !strings.Contains(updated, want) {
			t.Fatalf("tcp tags missing %q:\n%s", want, updated)
		}
	}
	again, err := addTCPTagsToJob(updated, binding)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(again, "traefik.tcp.routers.blobtcp-tcp-echo-20000") != 3 {
		t.Fatalf("addTCPTagsToJob should replace existing router tags without duplicating them:\n%s", again)
	}
	removed := removeTCPTagsFromJob(updated, binding)
	if strings.Contains(removed, "traefik.enable=true") || strings.Contains(removed, "traefik.tcp.routers.blobtcp-tcp-echo-20000") || strings.Contains(removed, "traefik.tcp.services.blobtcp-tcp-echo-20000") {
		t.Fatalf("tcp tags were not removed:\n%s", removed)
	}
}

func TestRenderEdgeTraefikJobTCPEntrypoints(t *testing.T) {
	job := renderEdgeTraefikJob("dc1", "ops@example.com", []int{20001, 20000, 20000})
	for _, want := range []string{
		`port "tcp20000" { static = 20000 }`,
		`port "tcp20001" { static = 20001 }`,
		`--entrypoints.blobtcp20000.address=:20000`,
		`--entrypoints.blobtcp20001.address=:20001`,
	} {
		if !strings.Contains(job, want) {
			t.Fatalf("edge job missing %q:\n%s", want, job)
		}
	}
	if strings.Count(job, `--entrypoints.blobtcp20000.address=:20000`) != 1 {
		t.Fatalf("duplicate tcp20000 entrypoint:\n%s", job)
	}
}

func TestPatchEdgeTraefikTCPEntrypointsPreservesExistingConfig(t *testing.T) {
	base := `job "edge-traefik" {
  group "edge" {
    constraint {
      attribute = "${node.class}"
      value     = "edge"
    }
    network {
      port "http"  { static = 80 }
      port "https" { static = 443 }
      port "tcp20000" { static = 20000 }
    }
    task "traefik" {
      config {
        image = "traefik:v3.6"
        volumes = ["/custom:/custom"]
        args = [
          "--entrypoints.web.address=:80",
          "--entrypoints.websecure.address=:443",
          "--entrypoints.blobtcp20000.address=:20000",
          "--providers.nomad=true",
        ]
      }
    }
  }
}`
	patched, err := patchEdgeTraefikTCPEntrypoints(base, []int{20001})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`attribute = "${node.class}"`,
		`volumes = ["/custom:/custom"]`,
		`--providers.nomad=true`,
		`port "tcp20001" { static = 20001 }`,
		`--entrypoints.blobtcp20001.address=:20001`,
	} {
		if !strings.Contains(patched, want) {
			t.Fatalf("patched edge job missing %q:\n%s", want, patched)
		}
	}
	if strings.Contains(patched, "tcp20000") || strings.Contains(patched, "blobtcp20000") {
		t.Fatalf("old generated tcp entrypoint was not removed:\n%s", patched)
	}
}
