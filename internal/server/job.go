package server

import (
	"fmt"
	"sort"
	"strings"
)

// renderNomadJob returns an HCL Nomad job spec for a web-service / daemon form.
//
// Web-services receive Traefik routing tags so the platform's existing Traefik
// (running in the cluster) can route HTTP/HTTPS to them by hostname. Daemons
// have no exposure block and no Traefik tags.
func renderNomadJob(app, image string, port int, domain string, cpu, memory, replicas int, form string, env map[string]string, dc string) string {
	if cpu <= 0 {
		cpu = 500
	}
	if memory <= 0 {
		memory = 512
	}
	if replicas <= 0 {
		replicas = 1
	}
	if form == "" {
		form = "web-service"
	}
	envBlock := renderEnvBlock(env)

	switch form {
	case "daemon":
		return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "service"

  group "main" {
    count = %d
    task "app" {
      driver = "docker"
      config {
        image = %q
      }
%s      resources {
        cpu    = %d
        memory = %d
      }
    }
  }
}
`, app, dc, replicas, image, envBlock, cpu, memory)
	default: // web-service
		return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "service"

  group "web" {
    count = %d
    network {
      port "http" {
        to = %d
      }
    }

    service {
      name     = %q
      provider = "nomad"
      port     = "http"
      tags = [
        "traefik.enable=true",
        "traefik.http.routers.%s-http.rule=Host(`+"`%s`"+`)",
        "traefik.http.routers.%s-http.entrypoints=web",
        "traefik.http.routers.%s-https.rule=Host(`+"`%s`"+`)",
        "traefik.http.routers.%s-https.entrypoints=websecure",
        "traefik.http.routers.%s-https.tls=true",
        "traefik.http.routers.%s-https.tls.certresolver=le"
      ]
    }

    task "app" {
      driver = "docker"
      config {
        image = %q
        ports = ["http"]
      }
%s      resources {
        cpu    = %d
        memory = %d
      }
    }
  }
}
`, app, dc, replicas, port, app, app, domain, app, app, domain, app, app, app, image, envBlock, cpu, memory)
	}
}

func renderEnvBlock(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString("      env {\n")
	for _, k := range keys {
		fmt.Fprintf(&sb, "        %s = %q\n", k, env[k])
	}
	sb.WriteString("      }\n")
	return sb.String()
}
