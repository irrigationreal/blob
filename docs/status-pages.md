# Status pages

`blob status-pages` publishes a small public page for an app. The page is served by `blobd` at `https://blob.<base-domain>/status/<app>` with a matching JSON feed at `https://blob.<base-domain>/status/<app>.json`.

The public payload contains:

- app name, form, public URL, Nomad job status, and running replica count
- route probe result for the app URL, including HTTP status code and latency
- current relevant doctor issues for the app, plus cluster-wide P1/P2 issues
- uptime monitors attached to the app via `blob monitors add <app>`
- open incidents created with `blob status-pages incident open`

It does not expose secret values, monitor webhook URLs, or Nomad allocation IDs. Doctor and incident text is sanitized before it is rendered publicly.

## Enable a status page

```sh
blob status-pages enable my-app
```

Example output:

```text
enabled status page for my-app
url:     https://blob.example.com/status/my-app
overall: operational
```

The binding is persisted at `/srv/blob/status-pages/<app>.json` and survives `blobd` restarts.

## List and inspect pages

```sh
blob status-pages list
blob status-pages show my-app
```

`show` calls the same status builder used by the public page, so it refreshes app status, route health, monitors, incidents, and doctor issues.

## Incidents

```sh
blob status-pages incident open my-app --title "Elevated errors" --impact major --message "Investigating request failures"
blob status-pages incident update inc-123 --message "Errors are dropping"
blob status-pages incident resolve inc-123 --message "Recovered"
blob status-pages incident list --app my-app
```

Open incidents appear on the public HTML page and JSON feed. `critical` incidents mark the page `down`; open `minor` and `major` incidents mark it `degraded`; `maintenance` incidents stay visible without degrading the page.

## Public endpoints

```sh
curl https://blob.example.com/status/my-app
curl https://blob.example.com/status/my-app.json
```

The HTML endpoint is meant for humans. The JSON endpoint is stable enough for monitors and external uptime badges:

```json
{
  "app": "my-app",
  "url": "https://blob.example.com/status/my-app",
  "overall": "operational",
  "app_status": {
    "app": "my-app",
    "form": "web-service",
    "domain": "my-app.example.com",
    "url": "https://my-app.example.com",
    "status": "running",
    "replicas": 1
  },
  "route_health": {
    "url": "https://my-app.example.com",
    "status": "reachable",
    "ok": true,
    "status_code": 200,
    "latency_ms": 42
  },
  "monitors": [
    {
      "name": "my-app",
      "url": "https://my-app.example.com/healthz",
      "health": {"status": "reachable", "ok": true, "status_code": 200}
    }
  ],
  "incidents": [
    {
      "id": "inc-123",
      "app": "my-app",
      "title": "Elevated errors",
      "status": "open",
      "impact": "major",
      "latest_message": "Investigating request failures"
    }
  ],
  "doctor_issues": []
}
```

`overall` is `operational`, `degraded`, or `down`. A dead or missing app is `down`. A running app with a failing attached monitor or open critical incident is also `down`. A running app with a failing route probe, relevant P2 issue, or open non-maintenance incident is `degraded`.

## Disable a status page

```sh
blob status-pages disable my-app --yes
```

This removes the binding file. It does not destroy the app.
