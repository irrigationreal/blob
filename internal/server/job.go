package server

import (
	"fmt"
	"sort"
	"strings"

	"github.com/irrigationreal/blob/internal/api"
)

// renderJob writes an HCL Nomad job for a single Component (DeployRequest).
// Supports forms: web-service, function, daemon, job (batch), cronjob (periodic batch).
// Bundles are expressed as a single group with one primary task plus sidecars.
func renderJob(req *api.DeployRequest, image string, port int, domain, dc string, namespacedID string) string {
	normalizeDeployRequestForRender(req)
	form := req.Form

	isolation := normalizeIsolation(req.Isolation)
	envBlock := renderEnvBlock(req.Env, req.Secrets)
	volumeBlocks, volumeMounts := renderVolumes(req.Volumes, namespacedID)
	sidecars := renderSidecars(req.Sidecars, isolation)

	switch form {
	case "job":
		return renderBatch(namespacedID, dc, image, req, envBlock, volumeBlocks, volumeMounts, sidecars, false, "")
	case "cronjob":
		return renderBatch(namespacedID, dc, image, req, envBlock, volumeBlocks, volumeMounts, sidecars, true, req.Schedule)
	case "daemon":
		return renderDaemon(namespacedID, dc, image, req, envBlock, volumeBlocks, volumeMounts, sidecars)
	case "static", "function":
		return renderWebService(namespacedID, dc, image, port, domain, req, envBlock, volumeBlocks, volumeMounts, sidecars)
	default: // web-service
		return renderWebService(namespacedID, dc, image, port, domain, req, envBlock, volumeBlocks, volumeMounts, sidecars)
	}
}

func normalizeDeployRequestForRender(req *api.DeployRequest) {
	if req.CPU <= 0 {
		if req.Form == "function" {
			req.CPU = 100
		} else {
			req.CPU = 500
		}
	}
	if req.Memory <= 0 {
		if req.Form == "function" {
			req.Memory = 128
		} else {
			req.Memory = 512
		}
	}
	if req.Replicas <= 0 {
		req.Replicas = 1
	}
	req.Exposure = strings.ToLower(strings.TrimSpace(req.Exposure))
	if req.Form == "" {
		req.Form = "web-service"
	}
	if normalizeIsolation(req.Isolation) == "" {
		req.Isolation = ""
	}
}

func validateTCPExposureRequest(req *api.DeployRequest) error {
	if req.Exposure == "" {
		return nil
	}
	if req.Exposure != "tcp" {
		return fmt.Errorf("unknown exposure %q", req.Exposure)
	}
	if req.Form != "daemon" {
		return fmt.Errorf("exposure: tcp requires form: daemon")
	}
	if req.Port <= 0 {
		return fmt.Errorf("exposure: tcp requires port")
	}
	return nil
}

func normalizeIsolation(isolation string) string {
	switch strings.ToLower(strings.TrimSpace(isolation)) {
	case "kata":
		return "kata"
	default:
		return ""
	}
}

func renderProjectionMeta(hash string) string {
	if hash == "" {
		return ""
	}
	return fmt.Sprintf(`  meta {
    blob_projection_hash = %q
  }

`, hash)
}

func renderIsolationConstraint(isolation string) string {
	if normalizeIsolation(isolation) != "kata" {
		return ""
	}
	return `    constraint {
      attribute = "${meta.blob_kata}"
      value     = "true"
    }
`
}

func renderDockerRuntime(isolation string) string {
	if normalizeIsolation(isolation) != "kata" {
		return ""
	}
	return `        runtime = "kata-runtime"
`
}

func renderWebService(id, dc, image string, port int, domain string, req *api.DeployRequest, envBlock, volBlocks, volMounts, sidecars string) string {
	cmdBlock := renderCommandBlock(req.Command)
	traefikTags := renderTraefikTags(id, domain, req.Domains)
	projectionMeta := renderProjectionMeta(req.ProjectionHash)
	constraintBlock := renderIsolationConstraint(req.Isolation)
	runtimeBlock := renderDockerRuntime(req.Isolation)
	return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "service"
%s
  group "web" {
    count = %d
%s%s
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
%s%s%s      }
%s      resources {
        cpu    = %d
        memory = %d
      }
    }
%s  }
}
`, id, dc, projectionMeta, req.Replicas, constraintBlock, volBlocks, port, id, traefikTags, image, runtimeBlock, cmdBlock, volMounts, envBlock, req.CPU, req.Memory, sidecars)
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
	projectionMeta := renderProjectionMeta(req.ProjectionHash)
	constraintBlock := renderIsolationConstraint(req.Isolation)
	runtimeBlock := renderDockerRuntime(req.Isolation)
	if req.Exposure == "tcp" {
		return renderTCPDaemon(id, dc, image, req, envBlock, volBlocks, volMounts, sidecars, cmdBlock, projectionMeta, constraintBlock, runtimeBlock)
	}
	return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "service"
%s
  group "main" {
    count = %d
%s%s
    task "app" {
      driver = "docker"
      config {
        image = %q
%s%s%s      }
%s      resources {
        cpu    = %d
        memory = %d
      }
    }
%s  }
}
`, id, dc, projectionMeta, req.Replicas, constraintBlock, volBlocks, image, runtimeBlock, cmdBlock, volMounts, envBlock, req.CPU, req.Memory, sidecars)
}

func renderTCPDaemon(id, dc, image string, req *api.DeployRequest, envBlock, volBlocks, volMounts, sidecars, cmdBlock, projectionMeta, constraintBlock, runtimeBlock string) string {
	return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "service"
%s
  group "main" {
    count = %d
%s%s
    network {
      port "tcp" {
        to = %d
      }
    }

    service {
      name     = %q
      provider = "nomad"
      port     = "tcp"
      tags = [
      ]
    }

    task "app" {
      driver = "docker"
      config {
        image = %q
        ports = ["tcp"]
%s%s%s      }
%s      resources {
        cpu    = %d
        memory = %d
      }
    }
%s  }
}
`, id, dc, projectionMeta, req.Replicas, constraintBlock, volBlocks, req.Port, id, image, runtimeBlock, cmdBlock, volMounts, envBlock, req.CPU, req.Memory, sidecars)
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
	projectionMeta := renderProjectionMeta(req.ProjectionHash)
	constraintBlock := renderIsolationConstraint(req.Isolation)
	runtimeBlock := renderDockerRuntime(req.Isolation)
	return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "batch"
%s%s
  group "main" {
    count = %d
%s%s
    task "app" {
      driver = "docker"
      config {
        image = %q
%s%s%s      }
%s      resources {
        cpu    = %d
        memory = %d
      }
    }
%s  }
}
`, id, dc, projectionMeta, periodicBlock, req.Replicas, constraintBlock, volBlocks, image, runtimeBlock, cmdBlock, volMounts, envBlock, req.CPU, req.Memory, sidecars)
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
func renderSidecars(s []api.Sidecar, isolation string) string {
	if len(s) == 0 {
		return ""
	}
	runtimeBlock := renderDockerRuntime(isolation)
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
%s%s      }
%s      resources {
        cpu    = %d
        memory = %d
      }
    }
`, sc.Name, sc.Image, runtimeBlock, argsHCL, envBlock, cpu, mem)
	}
	return sb.String()
}
