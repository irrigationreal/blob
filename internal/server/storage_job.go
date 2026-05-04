package server

import "fmt"

// renderStorageJob writes a Nomad service job for a single-node MinIO.
// API on host port APIPort (forwarded to container :9000), console on
// UIPort (→ :9001). Persistent /data on a Docker named volume
// blob-storage-<name>.
//
// We deliberately don't expose either port through Traefik — the
// instance is for in-cluster app binding via `services: [<name>]`. If
// the operator wants public S3 access they can deploy a separate
// MinIO as a regular blob app (the v0.7 dogfood path remains valid).
func renderStorageJob(m *storageMeta, dc, id string, cpu, memory int) string {
	volume := "blob-storage-" + m.Name
	return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "service"

  group "storage" {
    count = 1

    network {
      port "api" {
        static = %d
        to     = 9000
      }
      port "ui" {
        static = %d
        to     = 9001
      }
    }

    service {
      name     = %q
      provider = "nomad"
      port     = "api"
      check {
        type     = "http"
        path     = "/minio/health/live"
        port     = "api"
        interval = "10s"
        timeout  = "3s"
      }
    }

    task "storage" {
      driver = "docker"
      config {
        image = "minio/minio:%s"
        ports = ["api", "ui"]
        args = [
          "server", "/data",
          "--address", ":9000",
          "--console-address", ":9001",
        ]
        mount {
          type     = "volume"
          target   = "/data"
          source   = %q
          readonly = false
        }
      }
      env {
        MINIO_ROOT_USER     = %q
        MINIO_ROOT_PASSWORD = %q
      }
      resources {
        cpu    = %d
        memory = %d
      }
    }
  }
}
`,
		id, dc,
		m.APIPort,
		m.UIPort,
		"storage-"+m.Name,
		m.Version,
		volume,
		m.AccessKey,
		m.SecretKey,
		cpu, memory,
	)
}
