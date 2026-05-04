package server

import "fmt"

// renderScyllaJob writes a Nomad service job for a single-node ScyllaDB.
//
// Mandatory args: --developer-mode 1 (skip kernel/io tuning),
// --memory 1G, --smp 1. Without these the container will grab every
// CPU and most of the box's RAM, OOM the host, or fail to start.
//
// Static host port → container 9042 (CQL native). Persistent
// /var/lib/scylla on a Docker named volume.
func renderScyllaJob(m *scyllaMeta, dc, id string, cpu, memory int) string {
	volume := "blob-scylladb-" + m.Name
	return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "service"

  group "scylladb" {
    count = 1

    network {
      port "cql" {
        static = %d
        to     = 9042
      }
    }

    service {
      name     = %q
      provider = "nomad"
      port     = "cql"
      check {
        type     = "tcp"
        port     = "cql"
        interval = "10s"
        timeout  = "3s"
      }
    }

    task "scylladb" {
      driver = "docker"
      config {
        image = "scylladb/scylla:%s"
        ports = ["cql"]
        args = [
          "--developer-mode", "1",
          "--memory", "768M",
          "--smp", "1",
          "--overprovisioned", "1",
          "--reserve-memory", "0",
          "--authenticator", "PasswordAuthenticator",
          "--authorizer", "CassandraAuthorizer",
        ]
        mount {
          type     = "volume"
          target   = "/var/lib/scylla"
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
		"scylladb-"+m.Name,
		m.Version,
		volume,
		cpu, memory,
	)
}
