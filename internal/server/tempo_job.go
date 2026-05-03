package server

import "fmt"

// renderTempoJob writes a Nomad service job for a single-binary Tempo.
//
// Two ports: HTTP API (3200 inside) and OTLP gRPC ingest (4317 inside).
// Both are exposed as static host ports. Local-block storage on a Docker
// named volume blob-tempo-<name>.
func renderTempoJob(m *tempoMeta, dc, id string, cpu, memory int) string {
	volume := "blob-tempo-" + m.Name
	return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "service"

  group "tempo" {
    count = 1

    network {
      port "http" {
        static = %d
        to     = 3200
      }
      port "otlp" {
        static = %d
        to     = 4317
      }
    }

    service {
      name     = %q
      provider = "nomad"
      port     = "http"
      check {
        type     = "http"
        path     = "/ready"
        port     = "http"
        interval = "10s"
        timeout  = "3s"
      }
    }

    task "tempo" {
      driver = "docker"
      config {
        image = "grafana/tempo:%s"
        ports = ["http", "otlp"]
        args = [
          "-config.file=/local/tempo-config.yaml",
        ]
        mount {
          type     = "volume"
          target   = "/var/tempo"
          source   = %q
          readonly = false
        }
      }
      resources {
        cpu    = %d
        memory = %d
      }

      template {
        destination = "local/tempo-config.yaml"
        perms       = "0644"
        data        = <<EOH
server:
  http_listen_port: 3200
  log_level: warn

distributor:
  receivers:
    otlp:
      protocols:
        grpc:
          endpoint: 0.0.0.0:4317

ingester:
  trace_idle_period: 10s
  max_block_duration: 5m

compactor:
  compaction:
    block_retention: 168h

storage:
  trace:
    backend: local
    local:
      path: /var/tempo/traces
    wal:
      path: /var/tempo/wal

usage_report:
  reporting_enabled: false
EOH
      }
    }
  }
}
`,
		id, dc,
		m.HTTPPort,
		m.OTLPPort,
		"tempo-"+m.Name,
		m.Version,
		volume,
		cpu, memory,
	)
}
