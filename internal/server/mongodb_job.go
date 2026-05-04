package server

import "fmt"

// renderMongoJob writes a Nomad service job for a single-node MongoDB.
//
// Static host port → container 27017. Persistent /data/db on a Docker
// named volume. Root credentials provisioned by the official mongo
// image's docker-entrypoint via MONGO_INITDB_ROOT_USERNAME/PASSWORD.
// The app-level (user, database) tuple is created post-start by
// ensureMongoUser via a one-shot mongosh container — the image's
// MONGO_INITDB_DATABASE only creates the database, not a non-root user
// in it.
func renderMongoJob(m *mongoMeta, dc, id string, cpu, memory int) string {
	volume := "blob-mongodb-" + m.Name
	return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "service"

  group "mongodb" {
    count = 1

    network {
      port "mongo" {
        static = %d
        to     = 27017
      }
    }

    service {
      name     = %q
      provider = "nomad"
      port     = "mongo"
      check {
        type     = "tcp"
        port     = "mongo"
        interval = "10s"
        timeout  = "3s"
      }
    }

    task "mongodb" {
      driver = "docker"
      config {
        image = "mongo:%s"
        ports = ["mongo"]
        mount {
          type     = "volume"
          target   = "/data/db"
          source   = %q
          readonly = false
        }
      }
      env {
        MONGO_INITDB_ROOT_USERNAME = %q
        MONGO_INITDB_ROOT_PASSWORD = %q
        MONGO_INITDB_DATABASE      = %q
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
		"mongodb-"+m.Name,
		m.Version,
		volume,
		m.RootUser,
		m.RootPass,
		m.Database,
		cpu, memory,
	)
}
