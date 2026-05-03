package server

import (
	"fmt"
	"sort"
	"strings"

	"github.com/darvell/blob/internal/api"
)

// renderJob writes an HCL Nomad job for a single Component (DeployRequest).
// Supports forms: web-service, daemon, job (batch), cronjob (periodic batch).
// Bundles are expressed as a single group with one primary task plus sidecars.
func renderJob(req *api.DeployRequest, image string, port int, domain, dc string, namespacedID string) string {
	if req.CPU <= 0 {
		req.CPU = 500
	}
	if req.Memory <= 0 {
		req.Memory = 512
	}
	if req.Replicas <= 0 {
		req.Replicas = 1
	}
	form := req.Form
	if form == "" {
		form = "web-service"
	}

	envBlock := renderEnvBlock(req.Env, req.Secrets)
	volumeBlocks, volumeMounts := renderVolumes(req.Volumes, namespacedID)
	sidecars := renderSidecars(req.Sidecars)

	switch form {
	case "job":
		return renderBatch(namespacedID, dc, image, req, envBlock, volumeBlocks, volumeMounts, sidecars, false, "")
	case "cronjob":
		return renderBatch(namespacedID, dc, image, req, envBlock, volumeBlocks, volumeMounts, sidecars, true, req.Schedule)
	case "daemon":
		return renderDaemon(namespacedID, dc, image, req, envBlock, volumeBlocks, volumeMounts, sidecars)
	case "static":
		return renderWebService(namespacedID, dc, image, port, domain, req, envBlock, volumeBlocks, volumeMounts, sidecars)
	default: // web-service
		return renderWebService(namespacedID, dc, image, port, domain, req, envBlock, volumeBlocks, volumeMounts, sidecars)
	}
}

func renderWebService(id, dc, image string, port int, domain string, req *api.DeployRequest, envBlock, volBlocks, volMounts, sidecars string) string {
	cmdBlock := renderCommandBlock(req.Command)
	traefikTags := renderTraefikTags(id, domain, req.Domains)
	return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "service"

  group "web" {
    count = %d
%s
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
%s      ]
    }

    task "app" {
      driver = "docker"
      config {
        image = %q
        ports = ["http"]
%s%s      }
%s      resources {
        cpu    = %d
        memory = %d
      }
    }
%s  }
}
`, id, dc, req.Replicas, volBlocks, port, id, traefikTags, image, cmdBlock, volMounts, envBlock, req.CPU, req.Memory, sidecars)
}

// renderTraefikTags returns the tags for a service. The primary domain plus any
// additional domains all map to the same backend. Each Host expression is or'd
// together inside a single router rule using Traefik's || syntax.
func renderTraefikTags(id, primary string, extras []string) string {
	hosts := append([]string{primary}, extras...)
	rule := strings.Builder{}
	for i, h := range hosts {
		if i > 0 {
			rule.WriteString(" || ")
		}
		rule.WriteString("Host(`")
		rule.WriteString(h)
		rule.WriteString("`)")
	}
	r := rule.String()
	var sb strings.Builder
	fmt.Fprintf(&sb, "        \"traefik.enable=true\",\n")
	fmt.Fprintf(&sb, "        \"traefik.http.routers.%s-http.rule=%s\",\n", id, r)
	fmt.Fprintf(&sb, "        \"traefik.http.routers.%s-http.entrypoints=web\",\n", id)
	fmt.Fprintf(&sb, "        \"traefik.http.routers.%s-https.rule=%s\",\n", id, r)
	fmt.Fprintf(&sb, "        \"traefik.http.routers.%s-https.entrypoints=websecure\",\n", id)
	fmt.Fprintf(&sb, "        \"traefik.http.routers.%s-https.tls=true\",\n", id)
	fmt.Fprintf(&sb, "        \"traefik.http.routers.%s-https.tls.certresolver=le\"\n", id)
	return sb.String()
}

