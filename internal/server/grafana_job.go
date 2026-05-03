package server

import (
	"fmt"
	"strings"
)

// renderGrafanaJob writes a Nomad service job for a Grafana instance.
//
// Provisioning is materialized at task-start via three Nomad `template`
// stanzas — no on-host files. The image's defaults are overridden by env:
//   GF_PATHS_PROVISIONING points at /local/provisioning
//   GF_SECURITY_ADMIN_USER / GF_SECURITY_ADMIN_PASSWORD set the admin login
//   GF_LOG_LEVEL=warn keeps the log volume tame
//
// The Loki datasource is provisioned only when the operator passed a
// --loki <name> at create time; otherwise we drop in an empty datasources
// file so Grafana still starts cleanly.
func renderGrafanaJob(m *grafanaMeta, dc, id string, cpu, memory int) string {
	volume := "blob-grafana-" + m.Name

	dsYAML := buildDatasourcesYAML(m.LokiURL, m.TempoURL, m.PrometheusURL)

	dashYAML := dashboardsProviderYAML
	dashJSON := allBlobAppsDashboardJSON

	return fmt.Sprintf(`job %q {
  datacenters = [%q]
  type = "service"

  group "grafana" {
    count = 1

    network {
      port "http" {
        static = %d
        to     = 3000
      }
    }

    service {
      name     = %q
      provider = "nomad"
      port     = "http"
      check {
        type     = "http"
        path     = "/api/health"
        port     = "http"
        interval = "10s"
        timeout  = "3s"
      }
    }

    task "grafana" {
      driver = "docker"
      config {
        image = "grafana/grafana:%s"
        ports = ["http"]
        mount {
          type     = "volume"
          target   = "/var/lib/grafana"
          source   = %q
          readonly = false
        }
        volumes = [
          "local/provisioning:/etc/grafana/provisioning",
        ]
      }
      env {
        GF_PATHS_PROVISIONING       = "/etc/grafana/provisioning"
        GF_SECURITY_ADMIN_USER      = "admin"
        GF_SECURITY_ADMIN_PASSWORD  = %q
        GF_LOG_LEVEL                = "warn"
        GF_AUTH_ANONYMOUS_ENABLED   = "false"
        GF_USERS_ALLOW_SIGN_UP      = "false"
      }
      resources {
        cpu    = %d
        memory = %d
      }

      template {
        destination = "local/provisioning/datasources/blob.yaml"
        perms       = "0644"
        data        = <<EOH
%s
EOH
      }

      template {
        destination = "local/provisioning/dashboards/blob.yaml"
        perms       = "0644"
        data        = <<EOH
%s
EOH
      }

      template {
        destination = "local/provisioning/dashboards/all-blob-apps.json"
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
		m.Port,
		"grafana-"+m.Name,
		m.Version,
		volume,
		m.AdminPassword,
		cpu, memory,
		strings.TrimRight(dsYAML, "\n"),
		strings.TrimRight(dashYAML, "\n"),
		strings.TrimRight(dashJSON, "\n"),
	)
}

const emptyDatasourcesYAML = `apiVersion: 1
datasources: []
`

// buildDatasourcesYAML composes the provisioning YAML from whichever of
// loki/tempo/prometheus the operator passed at create time. The first
// configured datasource is marked default in this priority: prometheus,
// loki, tempo. If none are set we emit the empty template so grafana still
// boots cleanly.
func buildDatasourcesYAML(lokiURL, tempoURL, prometheusURL string) string {
	if lokiURL == "" && tempoURL == "" && prometheusURL == "" {
		return emptyDatasourcesYAML
	}
	var sb strings.Builder
	sb.WriteString("apiVersion: 1\ndatasources:\n")
	defaulted := false
	if prometheusURL != "" {
		fmt.Fprintf(&sb, "  - name: Prometheus\n    type: prometheus\n    access: proxy\n    url: %s\n    isDefault: true\n    editable: true\n", prometheusURL)
		defaulted = true
	}
	if lokiURL != "" {
		fmt.Fprintf(&sb, "  - name: Loki\n    type: loki\n    access: proxy\n    url: %s\n    isDefault: %t\n    editable: true\n", lokiURL, !defaulted)
		defaulted = true
	}
	if tempoURL != "" {
		fmt.Fprintf(&sb, "  - name: Tempo\n    type: tempo\n    access: proxy\n    url: %s\n    isDefault: %t\n    editable: true\n", tempoURL, !defaulted)
	}
	return sb.String()
}

// lokiDatasourceYAML is retained for back-compat with older grafana metas
// that only stored LokiURL. New code paths use buildDatasourcesYAML.
const lokiDatasourceYAML = `apiVersion: 1
datasources:
  - name: Loki
    type: loki
    access: proxy
    url: %s
    isDefault: true
    editable: true
`

const dashboardsProviderYAML = `apiVersion: 1
providers:
  - name: blob
    orgId: 1
    folder: Blob
    type: file
    disableDeletion: false
    editable: true
    updateIntervalSeconds: 30
    options:
      path: /etc/grafana/provisioning/dashboards
`

// allBlobAppsDashboardJSON is a minimal Grafana 11 dashboard with one panel:
// a Logs panel querying Loki for {job=~".+"} so every Promtail-shipped stream
// shows up. Operators can save edits — they land in the Grafana DB volume.
const allBlobAppsDashboardJSON = `{
  "annotations": {"list": []},
  "editable": true,
  "fiscalYearStartMonth": 0,
  "graphTooltip": 0,
  "id": null,
  "links": [],
  "liveNow": false,
  "panels": [
    {
      "datasource": {"type": "prometheus", "uid": "Prometheus"},
      "fieldConfig": {"defaults": {"unit": "short"}, "overrides": []},
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 0},
      "id": 1,
      "options": {"legend": {"displayMode": "list"}},
      "targets": [
        {
          "datasource": {"type": "prometheus", "uid": "Prometheus"},
          "expr": "up",
          "legendFormat": "{{service}} ({{node}})",
          "refId": "A"
        }
      ],
      "title": "Targets up (Prometheus)",
      "type": "timeseries"
    },
    {
      "datasource": {"type": "tempo", "uid": "Tempo"},
      "gridPos": {"h": 8, "w": 12, "x": 12, "y": 0},
      "id": 2,
      "options": {},
      "targets": [
        {
          "datasource": {"type": "tempo", "uid": "Tempo"},
          "queryType": "traceqlSearch",
          "filters": [
            {"id": "service-name", "tag": "service.name", "operator": "=~", "value": [".+"], "scope": "resource"}
          ],
          "refId": "A"
        }
      ],
      "title": "Recent traces (Tempo)",
      "type": "table"
    },
    {
      "datasource": {"type": "loki", "uid": "Loki"},
      "fieldConfig": {"defaults": {}, "overrides": []},
      "gridPos": {"h": 14, "w": 24, "x": 0, "y": 8},
      "id": 3,
      "options": {
        "showLabels": true,
        "showCommonLabels": false,
        "showTime": true,
        "wrapLogMessage": true,
        "sortOrder": "Descending",
        "dedupStrategy": "none",
        "enableLogDetails": true
      },
      "targets": [
        {
          "datasource": {"type": "loki", "uid": "Loki"},
          "expr": "{job=~\".+\"}",
          "queryType": "range",
          "refId": "A"
        }
      ],
      "title": "All Blob app logs (Loki)",
      "type": "logs"
    }
  ],
  "refresh": "10s",
  "schemaVersion": 39,
  "tags": ["blob"],
  "templating": {"list": []},
  "time": {"from": "now-1h", "to": "now"},
  "timepicker": {},
  "timezone": "utc",
  "title": "Blob apps — logs / traces / metrics",
  "uid": "blob-all-apps",
  "version": 2,
  "weekStart": ""
}
`
