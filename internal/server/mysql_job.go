package server

import "fmt"

// renderMySQLJob writes a Nomad service job for a single-node MySQL.
//
// Static host port → container 3306. Persistent /var/lib/mysql on a
// Docker named volume. Root password set via env; an app-level
// (user, database) is provisioned by the official mysql:8 image's
// docker-entrypoint MYSQL_USER / MYSQL_PASSWORD / MYSQL_DATABASE
// env vars (only on first start with an empty data dir).
func renderMySQLJob(m *mysqlMeta, dc, id string, cpu, memory int) string {
	volume := "blob-mysql-" + m.Name
	return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "service"

  group "mysql" {
    count = 1

    network {
      port "mysql" {
        static = %d
        to     = 3306
      }
    }

    service {
      name     = %q
      provider = "nomad"
      port     = "mysql"
      check {
        type     = "tcp"
        port     = "mysql"
        interval = "10s"
        timeout  = "3s"
      }
    }

    task "mysql" {
      driver = "docker"
      config {
        image = "mysql:%s"
        ports = ["mysql"]
        # No CLI args needed: MySQL 8.x defaults to caching_sha2_password
        # already, and the explicit --default-authentication-plugin flag
        # was removed in 8.4. Leaving args empty keeps the image's
        # entrypoint-driven config path clean across 8.0/8.4.
        mount {
          type     = "volume"
          target   = "/var/lib/mysql"
          source   = %q
          readonly = false
        }
      }
      env {
        MYSQL_ROOT_PASSWORD = %q
        MYSQL_DATABASE      = %q
        MYSQL_USER          = %q
        MYSQL_PASSWORD      = %q
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
		"mysql-"+m.Name,
		m.Version,
		volume,
		m.RootPass,
		m.Database,
		m.User,
		m.Password,
		cpu, memory,
	)
}