func renderDaemon(id, dc, image string, req *api.DeployRequest, envBlock, volBlocks, volMounts, sidecars string) string {
	cmdBlock := renderCommandBlock(req.Command)
	return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "service"

  group "main" {
    count = %d
%s
    task "app" {
      driver = "docker"
      config {
        image = %q
%s%s      }
%s      resources {
        cpu    = %d
        memory = %d
      }
    }
%s  }
}
`, id, dc, req.Replicas, volBlocks, image, cmdBlock, volMounts, envBlock, req.CPU, req.Memory, sidecars)
}

func renderBatch(id, dc, image string, req *api.DeployRequest, envBlock, volBlocks, volMounts, sidecars string, periodic bool, cron string) string {
	periodicBlock := ""
	if periodic {
		periodicBlock = fmt.Sprintf(`
  periodic {
    cron             = %q
    prohibit_overlap = true
    time_zone        = "UTC"
  }
`, cron)
	}
	cmdBlock := renderCommandBlock(req.Command)
	return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "batch"
%s
  group "main" {
    count = %d
%s
    task "app" {
      driver = "docker"
      config {
        image = %q
%s%s      }
%s      resources {
        cpu    = %d
        memory = %d
      }
    }
%s  }
}
`, id, dc, periodicBlock, req.Replicas, volBlocks, image, cmdBlock, volMounts, envBlock, req.CPU, req.Memory, sidecars)
}

// renderCommandBlock writes Docker driver `command` and `args` HCL when set.
// Nomad's docker driver wants command/args separately, like Kubernetes.
func renderCommandBlock(command []string) string {
	if len(command) == 0 {
		return ""
	}
	if len(command) == 1 {
		return fmt.Sprintf("        command = %q\n", command[0])
	}
	parts := make([]string, len(command)-1)
	for i, a := range command[1:] {
		parts[i] = fmt.Sprintf("%q", a)
	}
	return fmt.Sprintf("        command = %q\n        args    = [%s]\n", command[0], strings.Join(parts, ", "))
}

// renderEnvBlock writes a single `env { ... }` block combining literal env
// values and secret-resolved values. Secrets are looked up by the server
// before this is called and converted into literal env entries.
func renderEnvBlock(env map[string]string, secrets []api.SecretBinding) string {
	combined := map[string]string{}
	for k, v := range env {
		combined[k] = v
	}
	// Secrets are resolved into env vars by the server prior to rendering;
	// they ride through this map with keys provided by the caller. The
	// SecretBinding list is here for forward compatibility only.
	_ = secrets
	if len(combined) == 0 {
		return ""
	}
	keys := make([]string, 0, len(combined))
	for k := range combined {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString("      env {\n")
	for _, k := range keys {
		fmt.Fprintf(&sb, "        %s = %q\n", k, combined[k])
	}
	sb.WriteString("      }\n")
	return sb.String()
}

// renderVolumes produces (unused group-level block, task-config-level mount blocks)
// for Docker named volumes scoped to the workload. Uses docker driver's `mount`
// stanza directly so no Nomad host_volume registration is required (matches
// postgres/valkey semantics — works on a fresh node out of the box).
func renderVolumes(vols []api.VolumeMount, namespacedID string) (string, string) {
	if len(vols) == 0 {
		return "", ""
	}
	var taskSB strings.Builder
	for _, v := range vols {
		dockerVol := fmt.Sprintf("blob-%s-%s", namespacedID, v.Name)
		fmt.Fprintf(&taskSB, "        mount {\n          type   = \"volume\"\n          target = %q\n          source = %q\n          readonly = false\n        }\n", v.Path, dockerVol)
	}
	return "", taskSB.String()
}

// renderSidecars returns extra `task` blocks for a Bundle.
func renderSidecars(s []api.Sidecar) string {
	if len(s) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, sc := range s {
		cpu := sc.CPU
		if cpu <= 0 {
			cpu = 100
		}
		mem := sc.Memory
		if mem <= 0 {
			mem = 128
		}
		argsHCL := ""
		if len(sc.Args) > 0 {
			parts := make([]string, len(sc.Args))
			for i, a := range sc.Args {
				parts[i] = fmt.Sprintf("%q", a)
			}
			argsHCL = fmt.Sprintf("        args  = [%s]\n", strings.Join(parts, ", "))
		}
		envBlock := renderEnvBlock(sc.Env, nil)
		fmt.Fprintf(&sb, `    task %q {
      driver = "docker"
      config {
        image = %q
%s      }
%s      resources {
        cpu    = %d
        memory = %d
      }
    }
`, sc.Name, sc.Image, argsHCL, envBlock, cpu, mem)
	}
	return sb.String()
}
