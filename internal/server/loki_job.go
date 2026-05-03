package server

import "fmt"

// renderLokiJob writes a Nomad service job for a Loki instance.
//
// Single-binary mode (-target=all). Filesystem store under /loki — chunks +
// boltdb-shipper index live on a Docker named volume blob-loki-<name>. Auth
// is disabled (auth_enabled: false) — Loki binds to the host's private port
// and is only reachable from the platform network. The image embeds a
// default config; we override only what we need via -config.expand-env=true
// and a small -- key=value command-line override list to keep this driver
// dependency-free (no template files on disk).
func renderLokiJob(m *lokiMeta, dc, id string, cpu, memory int) string {
	volume := "blob-loki-" + m.Name
	return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "service"

  group "loki" {
    count = 1

    network {
      port "http" {
        static = %d
        to     = 3100
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

    task "loki" {
      driver = "docker"
      config {
        image = "grafana/loki:%s"
        ports = ["http"]
        args = [
          "-config.file=/local/loki-config.yaml",
          "-target=all",
        ]
        mount {
          type     = "volume"
          target   = "/loki"
          source   = %q
          readonly = false
        }
      }
      resources {
        cpu    = %d
        memory = %d
      }

      template {
        destination = "local/loki-config.yaml"
        perms       = "0644"
        data        = <<EOH
auth_enabled: false

server:
  http_listen_port: 3100
  grpc_listen_port: 9096
  log_level: warn

common:
  instance_addr: 127.0.0.1
  path_prefix: /loki
  storage:
    filesystem:
      chunks_directory: /loki/chunks
      rules_directory: /loki/rules
  replication_factor: 1
  ring:
    kvstore:
      store: inmemory

# Tight memory profile — designed to run inside ~512MB.
# chunks flush early so the ingester doesn't accumulate large in-memory blocks.
ingester:
  chunk_idle_period: 30s
  chunk_target_size: 524288
  max_chunk_age: 1h
  chunk_retain_period: 30s
  wal:
    enabled: true
    dir: /loki/wal
    flush_on_shutdown: true

schema_config:
  configs:
    - from: 2020-10-24
      store: tsdb
      object_store: filesystem
      schema: v13
      index:
        prefix: index_
        period: 24h

# Disable the query scheduler — single-tenant local mode doesn't need it
# and it adds resident overhead.
query_scheduler:
  max_outstanding_requests_per_tenant: 2048

querier:
  max_concurrent: 4

frontend:
  max_outstanding_per_tenant: 2048

limits_config:
  ingestion_rate_mb: 64
  ingestion_burst_size_mb: 128
  per_stream_rate_limit: 16MB
  per_stream_rate_limit_burst: 32MB
  max_streams_per_user: 0
  reject_old_samples: false
  allow_structured_metadata: true
  retention_period: 168h

ruler:
  alertmanager_url: http://localhost:9093
EOH
      }
    }
  }
}
`,
		id, dc,
		m.Port,
		"loki-"+m.Name,
		m.Version,
		volume,
		cpu, memory,
	)
}
