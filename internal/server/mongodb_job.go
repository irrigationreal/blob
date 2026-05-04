package server

import "fmt"

// renderMongoJob writes a Nomad service job for a single-node MongoDB.
//
// Static host port → container 27017. Persistent /data/db on a Docker
// named volume. Root credentials provisioned by the official mongo
// image's docker-entrypoint via MONGO_INITDB_ROOT_USERNAME/PASSWORD.
//
// The app-level (user, database) tuple is provisioned via a generated
// init script written to NOMAD_TASK_DIR/local/init.js and bind-mounted
// into /docker-entrypoint-initdb.d/init.js. The mongo entrypoint runs
// any *.js files in that directory as the root user *before* the main
// mongod listener is declared ready — so by the time the Nomad health
// check flips, the app user already exists. No post-start hop, no
// second image pull.
func renderMongoJob(m *mongoMeta, dc, id string, cpu, memory int) string {
	volume := "blob-mongodb-" + m.Name
	initJS := fmt.Sprintf(`db.getSiblingDB(%q).createUser({ user: %q, pwd: %q, roles: [ { role: "readWrite", db: %q } ] });`,
		m.Database, m.User, m.Password, m.Database)
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

      template {
        destination = "local/init.js"
        data        = <<EOH
%s
EOH
      }

      config {
        image = "mongo:%s"
        ports = ["mongo"]
        mount {
          type     = "volume"
          target   = "/data/db"
          source   = %q
          readonly = false
        }
        mount {
          type     = "bind"
          source   = "local/init.js"
          target   = "/docker-entrypoint-initdb.d/init.js"
          readonly = true
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
		initJS,
		m.Version,
		volume,
		m.RootUser,
		m.RootPass,
		m.Database,
		cpu, memory,
	)
}
