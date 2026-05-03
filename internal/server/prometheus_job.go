package server

import "fmt"

// renderPrometheusJob writes a Nomad service job for Prometheus.
//
// Scrape config covers:
//   - blobd /metrics on the platform host
//   - Traefik /metrics on the platform host (whatever-port; default :8082)
//   - Nomad service discovery — every blob-managed workload registers
//     `provider = "nomad"` so we pull the entire fleet for free.
//
// hostIP is the platform host's reachable IP (the same one used for
// postgres/valkey URL synthesis). We use it for the static targets so the
// container-side scraper reaches services via the host's NAT bridge.
func renderPrometheusJob(m *prometheusMeta, dc, id string, cpu, memory int, hostIP, region string) string {
	volume := "blob-prometheus-" + m.Name
	return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "service"

  group "prometheus" {
    count = 1

    network {
      port "http" {
        static = %d
        to     = 9090
      }
    }

    service {
      name     = %q
      provider = "nomad"
      port     = "http"
      check {
        type     = "http"
        path     = "/-/ready"
        port     = "http"
        interval = "10s"
        timeout  = "3s"
      }
    }

    task "prometheus" {
      driver = "docker"
      config {
        image = "prom/prometheus:%s"
        ports = ["http"]
        args = [
          "--config.file=/etc/prometheus/prometheus.yml",
          "--storage.tsdb.path=/prometheus",
          "--storage.tsdb.retention.time=15d",
          "--web.enable-lifecycle",
          "--web.console.templates=/etc/prometheus/consoles",
          "--web.console.libraries=/etc/prometheus/console_libraries",
        ]
        mount {
          type     = "volume"
          target   = "/prometheus"
          source   = %q
          readonly = false
        }
        volumes = [
          "local/prometheus.yml:/etc/prometheus/prometheus.yml:ro",
        ]
      }
      resources {
        cpu    = %d
        memory = %d
      }

      template {
        destination = "local/prometheus.yml"
        perms       = "0644"
        data        = <<EOH
global:
  scrape_interval: 30s
  evaluation_interval: 30s
  external_labels:
    cluster: blob
    host: %s

scrape_configs:
  - job_name: blobd
    metrics_path: /metrics
    static_configs:
      - targets: ['172.17.0.1:8787']
        labels:
          service: blobd

  - job_name: traefik
    metrics_path: /metrics
    static_configs:
      - targets: ['172.17.0.1:8082']
        labels:
          service: traefik

  # Self-scrape so 'up{job="prometheus"} == 1' is meaningful.
  - job_name: prometheus
    static_configs:
      - targets: ['localhost:9090']
        labels:
          service: prometheus

  # Nomad service discovery — every blob-managed workload registers as a
  # Nomad service. Prometheus pulls the full list and scrapes /metrics on
  # each one. Workloads that don't expose /metrics return 404 and show as
  # 'up == 0' which is fine — that's how operators discover unmonitored
  # apps.
  - job_name: cadvisor
    metrics_path: /metrics
    static_configs:
      - targets: ['172.17.0.1:18080']
        labels:
          service: cadvisor

  - job_name: nomad-services
    nomad_sd_configs:
      - server: 'http://172.17.0.1:4646'
        region: '%s'
    relabel_configs:
      - source_labels: [__meta_nomad_service]
        target_label: service
      - source_labels: [__meta_nomad_node]
        target_label: node
EOH
      }
    }
  }
}
`,
		id, dc,
		m.Port,
		"prometheus-"+m.Name,
		m.Version,
		volume,
		cpu, memory,
		hostIP,
		region,
	)
}
