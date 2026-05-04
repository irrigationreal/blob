# Public TCP Services

`exposure: tcp` publishes a non-HTTP daemon through Traefik's TCP router. Use it for protocols that do not speak HTTP, small custom servers, databases you intentionally want to expose, or migration bridges.

## Quick start

```yaml
name: tcp-echo
form: daemon
image: hashicorp/http-echo:1.0.0
command: ["/http-echo", "-listen=:5678", "-text=tcp-ok"]
port: 5678
exposure: tcp
```

```sh
blob deploy
blob tcp add tcp-echo
blob tcp list
```

The add command allocates a public port from `20000-20099`, patches the edge Traefik job with a matching TCP entrypoint, and adds TCP router tags to the app job.

## Commands

```sh
blob tcp add <app> [--public-port P] [--target-port P]
blob tcp list
blob tcp show <public-port>
blob tcp remove <public-port> [--yes]
```

`--target-port` defaults to the app's manifest `port:`. `--public-port` is optional; when omitted Blob uses the next free port in the platform TCP pool.

## Manifest fields

`exposure: tcp` is only valid with `form: daemon`, because HTTP forms already publish through hostname routers and batch jobs do not have a long-running listener.

```yaml
name: smtp-relay
form: daemon
image: my-registry/smtp-relay:latest
port: 2525
exposure: tcp
cpu: 200
memory: 256
```

## Storage

Bindings are stored as JSON under `/srv/blob/tcp/<public-port>.json`:

```json
{
  "app": "tcp-echo",
  "host": "blob.example.com",
  "public_port": 20000,
  "target_port": 5678,
  "entrypoint": "blobtcp20000",
  "url": "tcp://blob.example.com:20000",
  "created_at": "2026-05-04T12:00:00Z"
}
```

Re-deploying the app preserves existing TCP bindings by re-applying their router tags to the rendered job file.

## Firewall

Open the platform TCP pool on the host:

```sh
sudo ufw allow 20000:20099/tcp comment "blob-public-tcp"
```

The current implementation is TCP-only. UDP is intentionally not exposed yet.
