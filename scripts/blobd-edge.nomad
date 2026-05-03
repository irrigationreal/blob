job "blobd-edge" {
  datacenters = ["pve"]
  type = "service"

  group "edge" {
    count = 1
    network {
      port "http" {
        static = 18787
      }
    }

    service {
      name     = "blobd-edge"
      provider = "nomad"
      port     = "http"
      tags = [
        "traefik.enable=true",
        "traefik.http.routers.blobd-http.rule=Host(`blob.irrigate.cc`)",
        "traefik.http.routers.blobd-http.entrypoints=web",
        "traefik.http.routers.blobd-https.rule=Host(`blob.irrigate.cc`)",
        "traefik.http.routers.blobd-https.entrypoints=websecure",
        "traefik.http.routers.blobd-https.tls=true",
        "traefik.http.routers.blobd-https.tls.certresolver=le"
      ]
    }

    task "proxy" {
      driver = "docker"
      config {
        image        = "alpine/socat:1.8.0.0"
        network_mode = "host"
        args = [
          "TCP-LISTEN:18787,fork,reuseaddr",
          "TCP:127.0.0.1:8787"
        ]
      }
      resources {
        cpu    = 50
        memory = 32
      }
    }
  }
}
