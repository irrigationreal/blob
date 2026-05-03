package server

import "fmt"

// renderValkeyJob writes a Nomad service job for a Valkey instance.
//
// AOF persistence is enabled (--appendonly yes) so writes survive restarts.
// The data dir is /data, mounted as a Docker named volume blob-valkey-<name>.
// AUTH is required (--requirepass <pw>); apps connect via redis://:pw@host:port.
func renderValkeyJob(m *valkeyMeta, dc, id string, cpu, memory int) string {
	volume := "blob-valkey-" + m.Name
	return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "service"

  group "valkey" {
    count = 1

    network {
      port "redis" {
        static = %d
        to     = 6379
      }
    }

    service {
      name     = %q
      provider = "nomad"
      port     = "redis"
      check {
        type     = "tcp"
        port     = "redis"
        interval = "10s"
        timeout  = "3s"
      }
    }

    task "valkey" {
      driver = "docker"
      config {
        image = "valkey/valkey:%s-alpine"
        ports = ["redis"]
        args = [
          "--requirepass", %q,
          "--appendonly", "yes",
          "--dir", "/data"
        ]
        mount {
          type     = "volume"
          target   = "/data"
          source   = %q
          readonly = false
        }
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
		m.Port,
		"valkey-"+m.Name,
		m.Version,
		m.Password,
		volume,
		cpu, memory,
	)
}
