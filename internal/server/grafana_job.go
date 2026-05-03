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

	dsYAML := emptyDatasourcesYAML
	if m.LokiURL != "" {
		dsYAML = fmt.Sprintf(lokiDatasourceYAML, m.LokiURL)
	}

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

// lokiDatasourceYAML is %s-formatted with the Loki base URL (http://host:port).
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
      "datasource": {"type": "loki", "uid": "Loki"},
      "fieldConfig": {"defaults": {}, "overrides": []},
      "gridPos": {"h": 22, "w": 24, "x": 0, "y": 0},
      "id": 1,
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
      "title": "All Blob apps",
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
  "title": "All Blob apps",
  "uid": "blob-all-apps",
  "version": 1,
  "weekStart": ""
}
`
