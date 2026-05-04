# Audit log

`blobd` keeps an append-only audit log for authenticated mutating API requests. Each event is hash-chained to the previous event so local tampering is detectable when the log is read back.

Events live at:

```text
/srv/blob/audit/events.jsonl
```

Each line is one JSON object. The file is opened with append mode and written `0600`.

## What gets logged

Authenticated `POST`, `PUT`, `PATCH`, and `DELETE` requests under `/v1/` are logged after the handler returns. Public reads such as `/healthz`, `/metrics`, `/status/<app>`, and GitHub webhook ingress are not logged by this surface.

An event records:

- event id and UTC timestamp
- bearer-token actor hash prefix, not the token itself
- method, sanitized path, and a small action label
- HTTP status code
- remote address and sanitized user agent
- previous event hash and this event hash

Request and response bodies are deliberately not stored. That keeps secret values, DSNs, and generated credentials out of the audit log even when the audited action is `blob secrets set` or a managed-service create.

## Read the log

```sh
blob audit list
blob audit list --limit 100
blob audit show <id>
```

Example:

```text
ID                             TIME                 METHOD   ACTION                       CODE PATH
aud-1777872523412-aabbccddeeff 2026-05-04T05:30:00Z POST     create status-pages          200  /v1/status-pages
```

`show` prints the full event JSON, including hash-chain fields:

```json
{
  "id": "aud-1777872523412-aabbccddeeff",
  "created_at": "2026-05-04T05:30:00Z",
  "actor": "bearer:8f14e45fceea",
  "method": "POST",
  "path": "/v1/status-pages",
  "action": "create status-pages",
  "status_code": 200,
  "previous_hash": "...",
  "hash": "..."
}
```

## Redaction

The audit writer redacts common sensitive shapes before persisting text fields:

- DSNs such as `postgres://user:password@host/db`
- query fragments like `password=...`, `token=...`, `secret=...`, `key=...`, `dsn=...`, `url=...`
- Nomad-style allocation UUIDs and 32-byte hex ids

The hash fields themselves are not redacted because they are the integrity chain.

## Integrity checks

`blob audit list` and `blob audit show` verify the chain while reading:

1. each event hash must match the event content with `hash` blanked
2. each event's `previous_hash` must match the previous line's hash

If a line is modified, removed, or reordered, the API returns an audit-chain error instead of silently serving a partial log.
