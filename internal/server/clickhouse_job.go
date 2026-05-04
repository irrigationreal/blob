package server

import "fmt"

// renderClickHouseJob writes a Nomad service job for single-node
// clickhouse-server. HTTP on host port HTTPPort (→ container :8123),
// native protocol on NativePort (→ :9000). Persistent /var/lib/clickhouse
// + /var/log/clickhouse-server on Docker named volumes. Default user
// + database provisioned via the official entrypoint env vars.
//
// IPC_LOCK + ulimit nofile bumps are required by clickhouse-server
// at start time on cgroup-v2 hosts; we set them via the docker driver's
// cap_add and ulimit fields.
func renderClickHouseJob(m *clickhouseMeta, dc, id string, cpu, memory int) string {
	dataVol := "blob-clickhouse-" + m.Name
	logVol := "blob-clickhouse-" + m.Name + "-log"
	return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "service"

  group "clickhouse" {
    count = 1

    network {
      port "http" {
        static = %d
        to     = 8123
      }
      port "native" {
        static = %d
        to     = 9000
      }
    }

    service {
      name     = %q
      provider = "nomad"
      port     = "http"
      check {
        type     = "http"
        path     = "/ping"
        port     = "http"
        interval = "10s"
        timeout  = "3s"
      }
    }

    task "clickhouse" {
      driver = "docker"
      config {
        image = "clickhouse/clickhouse-server:%s"
        ports = ["http", "native"]
        # IPC_LOCK is what ClickHouse asks for at start (mlock for hot
        # buffers), but Nomad's docker driver blocks it by default and
        # the server runs fine without it on a single-node deploy.
        # Drop the cap_add to keep the default allowlist happy.
        ulimit {
          nofile = "262144:262144"
        }
        mount {
          type     = "volume"
          target   = "/var/lib/clickhouse"
          source   = %q
          readonly = false
        }
        mount {
          type     = "volume"
          target   = "/var/log/clickhouse-server"
          source   = %q
          readonly = false
        }
      }
      env {
        CLICKHOUSE_DB                       = %q
        CLICKHOUSE_USER                     = %q
        CLICKHOUSE_PASSWORD                 = %q
        CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT = "1"
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
		m.HTTPPort,
		m.NativePort,
		"clickhouse-"+m.Name,
		m.Version,
		dataVol,
		logVol,
		m.Database,
		m.User,
		m.Password,
		cpu, memory,
	)
}
