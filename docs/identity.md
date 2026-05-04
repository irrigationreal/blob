# Identity and scoped tokens

`BLOB_TOKEN` remains the owner break-glass credential. It authenticates as `owner`, has every scope, and should stay in `/etc/blob/env` with mode `0600`.

For automation, create scoped service tokens. Token metadata and grants live under:

```text
/srv/blob/identity/tokens/<token-id>.json
/srv/blob/identity/grants/<token-id>.json
```

Only token hashes are stored. The generated secret is shown once when the token is created.

## Commands

```sh
blob identity tokens create ci-deployer
blob identity tokens list
blob identity tokens revoke <token-id> --yes

blob identity grants add <token-id> deploy:write
blob identity grants list --token <token-id>
blob identity grants revoke <token-id> deploy:write --yes
```

Use a generated secret the same way as the owner token:

```sh
BLOB_TOKEN=blob_xxx blob whoami
BLOB_TOKEN=blob_xxx blob deploy
```

`blob whoami` prints the resolved actor id, display name, owner flag, and scopes.

## Scopes

| Scope | Allows |
|---|---|
| `deploy:write` | source uploads, deploys, app writes, scale, restart, destroy |
| `apps:read` | app list/status/release/log reads under `/v1/apps` |
| `secrets:read` | secret listing |
| `secrets:write` | secret create/update/delete |
| `audit:read` | audit log list/show |
| `identity:admin` | token and grant administration |
| `admin:read` | other read-only API surfaces |
| `admin:write` | other mutating API surfaces |
| `*` | all scopes |

The owner token bypasses scope checks. Service tokens only get what their grants contain. Revoked tokens stop authenticating immediately; their old grant files may remain on disk but no longer authorize anything.

## Audit actors

Audit events store the resolved actor id. Owner-token writes show `owner`; service-token writes show the token id, such as `tok-7c9d3a21b010`. Request and response bodies are still omitted, and the audit redaction rules still scrub DSNs, secrets, token-shaped values, and Nomad allocation ids from stored text fields.
