package server

import "fmt"

// renderNATSJob writes a Nomad service job for a single-node NATS with
// JetStream. Static client port (4222 inside → m.Port outside), monitor
// port not exposed externally. JetStream data persists on a Docker
// named volume.
func renderNATSJob(m *natsMeta, dc, id string, cpu, memory int) string {
	volume := "blob-nats-" + m.Name
	return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "service"

  group "nats" {
    count = 1

    network {
      port "client" {
        static = %d
        to     = 4222
      }
    }

    service {
      name     = %q
      provider = "nomad"
      port     = "client"
      check {
        type     = "tcp"
        port     = "client"
        interval = "10s"
        timeout  = "3s"
      }
    }

    task "nats" {
      driver = "docker"
      config {
        image = "nats:%s"
        ports = ["client"]
        args = [
          "-js",
          "-sd", "/data",
          "-m", "8222",
          "--name", %q,
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
		"nats-"+m.Name,
		m.Version,
		"nats-"+m.Name,
		volume,
		cpu, memory,
	)
}
