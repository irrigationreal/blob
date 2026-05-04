# Uptime monitors

Blob can run persisted HTTP checks from blobd and attach the result to public status pages. Monitors are stored under the state dir and checked by the control-plane process, so they do not create Nomad allocations.

```sh
blob monitors add my-app --path /healthz --interval 60 --timeout 5
blob monitors list
blob monitors show my-app
blob monitors remove my-app --yes
```

`blob monitors add <app>` uses the app's current public URL by default. Pass `--path /some/path` to check a specific route. For an external URL, pass `--name <name> --url https://example.com/health` and optionally `--app <app>` if it should appear on that app's status page.

Options:

| Flag | Meaning |
|---|---|
| `--name` | Monitor name. Defaults to the app name. |
| `--url` | Full HTTP(S) URL to check. If omitted, Blob uses the app URL. |
| `--path` | Path appended to the app URL when `--url` is omitted. |
| `--interval` | Check interval in seconds. Default 60, minimum 15. |
| `--timeout` | Per-check timeout in seconds. Default 5, allowed 1-60. |
| `--status` | Expected HTTP status. Default 200. |
| `--webhook` | Alert webhook URL. Blob POSTs on up/down transitions. |
| `--disabled` | Create the monitor without scheduling checks. |

Webhook payloads include monitor name, app, URL, previous status, current status, check time, status code, and sanitized error text. Alerts fire only after Blob has a previous check state and the state changes between `up` and `down`.

Public status pages include monitors for the same app, but they do not expose webhook URLs, failure counters, allocation IDs, or secrets.
