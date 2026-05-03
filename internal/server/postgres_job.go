package server

import (
	"fmt"
	stdlog2 "log"
)

func stdLog(format string, a ...any) {
	stdlog2.Printf(format, a...)
}

// renderPostgresJob writes a Nomad service job for a Postgres instance.
//
// Storage uses a Docker named volume mounted directly via the docker driver's
// `mount` block — no Nomad host_volume registration required, so this works
// on a stock Nomad client.
//
// Network: a static host port (m.Port) is mapped to container port 5432.
func renderPostgresJob(m *postgresMeta, dc, id string) string {
	volume := "blob-pg-" + m.Name
	return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "service"

  group "pg" {
    count = 1

    network {
      port "pg" {
        static = %d
        to     = 5432
      }
    }

    service {
      name     = %q
      provider = "nomad"
      port     = "pg"
      check {
        type     = "tcp"
        port     = "pg"
        interval = "10s"
        timeout  = "3s"
      }
    }

    task "pg" {
      driver = "docker"
      config {
        image = "postgres:%s-alpine"
        ports = ["pg"]
        args  = ["-c", "listen_addresses=*"]
        mount {
          type   = "volume"
          target = "/var/lib/postgresql/data"
          source = %q
          readonly = false
        }
      }
      env {
        POSTGRES_USER     = %q
        POSTGRES_PASSWORD = %q
        POSTGRES_DB       = %q
        PGDATA            = "/var/lib/postgresql/data/pgdata"
      }
      resources {
        cpu    = 500
        memory = 512
      }
    }
  }
}
`,
		id, dc,
		m.Port,
		"pg-"+m.Name,
		m.Version,
		volume,
		m.User, m.Password, m.Database,
	)
}
