package server

import "fmt"

// renderPromtailJob writes a Nomad **system** job (one alloc per client node)
// for a Promtail data shipper. The host's Nomad alloc dir is bind-mounted
// read-only into the container; promtail tails it and pushes to Loki via
// /loki/api/v1/push.
//
// Two tasks in one group:
//
//  1. seed-positions (lifecycle prestart, sidecar=false) — a tiny busybox
//     job that walks /opt/nomad/data/alloc/*/alloc/logs/*.std*.[0-9]* and
//     writes a positions.yaml mapping every existing log file to its current
//     end-of-file byte offset. Without this seed, promtail's first-start
//     behavior is to slurp the entire historical content of every alloc log
//     on the node — easily hundreds of MB on a busy host. With the seed in
//     place, promtail attaches at the tail and only forwards lines written
//     AFTER it started. Net effect on the ingester: KB/s instead of MB/s on
//     boot.
//
//  2. promtail — the long-running shipper, using /alloc/data/positions.yaml
//     as its positions file (written by the prestart task).
//
// Both tasks share the alloc-local /alloc/data dir so the prestart's output
// is visible to the main task. We deliberately don't use /local/ because
// /local/ is template-rendered each run; alloc/data/ persists across
// restarts of the long-running task within the same alloc.
func renderPromtailJob(m *promtailMeta, dc, id string, cpu, memory int) string {
	pushURL := m.LokiURL + "/loki/api/v1/push"
	cfg := fmt.Sprintf(promtailConfigYAML, pushURL)

	return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "system"

  group "promtail" {
    count = 1

    task "seed-positions" {
      driver = "docker"
      lifecycle {
        hook    = "prestart"
        sidecar = false
      }
      config {
        image      = "busybox:1.37"
        entrypoint = ["sh", "-c"]
        args       = [%q]
        volumes = [
          "/opt/nomad/data/alloc:/opt/nomad/data/alloc:ro",
        ]
      }
      resources {
        cpu    = 50
        memory = 32
      }
    }

    task "promtail" {
      driver = "docker"
      config {
        image = "grafana/promtail:%s"
        args = [
          "-config.file=/etc/promtail/config.yml",
          "-config.expand-env=true",
        ]
        volumes = [
          "local/promtail-config.yml:/etc/promtail/config.yml:ro",
          "/opt/nomad/data/alloc:/opt/nomad/data/alloc:ro",
        ]
      }
      env {
        HOSTNAME = "${node.unique.name}"
      }
      resources {
        cpu    = %d
        memory = %d
      }

      template {
        destination = "local/promtail-config.yml"
        perms       = "0644"
        data        = <<EOH
%s
EOH
      }
    }
  }
}
`,
		id, dc,
		seedPositionsScript,
		m.Version,
		cpu, memory,
		cfg,
	)
}

// seedPositionsScript walks every existing alloc log file on the node and
// emits a YAML positions file pinning each path to its current end-of-file
// byte offset. Promtail will load this on startup and seek to those offsets,
// so it only forwards log lines written AFTER the seed ran.
//
// The output path is /alloc/data/positions.yaml — Nomad makes the alloc
// data dir available to every task in the group at /alloc/data, and it
// survives the prestart task's exit.
const seedPositionsScript = `set -e
out=/alloc/data/positions.yaml
echo "positions:" > "$out"
for f in /opt/nomad/data/alloc/*/alloc/logs/*.stdout.[0-9]* /opt/nomad/data/alloc/*/alloc/logs/*.stderr.[0-9]*; do
  [ -f "$f" ] || continue
  size=$(stat -c %s "$f" 2>/dev/null || echo 0)
  printf '  %s: "%s"\n' "$f" "$size" >> "$out"
done
echo "seeded $(grep -c ': "' "$out" 2>/dev/null || echo 0) positions to $out"
cat "$out" | head -5
`

// promtailConfigYAML is %s-formatted with the Loki push URL.
//
// positions filename lives in /alloc/data which is shared with the
// seed-positions prestart task and survives across promtail restarts.
const promtailConfigYAML = `server:
  http_listen_port: 0
  grpc_listen_port: 0

positions:
  filename: /alloc/data/positions.yaml

clients:
  - url: %s
    batchwait: 1s
    batchsize: 102400
    backoff_config:
      min_period: 500ms
      max_period: 5m
      max_retries: 10

scrape_configs:
  - job_name: nomad-allocs
    static_configs:
      - targets: [localhost]
        labels:
          job: nomad-alloc
          host: ${HOSTNAME}
          __path__: /opt/nomad/data/alloc/*/alloc/logs/*.std*.[0-9]*
    pipeline_stages:
      - regex:
          source: filename
          expression: '/opt/nomad/data/alloc/(?P<alloc>[^/]+)/alloc/logs/(?P<task>[^.]+)\.(?P<stream>stdout|stderr)\.(?P<idx>\d+)'
      - labels:
          alloc:
          task:
          stream:
`
